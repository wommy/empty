// Package endpoint implements idempotent volume and snapshot lifecycle on
// ZFS. It owns what CSI front ends would otherwise each re-implement:
// name-keyed idempotency, locking, metadata, and the CSI error contract.
//
// The design is data-oriented. One scan reads the relevant subtree into a
// Table of typed rows (table.go); pure planners turn (Table, request) into a
// Plan of mutations or an error (plan.go); apply executes it (apply.go).
// Idempotency is "the plan is empty"; recovery is "re-scan and re-plan".
//
// State lives in ZFS user properties under the endpoint's own namespace
// (DefaultPropertyPrefix). The property names follow democratic-csi's, so
// with its prefix (adoption mode) the volumes it provisioned are adopted.
// ADR 0003.
package endpoint

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/wommy/empty/internal/lock"
	"github.com/wommy/empty/internal/zfs"
)

// VolumeKind is a filesystem dataset or a zvol.
type VolumeKind int

const (
	Filesystem VolumeKind = iota
	Block
)

func (k VolumeKind) String() string {
	if k == Block {
		return "block"
	}
	return "filesystem"
}

// DefaultPropertyPrefix is the endpoint's own user-property namespace.
// "democratic-csi" is the adoption mode: it reads (and writes) that
// driver's properties, so the volumes it provisioned are adopted.
const DefaultPropertyPrefix = "zfs-endpoint"

// Property names under a prefix.
type props struct{ p string }

func (n props) all() []string {
	return []string{n.managed(), n.success(), n.volumeName(), n.sourceType(), n.sourceID(),
		n.snapshotName(), n.snapshotSource(), n.nfsClients(), n.cloneSourceFor(), n.committedAt(), n.nfsServer()}
}

// validPrefix reports why prefix cannot name OpenZFS user properties:
// zfs_prop.c zfs_prop_user accepts only a-z 0-9 - _ . : (the ':' we
// append makes it a user property), and zfs_check_userprops refuses a name
// of ZAP_MAXNAMELEN (256) bytes or more. Checked at startup, so a bad
// prefix is not first found by zfs refusing every create.
func validPrefix(prefix string) error {
	if prefix == "" {
		return errors.New("empty")
	}
	for _, c := range []byte(prefix) {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.IndexByte("-_.:", c) >= 0) {
			return fmt.Errorf("%q is not allowed in a zfs user property (a-z 0-9 - _ . :)", c)
		}
	}
	for _, n := range (props{prefix}).all() {
		if len(n) >= 256 {
			return fmt.Errorf("property %s is %d bytes, zfs allows 255", n[:20]+"...", len(n))
		}
	}
	return nil
}

func (n props) managed() string        { return n.p + ":managed_resource" }
func (n props) success() string        { return n.p + ":provision_success" }
func (n props) volumeName() string     { return n.p + ":csi_volume_name" }
func (n props) sourceType() string     { return n.p + ":csi_volume_content_source_type" }
func (n props) sourceID() string       { return n.p + ":csi_volume_content_source_id" }
func (n props) snapshotName() string   { return n.p + ":csi_snapshot_name" }
func (n props) snapshotSource() string { return n.p + ":csi_snapshot_source_volume_id" }

// cloneSourceFor is the endpoint's own property (not the baseline's): set
// locally on the internal snapshot a volume-to-volume clone hangs off, at
// its creation (`zfs snapshot -o`), naming the clone it was cut for. With
// managed_resource it is what makes such a snapshot the endpoint's; the
// reserved name alone is not ownership (anyone can `zfs snapshot` it).
func (n props) cloneSourceFor() string { return n.p + ":clone_source_for" }

// committedAt is the endpoint's own property: the Unix time (endpoint
// clock) CreateVolume committed the volume, set with provision_success.
// Reconcile ages a committed volume from it, not from its zfs creation: a
// provision retried long after its first attempt is new to the caller.
func (n props) committedAt() string { return n.p + ":committed_at" }

// nfsClients is the client list earlier versions stored and computed
// sharenfs from. Export state now lives in sharenfs alone (ADR 0004): the
// property is never written or decided on, only read so Reconcile can
// report a leftover one.
func (n props) nfsClients() string { return n.p + ":nfs_clients" }

// nfsServer is the endpoint's own property: the NFS server address a
// volume exported at create was created with, set in the same `zfs create`
// (or clone) as its sharenfs. CSI and Kubernetes hold it fixed for the
// volume's life (a PV's volume attributes are immutable), and OpenZFS
// keeps no such address, so it is recorded once. It is addressing
// metadata, not export state (ADR 0004: who may mount is sharenfs alone).
// Read only when set locally: an inherited or received one is not this
// volume's (Reconcile reports it).
func (n props) nfsServer() string { return n.p + ":nfs_server" }

// baseline's name for the internal snapshot a volume-to-volume clone hangs off
const cloneSourceSnapshotPrefix = "volume-source-for-volume-"

// Config of an Endpoint.
type Config struct {
	// Parent dataset volumes are created under, e.g. "tank/k8s/vols".
	Parent string
	// PropertyPrefix for user properties. Default DefaultPropertyPrefix;
	// "democratic-csi" is the adoption mode.
	PropertyPrefix string
	// VolBlockSize used to pre-round zvol sizes for limit checks. Default
	// 16 KiB. zfs may round further; reported sizes are always re-read.
	VolBlockSize uint64
	// LockWait bounds how long an operation waits for a busy resource
	// before returning Aborted. Default 30s, and always inside the caller's
	// deadline.
	LockWait time.Duration
	// Now is the clock reconcile ages rows against. Default time.Now.
	Now func() time.Time
	// FileExport enables NFS export (Publish/UnpublishFileExport). Exports
	// need root: libzfs writes /etc/exports.d and runs exportfs as the
	// caller, and only root can mount. The caller decides (e.g. from the
	// effective uid); unset, those calls are Unimplemented.
	//
	// It also says the endpoint can mount: CreateVolume then commits a
	// filesystem volume zfs would mount only once it is mounted, and
	// mounts one the create left unmounted (`zfs mount`) before the commit
	// (Planner.RequireMounted). Without root (-allow-non-root) nothing can
	// mount, and volumes commit unmounted.
	FileExport bool
	// Etab reads the kernel export table to verify exports. Default
	// /var/lib/nfs/etab.
	Etab EtabReader
	// NFSD reports the nfsd thread count; Publish succeeds only while
	// nfsd runs. Default: Etab when it also implements NFSDReader (the
	// simulator), else /proc/fs/nfsd/threads.
	NFSD NFSDReader
	// NFSMinPrefix is the broadest client network PublishFileExport and
	// CreateVolume's NFS export accept: an IPv4 prefix length, 1-32.
	// Default DefaultNFSMinPrefix (/16). Unpublish takes any network, and
	// the floor is not part of the ownership grammar (ParseShareNFS), so
	// raising it never strands an export.
	NFSMinPrefix int
	// NFSServers are the addresses (IPv4 or DNS names) NFS clients mount
	// this endpoint's volumes from, in order of preference: what
	// CreateVolumeRequest.NFS.Server chooses among, and the first is the
	// default. Advertise stable names (a VIP or a DNS name): a volume
	// records the address it was created with, and the PVs that hold it
	// can never be changed. Needs FileExport. Empty: CreateVolume takes no
	// NFS export.
	NFSServers []string
	// DestroyForeignSnapshots lets DeleteVolume destroy a volume together
	// with snapshots the endpoint did not cut (no locally set
	// csi_snapshot_name or clone-source stamp: an administrator's, a backup
	// tool's, a received one), each by name. Default false: such a volume
	// is refused with FailedPrecondition naming them (ADR 0003: never touch
	// what you don't own). Reconcile never uses it: an operator opts in
	// for a per-volume delete, not for a bulk collection.
	DestroyForeignSnapshots bool
	// Settle waits until the host's device manager has handled the device
	// events already raised (UdevSettle: `udevadm settle`). A zvol is
	// long-held while anything has its /dev/zdN open, and its destroy is
	// then EBUSY. Right after `zfs create -V` (or a clone) udev's rules for
	// the new device (zvol_id, blkid) hold it open for as long as udev
	// takes to reach the event, so a delete soon after a create can meet a
	// hold nobody asked for. A busy destroy therefore settles once and
	// retries once; still busy is a volume in use (FAILED_PRECONDITION).
	// OpenZFS does the same in its own create error path: it waits out
	// the zvol's async minor handling and retries the destroy
	// (zfs_ioctl.c zfs_ioc_create). Nil: no settle and no retry (the
	// simulator, which has no devices).
	Settle func(context.Context) error
}

// Endpoint is the volume service.
type Endpoint struct {
	zfs   *zfs.Client
	cfg   Config
	names props
	plan  Planner
	locks lock.Keyed
	// scans holds a slot per whole-parent scan in flight (maxWholeScans)
	scans chan struct{}
}

// MaxParentLen is the longest parent dataset name New accepts: zfs names are
// at most 255 bytes, and a volume (<parent>/<id>) or snapshot
// (<parent>/<volume>@<name>) id is up to 128 bytes, so a longer parent
// would refuse some CSI names that are valid everywhere else. Refused at
// startup rather than on each CreateVolume.
const MaxParentLen = 255 - 1 - 128

// New returns an Endpoint.
func New(z *zfs.Client, cfg Config) (*Endpoint, error) {
	if !zfs.ValidDataset(cfg.Parent) {
		return nil, fmt.Errorf("invalid parent dataset %q", cfg.Parent)
	}
	if len(cfg.Parent) > MaxParentLen {
		return nil, fmt.Errorf("parent dataset %q is %d bytes, over %d: its volumes' names (up to 128 bytes more) would exceed zfs's 255", cfg.Parent, len(cfg.Parent), MaxParentLen)
	}
	if cfg.PropertyPrefix == "" {
		cfg.PropertyPrefix = DefaultPropertyPrefix
	}
	if err := validPrefix(cfg.PropertyPrefix); err != nil {
		return nil, fmt.Errorf("property prefix %q: %w", cfg.PropertyPrefix, err)
	}
	if cfg.VolBlockSize == 0 {
		cfg.VolBlockSize = 16 * 1024
	}
	if cfg.LockWait == 0 {
		cfg.LockWait = 30 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Etab == nil {
		cfg.Etab = DefaultEtab
	}
	if cfg.NFSD == nil {
		cfg.NFSD = DefaultNFSD
		if r, ok := cfg.Etab.(NFSDReader); ok {
			cfg.NFSD = r
		}
	}
	if cfg.NFSMinPrefix == 0 {
		cfg.NFSMinPrefix = DefaultNFSMinPrefix
	}
	if cfg.NFSMinPrefix < 1 || cfg.NFSMinPrefix > 32 {
		return nil, fmt.Errorf("NFS minimum prefix /%d: want 1-32", cfg.NFSMinPrefix)
	}
	if len(cfg.NFSServers) > 0 && !cfg.FileExport {
		return nil, errors.New("NFS servers are advertised, but file export is off (it needs root)")
	}
	for i, s := range cfg.NFSServers {
		if err := ValidNFSServer(s); err != nil {
			return nil, err
		}
		if slices.Contains(cfg.NFSServers[:i], s) {
			return nil, fmt.Errorf("NFS server %q is listed twice", s)
		}
	}
	cfg.NFSServers = slices.Clone(cfg.NFSServers)
	names := props{cfg.PropertyPrefix}
	return &Endpoint{
		zfs:   z,
		cfg:   cfg,
		names: names,
		scans: make(chan struct{}, maxWholeScans),
		plan: Planner{Parent: cfg.Parent, Names: names, VolBlockSize: cfg.VolBlockSize, DestroyForeignSnapshots: cfg.DestroyForeignSnapshots,
			RequireMounted: cfg.FileExport, NFSServers: cfg.NFSServers, NFSMinPrefix: cfg.NFSMinPrefix},
	}, nil
}

// ValidNFSServer checks an NFS server address as -nfs-server takes it: an
// IPv4 address in canonical form or a DNS name (letters, digits, '-' and
// '.', labels of 1-63 bytes, 253 bytes at most). csi-driver-nfs mounts
// "<server>:<share>", so nothing else may reach it.
func ValidNFSServer(s string) error {
	if a, err := netip.ParseAddr(s); err == nil {
		if a.Is4() && a.String() == s {
			return nil
		}
		return fmt.Errorf("NFS server %q: want an IPv4 address or a DNS name (IPv6 is not supported yet)", s)
	}
	bad := fmt.Errorf("NFS server %q: want an IPv4 address or a DNS name", s)
	if s == "" || len(s) > 253 {
		return bad
	}
	labels := strings.Split(strings.TrimSuffix(s, "."), ".")
	if last := labels[len(labels)-1]; last == "" || !(last[0] >= 'a' && last[0] <= 'z' || last[0] >= 'A' && last[0] <= 'Z') {
		// a name whose last label starts with a digit is an address to
		// inet_aton, which mount.nfs uses: "192.0.2.010" is octal,
		// "0xc0000210" hex; only a top-level label that starts with a
		// letter is a name (RFC 1123 2.1)
		return bad
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return bad
		}
		for _, c := range []byte(l) {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return bad
			}
		}
	}
	return nil
}

// Volume as returned to front ends.
type Volume struct {
	ID            string
	Name          string
	Dataset       string
	Kind          VolumeKind
	CapacityBytes uint64
	// SourceType is "snapshot", "volume" or "".
	SourceType string
	SourceID   string
	// DevicePath for Block (on the storage host), Mountpoint for Filesystem.
	DevicePath string
	Mountpoint string
	// NFS, for a filesystem with a recorded NFS server or an export in
	// the endpoint's grammar: Server is the address recorded at create,
	// Clients and ReadOnly the local sharenfs (ReadOnly: every client).
	NFS *NFSExport
}

// NFSExport is a static NFS export, given with CreateVolume.
type NFSExport struct {
	// Clients are IPv4 networks with an explicit prefix length and host
	// bits zero ("10.20.0.0/24", "10.20.0.5/32"), at or above
	// Config.NFSMinPrefix, none overlapping.
	Clients  []string
	ReadOnly bool
	// Server is one of Config.NFSServers; "" is the first.
	Server string
}

// Snapshot as returned to front ends.
type Snapshot struct {
	ID             string // "<volume id>@<name>", the baseline's native form
	Name           string
	SourceVolumeID string
	SizeBytes      uint64 // restore size: the source volume's capacity
	CreationUnix   int64
	ReadyToUse     bool
}

// CreateVolumeRequest mirrors csi.CreateVolumeRequest minus transport.
type CreateVolumeRequest struct {
	Name          string
	Kind          VolumeKind
	CapacityBytes uint64 // required bytes; 0 means unlimited for Filesystem
	LimitBytes    uint64 // 0 = no limit
	// Sparse: zvol without refreservation. Reserve: filesystem with one.
	Sparse  bool
	Reserve bool
	// at most one of these
	SourceSnapshotID string
	SourceVolumeID   string
	// Properties are native ZFS properties applied at create, checked
	// against a per-kind allowlist (props.go). A committed volume is
	// compatible only if each one is set locally to the same value.
	Properties map[string]string
	// NFS exports a filesystem at create (needs Config.NFSServers): the
	// volume is committed only once the NFS server serves every client.
	// A committed volume is compatible only with the same clients and
	// mode, and with the same server or none.
	NFS *NFSExport
}

func (e *Endpoint) dataset(id string) string { return e.cfg.Parent + "/" + id }

// volumeID validates a CSI name as a dataset component. The baseline uses
// the name directly; ids must also fit CSI's 128-byte limit.
func volumeID(name string) (string, error) {
	if name == "" || len(name) > 128 || strings.ContainsAny(name, "/@") || !zfs.ValidDataset(name) {
		return "", errorf(InvalidArgument, "invalid volume name %q", name)
	}
	return name, nil
}

// issuedVolumeID validates a volume id a caller hands back. Ids are opaque
// to callers, so a non-empty one this endpoint could never have issued
// names no volume: NotFound (DeleteVolume turns that into OK). A missing
// id stays InvalidArgument.
func issuedVolumeID(id string) (string, error) {
	if _, err := volumeID(id); err != nil {
		if id == "" {
			return "", err
		}
		return "", errorf(NotFound, "volume %q not found: not an id this endpoint issues", id)
	}
	return id, nil
}

func parseSnapshotID(id string) (vol, snap string, err error) {
	i := strings.IndexByte(id, '@')
	if i <= 0 || i == len(id)-1 || strings.Count(id, "@") != 1 {
		return "", "", errorf(InvalidArgument, "invalid snapshot id %q", id)
	}
	vol, snap = id[:i], id[i+1:]
	if _, err := volumeID(vol); err != nil {
		return "", "", err
	}
	if !zfs.ValidSnapshot("x@" + snap) {
		return "", "", errorf(InvalidArgument, "invalid snapshot id %q", id)
	}
	return vol, snap, nil
}

// lockMargin is left before the caller's deadline so a busy resource is
// reported as Aborted (retried by the CSI sidecars) rather than surfacing as
// the caller's own DEADLINE_EXCEEDED (external-provisioner calls time out
// after 10s by default).
const lockMargin = time.Second

func (e *Endpoint) lock(ctx context.Context, keys ...string) (func(), error) {
	wait := e.cfg.LockWait
	if dl, ok := ctx.Deadline(); ok {
		if left := time.Until(dl) - lockMargin; left < wait {
			wait = max(left, 0)
		}
	}
	if wait <= 0 && ctx.Err() == nil {
		// no time left to wait (the deadline is inside lockMargin, or
		// LockWait is not positive), but a free lock is still taken: a
		// WithTimeout(0) would refuse even an uncontended one
		if release, ok := e.locks.TryAcquire(keys...); ok {
			return release, nil
		}
		return nil, errorf(Aborted, "operation pending on %v", keys)
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	release, err := e.locks.Acquire(ctx, keys...)
	if err != nil {
		return nil, errorf(Aborted, "operation pending on %v", keys)
	}
	return release, nil
}

// zfsErr maps a zfs failure to an endpoint Code.
func zfsErr(err error, what string) error {
	if err == nil {
		return nil
	}
	code := Internal
	switch {
	case errors.Is(err, zfs.ErrNotFound), errors.Is(err, zfs.ErrParentNotFound):
		code = NotFound
	case errors.Is(err, zfs.ErrExists):
		code = AlreadyExists
	case errors.Is(err, zfs.ErrHasClones), errors.Is(err, zfs.ErrHasChildren), errors.Is(err, zfs.ErrPrecondition),
		errors.Is(err, zfs.ErrPermission): // a missing zfs allow grant
		// states only the caller or an administrator changes: final
		code = FailedPrecondition
	case errors.Is(err, zfs.ErrBusy):
		// an open zvol, a mounted filesystem in use, a user hold: clears
		// by itself, so retryable here. A destroy is the exception: CSI
		// wants FAILED_PRECONDITION for a volume or snapshot in use on
		// DeleteVolume/DeleteSnapshot (apply maps by op).
		code = Unavailable
	case errors.Is(err, zfs.ErrInterrupted):
		// killed by the caller's context: the op may or may not have run,
		// so never Internal; a retry re-scans and re-plans
		code = Aborted
		if errors.Is(err, context.DeadlineExceeded) {
			code = DeadlineExceeded
		}
	case errors.Is(err, zfs.ErrNoSpace):
		code = ResourceExhausted
	case errors.Is(err, zfs.ErrInvalidProperty):
		code = InvalidArgument
	}
	var ze *zfs.Error
	if errors.As(err, &ze) && ze.Kind == zfs.KindUnavailable {
		code = Unavailable
	}
	return &Error{Code: code, Msg: what, Err: err}
}

// parentUnavailable reports a parent zfs cannot open. zfs prints the same
// "dataset does not exist" for an exported pool as for a missing dataset,
// and the parent is configuration, so it is never "absent": Unavailable is
// retried, NotFound is final for the CSI sidecars.
func parentUnavailable(parent string, err error) error {
	return &Error{Code: Unavailable, Msg: "parent dataset " + parent + " is unavailable", Err: err}
}

// userValue reads a property value; "-" means unset only when zfs says
// so with a source other than local ("-" for an unset user property). A
// value set locally is the value, even a literal "-": `zfs set ns:prop=-`
// is legal, and reading it as unset would, for instance, turn a
// csi_volume_name of "-" into "no name marker", which OwnedVolume accepts
// for any id.
func userValue(p zfs.Prop) string {
	if p.Value == "-" && !p.Local() {
		return ""
	}
	return p.Value
}

// ---- views: rows to API values ---------------------------------------------

func volumeView(r Row) Volume {
	v := Volume{
		ID: r.ID, Name: r.VolumeName, Dataset: r.Name, Kind: r.Kind(),
		CapacityBytes: r.Capacity(), SourceType: r.SourceType, SourceID: r.SourceID,
	}
	if v.Kind == Block {
		v.DevicePath = "/dev/zvol/" + r.Name
		return v
	}
	v.Mountpoint = r.Mount
	if cs := clientsOf(r); r.NFSServer != "" || len(cs) > 0 {
		v.NFS = &NFSExport{Server: r.NFSServer, ReadOnly: len(cs) > 0}
		for _, c := range cs {
			v.NFS.Clients = append(v.NFS.Clients, c.Host)
			v.NFS.ReadOnly = v.NFS.ReadOnly && c.ReadOnly
		}
	}
	return v
}

func snapshotView(t Table, r Row) Snapshot {
	vol, _, _ := strings.Cut(r.ID, "@")
	s := Snapshot{
		ID: r.ID, Name: r.SnapshotName, SourceVolumeID: vol,
		CreationUnix: r.Creation,
		ReadyToUse:   true, // zfs snapshots are atomic and immediately usable
		SizeBytes:    r.Volsize,
	}
	// filesystem snapshots carry no refquota: restore size is the source's
	if s.SizeBytes == 0 {
		if src, ok := t.Get(vol); ok {
			s.SizeBytes = src.Capacity()
		}
	}
	return s
}

// ---- operations: lock, scan, plan, apply, re-read ---------------------------

// CreateVolume provisions a volume, returns the existing one when the
// request is compatible (CSI idempotency), or completes a provision that was
// interrupted before its commit marker.
func (e *Endpoint) CreateVolume(ctx context.Context, r CreateVolumeRequest) (Volume, error) {
	id, _, err := e.plan.validateCreate(r)
	if err != nil {
		return Volume{}, err
	}
	keys := []string{"vol:" + id}
	if r.SourceSnapshotID != "" {
		keys = append(keys, "snap:"+r.SourceSnapshotID)
		if vol, _, err := parseSnapshotID(r.SourceSnapshotID); err == nil {
			keys = append(keys, "vol:"+vol)
		}
	}
	if r.SourceVolumeID != "" {
		keys = append(keys, "vol:"+r.SourceVolumeID)
	}
	release, err := e.lock(ctx, keys...)
	if err != nil {
		return Volume{}, err
	}
	defer release()

	// A create plan that leaves its commit to a re-scan (a filesystem
	// whose mount only a scan can confirm) takes a second pass: the
	// resume, which commits or says why not. Two passes at most.
	for pass := 0; ; pass++ {
		t, err := e.scanFor(ctx, id, r.SourceVolumeID, r.SourceSnapshotID)
		if err != nil {
			return Volume{}, err
		}
		p := e.plan
		p.now = e.cfg.Now().Unix()
		plan, err := p.PlanCreateVolume(t, r)
		if err != nil {
			return Volume{}, err
		}
		commit := Plan(nil)
		if r.NFS != nil && p.commits(plan) {
			// an NFS volume commits only once it is served (verifyExport)
			plan, commit = plan[:len(plan)-1], plan[len(plan)-1:]
		}
		if err := e.apply(ctx, plan); err != nil {
			if row, ok := t.Get(id); ok && !row.Committed {
				return Volume{}, resumeErr(err)
			}
			return Volume{}, err
		}
		if commit != nil {
			if err := e.verifyExport(ctx, id); err != nil {
				return Volume{}, err
			}
			if err := e.apply(ctx, commit); err != nil {
				return Volume{}, resumeErr(err)
			}
		}
		if pass > 0 || len(plan)+len(commit) == 0 || commit != nil || p.commits(plan) {
			break
		}
	}
	return e.readVolume(ctx, id)
}

// verifyExport holds an NFS volume's commit until the NFS server serves
// its export: `zfs create -o sharenfs` exits 0 when the share or exportfs
// fails (csi-nfs-design §4, Fact A). Each client must be served in its
// mode, by its own line, and nothing beyond the export; a miss re-shares
// once (the value unchanged), then is Unavailable, uncommitted, and a
// retry converges. nfsd must run: etab alone is no proof of a server.
func (e *Endpoint) verifyExport(ctx context.Context, id string) error {
	for try := 0; ; try++ {
		t, err := e.scan(ctx, id)
		if err != nil {
			return err
		}
		row, ok := t.Get(id)
		if !ok || !row.OwnedVolume() {
			return errorf(Aborted, "volume %s vanished or changed owner during provisioning", id)
		}
		want := clientsOf(row)
		if len(want) == 0 {
			// the resume set the requested export just before this scan
			return errorf(Aborted, "volume %s: sharenfs changed during provisioning (now %q); retry", id, row.ShareNFS)
		}
		live, err := e.exportTable(ctx, t, id)
		if err != nil {
			return err
		}
		var missing []string
		for _, c := range want {
			if !live.serves(row.Mount, c) {
				missing = append(missing, c.Host+" "+c.mode())
			}
		}
		if x := live.exposure(realPath(row.Mount), want); len(x) > 0 && len(missing) == 0 {
			return errorf(FailedPrecondition, "volume %s is served beyond its export by the NFS export table: %s; "+
				"remove these exports (an ancestor dataset's sharenfs with crossmnt, e.g. \"on\", or /etc/exports); the volume is not committed", id, strings.Join(x, "; "))
		}
		if len(missing) == 0 {
			return e.nfsdRunning(ctx)
		}
		if try > 0 {
			return errorf(Unavailable, "sharenfs on %s is set, but the NFS export table does not serve %s to %s: "+
				"zfs does not report exportfs failures; check exportfs and the NFS server on the storage host; the volume is not committed",
				row.Name, row.Mount, strings.Join(missing, ", "))
		}
		if err := e.apply(ctx, reshare(row, want)); err != nil {
			return resumeErr(err)
		}
	}
}

// resumeErr maps the failure of a resumed provision. An earlier attempt
// already changed the volume, so an unclassified zfs failure is Aborted
// even on the resume's first op, as it was on the attempt that left the
// row (opErr): a final Internal would strand the half-made volume, and
// flip the code a caller saw for the same state.
func resumeErr(err error) error {
	var e *Error
	if errors.As(err, &e) && e.Code == Internal && e.Err != nil {
		return &Error{Code: Aborted, Msg: e.Msg + " (resuming an earlier attempt; a retry converges)", Err: e.Err}
	}
	return err
}

// scanFor reads the volumes an operation touches (one zfs call each).
func (e *Endpoint) scanFor(ctx context.Context, ids ...string) (Table, error) {
	var rows []Row
	var covered []string
	seen := map[string]bool{}
	for _, id := range ids {
		vol, _, _ := strings.Cut(id, "@")
		if vol == "" || seen[vol] {
			continue
		}
		seen[vol] = true
		if _, err := volumeID(vol); err != nil {
			continue // invalid ids simply have no rows; planners report them
		}
		t, err := e.scan(ctx, vol)
		if err != nil {
			return Table{}, err
		}
		rows = slices.AppendSeq(rows, t.All())
		covered = append(covered, vol)
	}
	return newPartialTable(rows, covered...), nil
}

func (e *Endpoint) readVolume(ctx context.Context, id string) (Volume, error) {
	t, err := e.scan(ctx, id)
	if err != nil {
		return Volume{}, err
	}
	row, ok := t.Get(id)
	if !ok || !row.ServedVolume() {
		// only ours is ever returned: something replaced it after the plan
		return Volume{}, errorf(Aborted, "volume %s vanished or changed owner after provisioning", id)
	}
	return volumeView(row), nil
}

// DeleteVolume destroys a volume. Missing volumes succeed (CSI). Like the
// baseline, volumes with CSI snapshots or dependent clones fail with
// FailedPrecondition and are left untouched.
func (e *Endpoint) DeleteVolume(ctx context.Context, volID string) error {
	if _, err := issuedVolumeID(volID); err != nil {
		if CodeOf(err) == NotFound {
			return nil // never issued, so never existed: CSI says OK
		}
		return err
	}
	// A delete that releases a clone origin (<src>@volume-source-for-
	// volume-<id>) touches the source volume as well, so it holds vol:<src>
	// too, as Reconcile does. Which source is known only from the scan,
	// after vol:<id> is held. Lock order, so this cannot invert against a
	// CreateVolume from that source (which waits for {vol:<new>, vol:<src},
	// sorted):
	//   - every waiting acquisition takes its whole key set at once, in
	//     sorted order (lock.Keyed.Acquire);
	//   - a key added while others are held is only ever tried, never
	//     waited for (TryAcquire). Busy, everything is released and the
	//     whole set is waited for, sorted, then scanned and planned again.
	// Bounded: the set only grows; a plan that keeps naming new sources is
	// Aborted (retried by the sidecars).
	vols := []string{volID}
	for range 3 {
		again, err := e.deleteLocked(ctx, volID, &vols)
		if !again {
			return err
		}
	}
	return errorf(Aborted, "volume %s: the volumes its delete touches kept changing; retry", volID)
}

// deleteLocked is one DeleteVolume pass holding vol:<v> for each of *vols.
// again reports that the plan touches volumes not held and not free to
// take now: they were added to *vols, for the next pass to wait for.
func (e *Endpoint) deleteLocked(ctx context.Context, volID string, vols *[]string) (again bool, err error) {
	release, err := e.lock(ctx, volKeys(*vols)...)
	if err != nil {
		return false, err
	}
	defer release()
	// clones of this volume's snapshots live elsewhere under the parent
	t, err := e.scan(ctx, "")
	if err != nil {
		return false, err
	}
	plan, err := e.plan.PlanDeleteVolume(t, volID)
	if err != nil {
		return false, err
	}
	var extra []string
	for _, v := range e.plan.volumesOf(plan) {
		if !slices.Contains(*vols, v) {
			extra = append(extra, v)
		}
	}
	if len(extra) > 0 {
		more, ok := e.locks.TryAcquire(volKeys(extra)...)
		if !ok {
			*vols = append(*vols, extra...)
			return true, nil
		}
		defer more()
	}
	return false, e.apply(ctx, plan)
}

func volKeys(vols []string) []string {
	keys := make([]string, len(vols))
	for i, v := range vols {
		keys[i] = "vol:" + v
	}
	return keys
}

// ExpandVolume grows a volume to at least capacity. Shrinking is a no-op.
func (e *Endpoint) ExpandVolume(ctx context.Context, volID string, capacity, limit uint64) (Volume, error) {
	if _, err := issuedVolumeID(volID); err != nil {
		return Volume{}, err
	}
	release, err := e.lock(ctx, "vol:"+volID)
	if err != nil {
		return Volume{}, err
	}
	defer release()
	t, err := e.scan(ctx, volID)
	if err != nil {
		return Volume{}, err
	}
	plan, before, err := e.plan.PlanExpandVolume(t, volID, capacity, limit)
	if err != nil {
		return Volume{}, err
	}
	if err := e.apply(ctx, plan); err != nil {
		if rerr := e.rollbackExpand(ctx, before); rerr != nil {
			return Volume{}, errors.Join(err, fmt.Errorf("rolling refquota back: %w", rerr))
		}
		return Volume{}, err
	}
	v, err := e.readVolume(ctx, volID)
	if err == nil && v.Kind == Filesystem && v.CapacityBytes == 0 {
		// unlimited (no refquota) already satisfies the request, and a
		// quota would shrink it; capacity after expansion is reported as
		// what was asked for, never 0 (CSI: capacity_bytes is required)
		v.CapacityBytes = capacity
		if v.CapacityBytes == 0 {
			v.CapacityBytes = limit
		}
	}
	return v, err
}

// rollbackExpand puts back a refquota a failed grow raised without its
// reservation (PlanExpandRollback), still under the volume's lock. It runs
// even when the caller's context ended (that may be why the grow failed),
// bounded by its own timeout.
func (e *Endpoint) rollbackExpand(ctx context.Context, before Row) error {
	if before.ID == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
	defer cancel()
	t, err := e.scan(ctx, before.ID)
	if err != nil {
		return err
	}
	return e.apply(ctx, e.plan.PlanExpandRollback(t, before))
}

// rollbackTimeout bounds rollbackExpand: one scan and one `zfs set`.
const rollbackTimeout = 30 * time.Second

// GetVolume returns a managed volume.
func (e *Endpoint) GetVolume(ctx context.Context, volID string) (Volume, error) {
	if _, err := issuedVolumeID(volID); err != nil {
		return Volume{}, err
	}
	t, err := e.scan(ctx, volID)
	if err != nil {
		return Volume{}, err
	}
	row, ok := t.Get(volID)
	if !ok || !row.ServedVolume() {
		return Volume{}, errorf(NotFound, "volume %s not found", volID)
	}
	return volumeView(row), nil
}

// tokenPrefix starts every page token: "after/<last id served>". "/" is in
// no volume or snapshot id, so a token is never mistaken for an id, and a
// string the endpoint never issued (csi-sanity's "invalid-token", a bare
// id from a build before this format) is Aborted rather than read as a
// position. A walk across that upgrade restarts once.
const tokenPrefix = "after/"

// volumeToken and snapshotToken report whether a token's id could be one
// the list serves; the ids need not exist any more.
func volumeToken(id string) bool { return id != "" && !strings.ContainsAny(id, "/@") }

func snapshotToken(id string) bool {
	vol, snap, ok := strings.Cut(id, "@")
	return ok && volumeToken(vol) && snap != "" && !strings.ContainsAny(snap, "/@")
}

// page returns items (sorted by id) after startingToken, at most max (0 =
// all), and the next token. A token resumes at the first id after the one
// it names, whether or not that item still exists: a volume deleted
// between pages (normal churn while a CO lists) must not end the walk. A
// token the endpoint could not have issued is Aborted (CSI).
func page[T any](items []T, id func(T) string, valid func(string) bool, max int, startingToken string) ([]T, string, error) {
	start := 0
	if startingToken != "" {
		after, ok := strings.CutPrefix(startingToken, tokenPrefix)
		if !ok || !valid(after) {
			return nil, "", errorf(Aborted, "invalid starting token %q", startingToken)
		}
		i, found := slices.BinarySearchFunc(items, after, func(it T, after string) int { return strings.Compare(id(it), after) })
		if found {
			i++
		}
		start = i
	}
	items = items[start:]
	if max > 0 && len(items) > max {
		return items[:max], tokenPrefix + id(items[max-1]), nil
	}
	return items, "", nil
}

// ListVolumes pages through managed, committed volumes ordered by id.
func (e *Endpoint) ListVolumes(ctx context.Context, maxEntries int, startingToken string) ([]Volume, string, error) {
	t, err := e.scan(ctx, "")
	if err != nil {
		return nil, "", err
	}
	var vols []Volume
	for r := range t.Volumes() {
		if r.ServedVolume() {
			vols = append(vols, volumeView(r))
		}
	}
	return page(vols, func(v Volume) string { return v.ID }, volumeToken, maxEntries, startingToken)
}

// CreateSnapshot cuts "<volume>@<name>". The same name on the same volume is
// idempotent; on another volume it is AlreadyExists (CSI).
func (e *Endpoint) CreateSnapshot(ctx context.Context, name, sourceVolumeID string) (Snapshot, error) {
	release, err := e.lock(ctx, "snapname:"+name, "vol:"+sourceVolumeID, "snap:"+sourceVolumeID+"@"+name)
	if err != nil {
		return Snapshot{}, err
	}
	defer release()
	// names are global, so the whole parent is the input
	t, err := e.scan(ctx, "")
	if err != nil {
		return Snapshot{}, err
	}
	plan, id, err := e.plan.PlanCreateSnapshot(t, name, sourceVolumeID)
	if err != nil {
		return Snapshot{}, err
	}
	if err := e.apply(ctx, plan); err != nil {
		return Snapshot{}, err
	}
	if len(plan) > 0 {
		if t, err = e.scan(ctx, sourceVolumeID); err != nil {
			return Snapshot{}, err
		}
	}
	row, ok := t.Get(id)
	if !ok {
		return Snapshot{}, errorf(Internal, "snapshot %s vanished after creation", id)
	}
	return snapshotView(t, row), nil
}

// ListSnapshots filters by snapshot id and/or source volume and pages.
// Snapshots pending deferred destruction are already deleted as far as CSI
// is concerned.
func (e *Endpoint) ListSnapshots(ctx context.Context, snapshotID, sourceVolumeID string, maxEntries int, startingToken string) ([]Snapshot, string, error) {
	t, err := e.scan(ctx, "")
	if err != nil {
		return nil, "", err
	}
	var snaps []Snapshot
	for r := range t.All() {
		if !r.OwnedSnapshot() || r.Deferred {
			continue
		}
		s := snapshotView(t, r)
		if (snapshotID != "" && s.ID != snapshotID) || (sourceVolumeID != "" && s.SourceVolumeID != sourceVolumeID) {
			continue
		}
		snaps = append(snaps, s)
	}
	return page(snaps, func(s Snapshot) string { return s.ID }, snapshotToken, maxEntries, startingToken)
}

// DeleteSnapshot removes a snapshot. Missing or never-issued ids succeed. A
// snapshot with clones is destroyed deferred (lzc_destroy_snaps defer): gone
// from listings now and from disk with its last clone, instead of leaking.
func (e *Endpoint) DeleteSnapshot(ctx context.Context, snapshotID string) error {
	vol, _, err := parseSnapshotID(snapshotID)
	if err != nil {
		return nil
	}
	release, err := e.lock(ctx, "snap:"+snapshotID, "vol:"+vol)
	if err != nil {
		return err
	}
	defer release()
	t, err := e.scan(ctx, vol)
	if err != nil {
		return err
	}
	plan, err := e.plan.PlanDeleteSnapshot(t, snapshotID)
	if err != nil {
		return err
	}
	return e.apply(ctx, plan)
}

// GetCapacity reports space available to new volumes under the parent.
func (e *Endpoint) GetCapacity(ctx context.Context) (uint64, error) {
	d, err := e.zfs.Get(ctx, e.cfg.Parent, "available")
	if zfs.IsNotFound(err) {
		return 0, parentUnavailable(e.cfg.Parent, err)
	}
	if err != nil {
		return 0, zfsErr(err, "inspect parent")
	}
	return d.Props["available"].Uint()
}

// ---- file export (NFS) -------------------------------------------------------

// FileExportEnabled reports whether this endpoint offers NFS export.
func (e *Endpoint) FileExportEnabled() bool { return e.cfg.FileExport }

// NFSServers are the NFS server addresses this endpoint advertises
// (Config.NFSServers), in order.
func (e *Endpoint) NFSServers() []string { return slices.Clone(e.cfg.NFSServers) }

func (e *Endpoint) readEtab(ctx context.Context) (Etab, error) {
	b, err := e.cfg.Etab.ReadEtab(ctx)
	if err != nil {
		return nil, &Error{Code: Unavailable, Msg: "read NFS export table", Err: err}
	}
	return ParseEtab(b), nil
}

// exportTable reads the export table with volume volID's entries keyed by
// its mountpoint property. exportfs stores every export under its real
// path (nfs-utils nfsd_realpath), so a mountpoint that crosses a symlink
// would otherwise never match: Publish Unavailable forever, and Unpublish
// blind to an export that is still served.
func (e *Endpoint) exportTable(ctx context.Context, t Table, volID string) (Etab, error) {
	live, err := e.readEtab(ctx)
	if err != nil {
		return nil, err
	}
	if row, ok := t.Get(volID); ok && row.Mount != "" {
		live = live.at(row.Mount, realPath(row.Mount))
	}
	return live, nil
}

// PublishFileExport exports a filesystem volume over NFS to one client (an
// IPv4 address or IPv4 CIDR no broader than Config.NFSMinPrefix; no
// hostnames, see NFSClient), read-write or read-only, and returns the
// export path on the storage host. Repeating it is a no-op; the same client
// in the other mode is AlreadyExists, an overlapping network is
// FailedPrecondition. A missing volume or an id this endpoint never issues
// is NotFound. Zvols, volumes zfs would not mount (canmount off/noauto,
// mountpoint none/legacy) and a sharenfs that is not the endpoint's (set
// locally outside its grammar, or received) are FailedPrecondition. Success
// means the kernel serves the export and nfsd runs: `zfs set sharenfs`
// cannot report a failed exportfs, so the export table is read back and a
// mismatch is Unavailable (the retry re-shares).
func (e *Endpoint) PublishFileExport(ctx context.Context, volID, host string, readOnly bool) (string, error) {
	if !e.cfg.FileExport {
		return "", errorf(Unimplemented, "file export needs the endpoint to run as root")
	}
	if _, err := issuedVolumeID(volID); err != nil {
		return "", err
	}
	c, err := ParseClient(host, readOnly)
	if err != nil {
		return "", err
	}
	if err := checkFloor(c, e.cfg.NFSMinPrefix); err != nil {
		return "", err
	}
	release, err := e.lock(ctx, "vol:"+volID)
	if err != nil {
		return "", err
	}
	defer release()
	t, err := e.scan(ctx, volID)
	if err != nil {
		return "", err
	}
	if row, ok := t.Get(volID); ok && row.OwnedVolume() && !row.ServedVolume() {
		// a provision still in flight is not a volume yet
		return "", errorf(NotFound, "volume %s not found", volID)
	}
	live, err := e.exportTable(ctx, t, volID)
	if err != nil {
		return "", err
	}
	plan, row, err := e.plan.PlanPublishExport(t, live, volID, c)
	if errors.Is(err, ErrServedBeyond) && row.Mounted {
		// a stale table (an earlier exportfs failed) goes with a re-share
		// of the unchanged value; what stays is someone else's, and the
		// refusal then leaves every property as it was
		if err := e.apply(ctx, reshare(row, clientsOf(row))); err != nil {
			return "", err
		}
		if live, err = e.exportTable(ctx, t, volID); err != nil {
			return "", err
		}
		plan, row, err = e.plan.PlanPublishExport(t, live, volID, c)
	}
	if err != nil {
		return "", err
	}
	if len(plan) > 0 {
		if err := e.apply(ctx, plan); err != nil {
			return "", err
		}
		if live, err = e.exportTable(ctx, t, volID); err != nil {
			return "", err
		}
		if !live.serves(row.Mount, c) {
			return "", errorf(Unavailable, "sharenfs on %s is set, but the NFS export table does not serve %s to %s %s: "+
				"zfs does not report exportfs failures; check exportfs and the NFS server on the storage host", row.Name, row.Mount, c.Host, c.mode())
		}
	}
	// served beyond the request after the change: what the planner could
	// not see (an inherited value's lines it replaced, an ancestor reached
	// only through the mountpoint's real path, a concurrent edit). The
	// change is undone, so a refused Publish leaves sharenfs as it was.
	if x := live.exposure(realPath(row.Mount), publishWant(row, c)); len(x) > 0 {
		refused := errorf(FailedPrecondition, "volume %s is served beyond its client list by the NFS export table: %s; "+
			"remove these exports (an ancestor dataset's sharenfs with crossmnt, e.g. \"on\", or /etc/exports) first", row.ID, strings.Join(x, "; "))
		if len(plan) > 0 {
			if rerr := e.restoreShareNFS(ctx, row); rerr != nil {
				return "", errors.Join(refused, fmt.Errorf("restoring sharenfs: %w", rerr))
			}
		}
		return "", refused
	}
	// etab is no proof of a server: exportfs -ra rewrites it with nfsd
	// stopped
	if err := e.nfsdRunning(ctx); err != nil {
		return "", err
	}
	return row.Mount, nil
}

// restoreShareNFS puts back the sharenfs a refused Publish changed: the
// local value it had (always the endpoint's own: Publish refuses anything
// else before writing), or inheritance. It runs even when the caller's
// context ended, bounded by its own timeout, still under the volume lock.
func (e *Endpoint) restoreShareNFS(ctx context.Context, before Row) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
	defer cancel()
	if before.ShareNFSLocal {
		return e.zfs.Set(ctx, before.Name, map[string]string{"sharenfs": before.ShareNFS})
	}
	return e.zfs.Inherit(ctx, before.Name, "sharenfs")
}

// nfsdRunning is Unavailable unless nfsd threads run.
func (e *Endpoint) nfsdRunning(ctx context.Context) error {
	n, err := e.cfg.NFSD.NFSDThreads(ctx)
	if err != nil {
		return &Error{Code: Unavailable, Msg: "read the nfsd thread count", Err: err}
	}
	if n <= 0 {
		return errorf(Unavailable, "the NFS server is not running on the storage host (no nfsd threads); the export is stored and is served once it starts")
	}
	return nil
}

// UnpublishFileExport revokes one client's NFS access to a volume. A
// missing volume, an id this endpoint never issues or a client that is not
// exported is OK. Any IPv4 address or CIDR is taken, whatever the floor.
// An unmounted volume stays unmounted. Success means the kernel no longer
// serves the export to that client.
func (e *Endpoint) UnpublishFileExport(ctx context.Context, volID, host string) error {
	if !e.cfg.FileExport {
		return errorf(Unimplemented, "file export needs the endpoint to run as root")
	}
	c, err := ParseClient(host, false)
	if err != nil {
		return err
	}
	if _, err := issuedVolumeID(volID); err != nil {
		if CodeOf(err) == NotFound {
			return nil // never issued, so never exported
		}
		return err
	}
	h := c.Host
	release, err := e.lock(ctx, "vol:"+volID)
	if err != nil {
		return err
	}
	defer release()
	t, err := e.scan(ctx, volID)
	if err != nil {
		return err
	}
	live, err := e.exportTable(ctx, t, volID)
	if err != nil {
		return err
	}
	plan, row, err := e.plan.PlanUnpublishExport(t, live, volID, h)
	if err != nil || len(plan) == 0 {
		return err
	}
	apply := e.apply
	if !row.Mounted {
		apply = e.applyNoMount
	}
	if err := apply(ctx, plan); err != nil {
		return err
	}
	if live, err = e.exportTable(ctx, t, volID); err != nil {
		return err
	}
	if _, ok := live.Lookup(row.Mount, h); ok {
		return errorf(Unavailable, "sharenfs on %s is updated, but the NFS export table still serves %s to %s: "+
			"zfs does not report exportfs failures; check exportfs and the NFS server on the storage host", row.Name, row.Mount, h)
	}
	if x := live.covers(realPath(row.Mount), h, unpublishWant(row, h)); len(x) > 0 {
		return errorf(FailedPrecondition, "sharenfs on %s is updated, but %s is still served to %s by: %s; "+
			"remove these exports (another exports file, or an ancestor dataset's sharenfs with crossmnt)", row.Name, row.Mount, h, strings.Join(x, "; "))
	}
	return nil
}

// applyNoMount applies an export plan to an unmounted filesystem with
// `zfs set -u`. A plain set of sharenfs runs the changelist postfix, which
// mounts a canmount=on filesystem and shares it: an Unpublish would undo
// an operator's deliberate unmount and start serving the volume. -u still
// changes the property, so a later `zfs mount` shares the revoked list,
// never the old one. (Refusing instead would block every detach for as
// long as the volume stays unmounted; skipping the sharenfs change would
// re-grant the revoked client on the next mount.)
func (e *Endpoint) applyNoMount(ctx context.Context, plan Plan) error {
	for i, op := range plan {
		if op.Kind != OpSet {
			return &Error{Code: Internal, Msg: fmt.Sprintf("op %d/%d: %v on %s is not a property change", i+1, len(plan), op.Kind, op.Target)}
		}
		if err := e.zfs.SetNoMount(ctx, op.Target, op.Props); err != nil {
			return opErr(err, i, plan)
		}
	}
	return nil
}
