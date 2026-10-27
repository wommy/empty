// Package endpoint implements idempotent volume and snapshot lifecycle on
// ZFS. It owns what CSI front ends would otherwise each re-implement:
// name-keyed idempotency, locking, metadata, and the CSI error contract.
//
// The design is data-oriented. One scan reads the relevant subtree into a
// Table of typed rows (table.go); pure planners turn (Table, request) into a
// Plan of mutations or an error (plan.go); apply executes it (apply.go).
// Idempotency is "the plan is empty"; recovery is "re-scan and re-plan".
//
// State lives in ZFS user properties using democratic-csi's names (the
// behavioral baseline), so volumes it provisioned are adopted as-is.
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

// Property names; the prefix defaults to the baseline's "democratic-csi".
type props struct{ p string }

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
	// PropertyPrefix for user properties. Default "democratic-csi".
	PropertyPrefix string
	// VolBlockSize used to pre-round zvol sizes for limit checks. Default
	// 16 KiB. zfs may round further; reported sizes are always re-read.
	VolBlockSize uint64
	// LockWait bounds how long an operation waits for a busy resource
	// before returning Aborted. Default 30s, and always inside the caller's
	// deadline.
	LockWait time.Duration
	// FileExport enables NFS export (Publish/UnpublishFileExport). Exports
	// need root: libzfs writes /etc/exports.d and runs exportfs as the
	// caller, and only root can mount. The caller decides (e.g. from the
	// effective uid); unset, those calls are Unimplemented.
	FileExport bool
	// Etab reads the kernel export table to verify exports. Default
	// /var/lib/nfs/etab.
	Etab EtabReader
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
		cfg.PropertyPrefix = "democratic-csi"
	}
	if cfg.VolBlockSize == 0 {
		cfg.VolBlockSize = 16 * 1024
	}
	if cfg.LockWait == 0 {
		cfg.LockWait = 30 * time.Second
	}
	if cfg.Etab == nil {
		cfg.Etab = DefaultEtab
	}
	names := props{cfg.PropertyPrefix}
	return &Endpoint{
		zfs:   z,
		cfg:   cfg,
		names: names,
		plan:  Planner{Parent: cfg.Parent, Names: names, VolBlockSize: cfg.VolBlockSize},
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
	case errors.Is(err, zfs.ErrHasClones), errors.Is(err, zfs.ErrHasChildren), errors.Is(err, zfs.ErrBusy):
		code = FailedPrecondition
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

// userValue reads a user property; "-" means unset.
func userValue(p zfs.Prop) string {
	if p.Value == "-" {
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
	id, err := e.plan.validateCreate(r)
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
	}
	return NewTable(rows), nil
}

func (e *Endpoint) readVolume(ctx context.Context, id string) (Volume, error) {
	t, err := e.scan(ctx, id)
	if err != nil {
		return Volume{}, err
	}
	row, ok := t.Get(id)
	if !ok {
		return Volume{}, errorf(Internal, "volume %s vanished after provisioning", id)
	}
	return volumeView(row), nil
}

// DeleteVolume destroys a volume. Missing volumes succeed (CSI). Like the
// baseline, volumes with CSI snapshots or dependent clones fail with
// FailedPrecondition and are left untouched.
func (e *Endpoint) DeleteVolume(ctx context.Context, volID string) error {
	if _, err := volumeID(volID); err != nil {
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
	if _, err := volumeID(volID); err != nil {
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
	return e.readVolume(ctx, volID)
}

// GetVolume returns a managed volume.
func (e *Endpoint) GetVolume(ctx context.Context, volID string) (Volume, error) {
	if _, err := volumeID(volID); err != nil {
		return Volume{}, err
	}
	t, err := e.scan(ctx, volID)
	if err != nil {
		return Volume{}, err
	}
	row, ok := t.Get(volID)
	if !ok || !row.Managed {
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
		if r.Managed && r.Committed {
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
		if r.Type != zfs.TypeSnapshot || !r.Managed || r.Deferred {
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
