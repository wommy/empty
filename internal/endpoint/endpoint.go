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
		n.snapshotName(), n.snapshotSource(), n.nfsClients()}
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

// nfsClients is the endpoint's own property (not the baseline's): the
// desired NFS client list that sharenfs is computed from.
func (n props) nfsClients() string { return n.p + ":nfs_clients" }

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
	FileExport bool
	// Etab reads the kernel export table to verify exports. Default
	// /var/lib/nfs/etab.
	Etab EtabReader
	// DestroyForeignSnapshots lets DeleteVolume (and reconcile) destroy a
	// volume together with snapshots the endpoint did not cut (no locally
	// set csi_snapshot_name: an administrator's, a backup tool's, a
	// received one). Default false: such a volume is refused with
	// FailedPrecondition naming them (ADR 0003: never touch what you
	// don't own).
	DestroyForeignSnapshots bool
}

// Endpoint is the volume service.
type Endpoint struct {
	zfs   *zfs.Client
	cfg   Config
	names props
	plan  Planner
	locks lock.Keyed
}

// New returns an Endpoint.
func New(z *zfs.Client, cfg Config) (*Endpoint, error) {
	if !zfs.ValidDataset(cfg.Parent) {
		return nil, fmt.Errorf("invalid parent dataset %q", cfg.Parent)
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
	names := props{cfg.PropertyPrefix}
	return &Endpoint{
		zfs:   z,
		cfg:   cfg,
		names: names,
		plan:  Planner{Parent: cfg.Parent, Names: names, VolBlockSize: cfg.VolBlockSize, DestroyForeignSnapshots: cfg.DestroyForeignSnapshots},
	}, nil
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
	} else {
		v.Mountpoint = r.Mount
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

	t, err := e.scanFor(ctx, id, r.SourceVolumeID, r.SourceSnapshotID)
	if err != nil {
		return Volume{}, err
	}
	plan, err := e.plan.PlanCreateVolume(t, r)
	if err != nil {
		return Volume{}, err
	}
	if err := e.apply(ctx, plan); err != nil {
		return Volume{}, err
	}
	return e.readVolume(ctx, id)
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
	if !ok || !row.OwnedVolume() {
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
	release, err := e.lock(ctx, "vol:"+volID)
	if err != nil {
		return err
	}
	defer release()
	// clones of this volume's snapshots live elsewhere under the parent
	t, err := e.scan(ctx, "")
	if err != nil {
		return err
	}
	plan, err := e.plan.PlanDeleteVolume(t, volID)
	if err != nil {
		return err
	}
	return e.apply(ctx, plan)
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
	plan, _, err := e.plan.PlanExpandVolume(t, volID, capacity, limit)
	if err != nil {
		return Volume{}, err
	}
	if err := e.apply(ctx, plan); err != nil {
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
	if !ok || !row.OwnedVolume() {
		return Volume{}, errorf(NotFound, "volume %s not found", volID)
	}
	return volumeView(row), nil
}

// page returns items after startingToken (an item id), at most max (0 = all),
// and the next token. An unknown token is Aborted (CSI).
func page[T any](items []T, id func(T) string, max int, startingToken string) ([]T, string, error) {
	start := 0
	if startingToken != "" {
		start = -1
		for i, it := range items {
			if id(it) == startingToken {
				start = i + 1
				break
			}
		}
		if start < 0 {
			return nil, "", errorf(Aborted, "invalid starting token %q", startingToken)
		}
	}
	items = items[start:]
	if max > 0 && len(items) > max {
		return items[:max], id(items[max-1]), nil
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
		if r.OwnedVolume() && r.Committed {
			vols = append(vols, volumeView(r))
		}
	}
	return page(vols, func(v Volume) string { return v.ID }, maxEntries, startingToken)
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
	return page(snaps, func(s Snapshot) string { return s.ID }, maxEntries, startingToken)
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

func (e *Endpoint) readEtab(ctx context.Context) (Etab, error) {
	b, err := e.cfg.Etab.ReadEtab(ctx)
	if err != nil {
		return nil, &Error{Code: Unavailable, Msg: "read NFS export table", Err: err}
	}
	return ParseEtab(b), nil
}

// PublishFileExport exports a filesystem volume over NFS to one client
// (hostname, IPv4 address or IPv4 CIDR), read-write or read-only, and
// returns the export path on the storage host. Repeating it is a no-op; the
// same client in the other mode is AlreadyExists. Unmounted volumes and
// zvols are FailedPrecondition. Success means the kernel serves the export:
// `zfs set sharenfs` cannot report a failed exportfs, so the export table is
// read back and a mismatch is Unavailable (the retry re-shares).
func (e *Endpoint) PublishFileExport(ctx context.Context, volID, host string, readOnly bool) (string, error) {
	if !e.cfg.FileExport {
		return "", errorf(Unimplemented, "file export needs the endpoint to run as root")
	}
	if _, err := volumeID(volID); err != nil {
		return "", err
	}
	c, err := ParseClient(host, readOnly)
	if err != nil {
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
	live, err := e.readEtab(ctx)
	if err != nil {
		return "", err
	}
	plan, row, err := e.plan.PlanPublishExport(t, live, volID, c)
	if err != nil {
		return "", err
	}
	if len(plan) == 0 {
		return row.Mount, nil
	}
	if err := e.apply(ctx, plan); err != nil {
		return "", err
	}
	if live, err = e.readEtab(ctx); err != nil {
		return "", err
	}
	if got, ok := live.Lookup(row.Mount, c.Host); !ok || got.ReadOnly != c.ReadOnly {
		return "", errorf(Unavailable, "sharenfs on %s is set, but the NFS export table does not serve %s to %s %s: "+
			"zfs does not report exportfs failures; check that the NFS server is running", row.Name, row.Mount, c.Host, c.mode())
	}
	return row.Mount, nil
}

// UnpublishFileExport revokes one client's NFS access to a volume. A
// missing volume or a client that is not exported is OK. Success means the
// kernel no longer serves the export to that client.
func (e *Endpoint) UnpublishFileExport(ctx context.Context, volID, host string) error {
	if !e.cfg.FileExport {
		return errorf(Unimplemented, "file export needs the endpoint to run as root")
	}
	if _, err := volumeID(volID); err != nil {
		return err
	}
	h, err := normalizeHost(host)
	if err != nil {
		return err
	}
	release, err := e.lock(ctx, "vol:"+volID)
	if err != nil {
		return err
	}
	defer release()
	t, err := e.scan(ctx, volID)
	if err != nil {
		return err
	}
	live, err := e.readEtab(ctx)
	if err != nil {
		return err
	}
	plan, row, err := e.plan.PlanUnpublishExport(t, live, volID, h)
	if err != nil || len(plan) == 0 {
		return err
	}
	if err := e.apply(ctx, plan); err != nil {
		return err
	}
	if live, err = e.readEtab(ctx); err != nil {
		return err
	}
	if _, ok := live.Lookup(row.Mount, h); ok {
		return errorf(Unavailable, "sharenfs on %s is updated, but the NFS export table still serves %s to %s: "+
			"zfs does not report exportfs failures; check that the NFS server is running", row.Name, row.Mount, h)
	}
	return nil
}
