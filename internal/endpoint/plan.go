package endpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"unicode"

	"github.com/wommy/empty/internal/zfs"
)

// OpKind is one zfs mutation.
type OpKind uint8

const (
	OpCreateFS OpKind = iota + 1
	OpCreateVol
	OpSnapshot
	OpClone
	OpSet
	OpDestroy
)

// Op is a planned mutation, as plain data. Each op states how a replay
// tolerates finding its work already done, so re-applying a partially
// applied plan converges instead of failing.
type Op struct {
	Kind      OpKind
	Target    string            // full zfs name
	Source    string            // clone origin snapshot
	Size      uint64            // volsize for OpCreateVol
	Sparse    bool              // OpCreateVol
	Recursive bool              // OpDestroy
	Defer     bool              // OpDestroy of a snapshot
	Props     map[string]string // create/clone/snapshot -o, or OpSet values
	OkExists  bool              // create/snapshot/clone: already exists is done
	OkMissing bool              // destroy: already gone is done
}

// Plan is the ordered ops of one request.
type Plan []Op

// Planner holds what planning needs besides the table: pure configuration.
type Planner struct {
	Parent       string
	Names        props
	VolBlockSize uint64
}

func (p Planner) full(id string) string { return p.Parent + "/" + id }

func u64(n uint64) string { return strconv.FormatUint(n, 10) }

// want is the capacity a request resolves to (rounded for zvols, to the
// requested volblocksize when props, the validated properties, carry one).
func (p Planner) want(r CreateVolumeRequest, props map[string]string) uint64 {
	if r.Kind == Block {
		bs, _ := strconv.ParseUint(props[volblocksizeProp], 10, 64)
		return p.roundTo(r.CapacityBytes, bs)
	}
	if r.CapacityBytes == 0 && r.LimitBytes > 0 {
		// "at most limit": unlimited would violate it
		return r.LimitBytes
	}
	return r.CapacityBytes
}

// roundTo rounds n up to a multiple of bs (a volume's own volblocksize,
// falling back to the configured default).
func (p Planner) roundTo(n, bs uint64) uint64 {
	if bs == 0 {
		bs = p.VolBlockSize
	}
	if r := n % bs; r != 0 {
		n += bs - r
	}
	return n
}

// validateCreate checks a request on its own, before looking at state, and
// returns the volume id and the canonical properties.
func (p Planner) validateCreate(r CreateVolumeRequest) (string, map[string]string, error) {
	id, err := volumeID(r.Name)
	if err != nil {
		return "", nil, err
	}
	props, err := ValidateProperties(r.Kind, r.Properties)
	if err != nil {
		return "", nil, err
	}
	if r.SourceSnapshotID != "" && r.SourceVolumeID != "" {
		return "", nil, errorf(InvalidArgument, "at most one content source")
	}
	if r.Kind == Block && r.CapacityBytes == 0 && r.SourceSnapshotID == "" && r.SourceVolumeID == "" {
		return "", nil, errorf(InvalidArgument, "block volume needs a capacity")
	}
	if r.Kind == Filesystem && r.Reserve && p.want(r, props) == 0 && r.SourceSnapshotID == "" && r.SourceVolumeID == "" {
		return "", nil, errorf(InvalidArgument, "reserve needs a capacity or limit")
	}
	if r.Kind == Block && r.Reserve {
		return "", nil, errorf(InvalidArgument, "reserve applies to filesystems; use sparse for zvols")
	}
	if r.Kind == Filesystem && r.Sparse {
		return "", nil, errorf(InvalidArgument, "sparse applies to zvols; filesystems are thin unless reserved")
	}
	if r.LimitBytes > 0 && p.want(r, props) > r.LimitBytes {
		return "", nil, errorf(OutOfRange, "capacity %d (rounded) exceeds limit %d", p.want(r, props), r.LimitBytes)
	}
	return id, props, nil
}

// PlanCreateVolume decides how to make the table satisfy r.
//
//   - no row: create (or clone) with ownership set atomically, then commit
//   - uncommitted row we own with the same source: converge it (a crash or
//     failure mid-provision), then commit
//   - committed row: must be compatible (CSI idempotency) and the plan is
//     empty, or AlreadyExists
func (p Planner) PlanCreateVolume(t Table, r CreateVolumeRequest) (Plan, error) {
	id, props, err := p.validateCreate(r)
	if err != nil {
		return nil, err
	}
	want := p.want(r, props)
	srcType, srcID := sourceOf(r)
	commit := Op{Kind: OpSet, Target: p.full(id), Props: map[string]string{p.Names.success(): "true"}}

	if row, ok := t.Get(id); ok {
		if !row.Managed {
			return nil, errorf(AlreadyExists, "dataset %s exists and is not managed", row.Name)
		}
		if row.VolumeName != "" && row.VolumeName != r.Name {
			return nil, errorf(AlreadyExists, "dataset %s belongs to volume %q", row.Name, row.VolumeName)
		}
		if row.Kind() != r.Kind || row.SourceType != srcType || row.SourceID != srcID {
			return nil, errorf(AlreadyExists, "volume %s exists as %s from %q %q", id, row.Kind(), row.SourceType, row.SourceID)
		}
		if !row.Committed {
			// volblocksize is create-only: a row without it cannot converge
			if bs, ok := props[volblocksizeProp]; ok && u64(row.BlockSz) != bs {
				return nil, errorf(AlreadyExists, "volume %s exists with volblocksize %d, requested %s", id, row.BlockSz, bs)
			}
			plan := p.sizeOps(id, row, r, want)
			if set := propSet(row, props); len(set) > 0 {
				plan = append(plan, Op{Kind: OpSet, Target: p.full(id), Props: set})
			}
			return append(plan, commit), nil
		}
		if err := p.compatible(row, r, want); err != nil {
			return nil, err
		}
		if k, bad := propsMismatch(row, props); bad {
			return nil, errorf(AlreadyExists, "volume %s exists without %s=%s set locally", id, k, props[k])
		}
		return nil, nil
	}

	// validated properties first: none of them can name an ownership
	// marker (user properties are refused) or a share property
	own := make(map[string]string, len(props)+6)
	for k, v := range props {
		own[k] = v
	}
	own[p.Names.managed()], own[p.Names.volumeName()] = "true", r.Name
	if r.Kind == Filesystem {
		// sharenfs/sharesmb inherit: a parent shared to "*" would export
		// every new volume (restores included) to everyone. Exports are
		// opt-in, set locally at creation.
		own["sharenfs"], own["sharesmb"] = "off", "off"
	}
	var plan Plan
	switch {
	case r.SourceSnapshotID != "":
		vol, _, err := parseSnapshotID(r.SourceSnapshotID)
		if err != nil {
			return nil, err
		}
		snap, ok := t.Get(r.SourceSnapshotID)
		if !ok || snap.Type != zfs.TypeSnapshot || snap.Deferred {
			return nil, errorf(NotFound, "source snapshot %s not found", r.SourceSnapshotID)
		}
		if src, ok := t.Get(vol); ok && src.Kind() != r.Kind {
			return nil, errorf(InvalidArgument, "source is %s, requested %s", src.Kind(), r.Kind)
		}
		src, _ := t.Get(vol)
		if err := cloneBlockSize(own, src); err != nil {
			return nil, err
		}
		own[p.Names.sourceType()], own[p.Names.sourceID()] = srcType, srcID
		plan = Plan{{Kind: OpClone, Source: snap.Name, Target: p.full(id), Props: own, OkExists: true}}
		plan = append(plan, p.sizeOps(id, Row{Type: kindType(r.Kind), Volsize: snap.Volsize}, r, want)...)
		if r.Kind == Filesystem {
			// filesystem clones start without refquota; size from the source
			if src, ok := t.Get(vol); ok && want == 0 && src.Refquota > 0 {
				plan = append(plan, Op{Kind: OpSet, Target: p.full(id), Props: map[string]string{"refquota": u64(src.Refquota)}})
			}
		}
	case r.SourceVolumeID != "":
		srcID, err := volumeID(r.SourceVolumeID)
		if err != nil {
			return nil, err
		}
		src, ok := t.Get(srcID)
		if !ok || src.Type == zfs.TypeSnapshot {
			return nil, errorf(NotFound, "source volume %s not found", srcID)
		}
		if src.Kind() != r.Kind {
			return nil, errorf(InvalidArgument, "source is %s, requested %s", src.Kind(), r.Kind)
		}
		if err := cloneBlockSize(own, src); err != nil {
			return nil, err
		}
		internal := src.Name + "@" + cloneSourceSnapshotPrefix + id
		if !zfs.ValidSnapshot(internal) {
			return nil, errorf(InvalidArgument, "names too long for clone source snapshot %q", internal)
		}
		own[p.Names.sourceType()], own[p.Names.sourceID()] = srcType, r.SourceVolumeID
		plan = Plan{
			{Kind: OpSnapshot, Target: internal, OkExists: true},
			{Kind: OpClone, Source: internal, Target: p.full(id), Props: own, OkExists: true},
		}
		base := src
		if r.Kind == Filesystem && want == 0 {
			want = src.Refquota
		}
		// a clone starts with neither the source's refquota nor its
		// refreservation; size from what the clone really has
		base.Refquota, base.Refres = 0, 0
		plan = append(plan, p.sizeOps(id, base, r, want)...)
	case r.Kind == Block:
		// thick zvols: zfs create -V sets refreservation to zvol_auto_resv
		// (volsize plus metadata) and keeps it in step with volsize; setting
		// it by hand would under-reserve and stop the tracking
		plan = Plan{{Kind: OpCreateVol, Target: p.full(id), Size: want, Sparse: r.Sparse, Props: own}}
	default:
		if want > 0 {
			own["refquota"] = u64(want)
			if r.Reserve {
				own["refreservation"] = u64(want)
			}
		}
		plan = Plan{{Kind: OpCreateFS, Target: p.full(id), Props: own}}
	}
	return append(plan, commit), nil
}

// cloneBlockSize enforces that a clone's requested volblocksize is its
// source's, and drops it from the clone's -o: a clone takes the origin's
// block size, and libzfs refuses volblocksize there as readonly
// (zfs_valid_proplist with a non-NULL zhp).
func cloneBlockSize(own map[string]string, src Row) error {
	bs, ok := own[volblocksizeProp]
	if !ok {
		return nil
	}
	if src.Type != zfs.TypeVolume || u64(src.BlockSz) != bs {
		return errorf(InvalidArgument, "volblocksize %s: a clone keeps its source's block size (%d)", bs, src.BlockSz)
	}
	delete(own, volblocksizeProp)
	return nil
}

// sizeOps converges an owned-but-uncommitted (or freshly cloned) volume to
// the requested size and reservation. It never shrinks.
func (p Planner) sizeOps(id string, row Row, r CreateVolumeRequest, want uint64) Plan {
	set := map[string]string{}
	if r.Kind == Block {
		if want > row.Volsize {
			set["volsize"] = u64(want)
		}
		if !r.Sparse && row.Refres == 0 {
			set["refreservation"] = "auto"
		}
	} else {
		if want > 0 && (row.Refquota == 0 || want > row.Refquota) {
			set["refquota"] = u64(want)
		}
		if r.Reserve && row.Refres < want {
			set["refreservation"] = u64(want)
		}
	}
	if len(set) == 0 {
		return nil
	}
	return Plan{{Kind: OpSet, Target: p.full(id), Props: set}}
}

// compatible applies CSI's CreateVolume idempotency rule to a committed row.
func (p Planner) compatible(row Row, r CreateVolumeRequest, want uint64) error {
	id, have := row.ID, row.Capacity()
	if want > 0 && (have == 0 || have < want) {
		return errorf(AlreadyExists, "volume %s exists with capacity %d < %d", id, have, want)
	}
	if r.LimitBytes > 0 && (have == 0 || have > r.LimitBytes) {
		return errorf(AlreadyExists, "volume %s exists with capacity %d > limit %d", id, have, r.LimitBytes)
	}
	if row.Kind() == Block && r.Sparse == (row.Refres > 0) {
		return errorf(AlreadyExists, "volume %s exists with sparse=%v", id, row.Refres == 0)
	}
	if row.Kind() == Filesystem && r.Reserve != (row.Refres > 0) {
		return errorf(AlreadyExists, "volume %s exists with reserve=%v", id, row.Refres > 0)
	}
	return nil
}

// PlanDeleteVolume: missing is done; unmanaged, snapshotted (like the
// baseline: CSI snapshots must outlive the call or it fails unchanged) or
// cloned volumes are refused; an internal clone-source origin is released.
func (p Planner) PlanDeleteVolume(t Table, volID string) (Plan, error) {
	id, err := volumeID(volID)
	if err != nil {
		return nil, err
	}
	row, ok := t.Get(id)
	if !ok {
		return nil, nil
	}
	if !row.Managed {
		return nil, errorf(FailedPrecondition, "dataset %s is not managed", row.Name)
	}
	for s := range t.SnapshotsOf(id) {
		if s.SnapshotName != "" && !s.Deferred {
			return nil, errorf(FailedPrecondition, "volume %s has snapshot %s", id, s.ID)
		}
		for c := range t.ClonesOf(s.Name) {
			if !strings.HasPrefix(c.Name, row.Name+"/") {
				return nil, errorf(FailedPrecondition, "volume %s has dependent clone %s", id, c.ID)
			}
		}
	}
	plan := Plan{{Kind: OpDestroy, Target: row.Name, Recursive: true, OkMissing: true}}
	if strings.Contains(row.Origin, "@"+cloneSourceSnapshotPrefix) {
		plan = append(plan, Op{Kind: OpDestroy, Target: row.Origin, Defer: true, OkMissing: true})
	}
	return plan, nil
}

// PlanExpandVolume grows to at least capacity; shrinking is a no-op.
func (p Planner) PlanExpandVolume(t Table, volID string, capacity, limit uint64) (Plan, Row, error) {
	id, err := volumeID(volID)
	if err != nil {
		return nil, Row{}, err
	}
	row, ok := t.Get(id)
	if !ok || !row.Managed {
		return nil, Row{}, errorf(NotFound, "volume %s not found", id)
	}
	want := capacity
	if row.Kind() == Block {
		want = p.roundTo(capacity, row.BlockSz)
	}
	if limit > 0 && want > limit {
		return nil, Row{}, errorf(OutOfRange, "capacity %d exceeds limit %d", want, limit)
	}
	if row.Kind() == Filesystem && row.Refquota == 0 {
		// unlimited already satisfies any capacity; a quota would shrink it
		return nil, row, nil
	}
	if want <= row.Capacity() {
		return nil, row, nil
	}
	set := map[string]string{}
	if row.Kind() == Block {
		// refreservation follows volsize by itself when it was auto
		set["volsize"] = u64(want)
	} else {
		set["refquota"] = u64(want)
		if row.Refres > 0 {
			set["refreservation"] = u64(want)
		}
	}
	return Plan{{Kind: OpSet, Target: row.Name, Props: set}}, row, nil
}

// PlanCreateSnapshot: CSI snapshot names are global; the same name on the
// same volume is done, on another volume AlreadyExists.
func (p Planner) PlanCreateSnapshot(t Table, name, volID string) (Plan, string, error) {
	vol, err := volumeID(volID)
	if err != nil {
		return nil, "", err
	}
	if name == "" || len(name) > 128 || strings.ContainsFunc(name, unicode.IsControl) {
		return nil, "", errorf(InvalidArgument, "invalid snapshot name %q", name)
	}
	if strings.HasPrefix(name, cloneSourceSnapshotPrefix) {
		return nil, "", errorf(InvalidArgument, "snapshot name prefix %q is reserved", cloneSourceSnapshotPrefix)
	}
	id := vol + "@" + snapComponent(vol, name)
	if len(id) > 128 {
		// ids are "<volume>@<component>"; a volume id over 91 bytes cannot
		// carry even a hashed component within CSI's limit
		return nil, "", errorf(InvalidArgument, "volume id %q too long to address snapshots within 128 bytes", vol)
	}
	for r := range t.All() {
		if r.Type == zfs.TypeSnapshot && r.SnapshotName == name && !r.Deferred {
			if r.ID == id {
				return nil, id, nil
			}
			return nil, "", errorf(AlreadyExists, "snapshot %q exists for volume %s", name, r.SnapshotSource)
		}
	}
	src, ok := t.Get(vol)
	if !ok || src.Type == zfs.TypeSnapshot || !src.Managed {
		return nil, "", errorf(NotFound, "source volume %s not found", vol)
	}
	if r, ok := t.Get(id); ok {
		// a snapshot of that name we do not own, or one pending deletion
		return nil, "", errorf(AlreadyExists, "snapshot %s exists (managed=%v deferred=%v)", id, r.Managed, r.Deferred)
	}
	return Plan{{Kind: OpSnapshot, Target: p.full(id), Props: map[string]string{
		p.Names.managed():        "true",
		p.Names.snapshotName():   name,
		p.Names.snapshotSource(): vol,
	}}}, id, nil
}

// snapComponent is the zfs snapshot name for CSI snapshot name: the name
// itself when it is a valid zfs component and "<vol>@<name>" fits CSI's
// 128-byte id limit, else a stable hash of it. The CSI name always lives in
// the csi_snapshot_name property, which is what lookups match on.
func snapComponent(vol, name string) string {
	if zfs.ValidSnapshot("x@"+name) && len(vol)+1+len(name) <= 128 {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	return "csi-" + hex.EncodeToString(sum[:16])
}

// PlanDeleteSnapshot: missing or never-issued ids are done; snapshots with
// clones are destroyed deferred (lzc_destroy_snaps defer) instead of leaking.
func (p Planner) PlanDeleteSnapshot(t Table, snapID string) (Plan, error) {
	if _, _, err := parseSnapshotID(snapID); err != nil {
		return nil, nil
	}
	row, ok := t.Get(snapID)
	if !ok || row.Deferred {
		return nil, nil
	}
	if !row.Managed {
		return nil, errorf(FailedPrecondition, "snapshot %s is not managed", snapID)
	}
	return Plan{{Kind: OpDestroy, Target: row.Name, Defer: true, OkMissing: true}}, nil
}

func sourceOf(r CreateVolumeRequest) (string, string) {
	switch {
	case r.SourceSnapshotID != "":
		return "snapshot", r.SourceSnapshotID
	case r.SourceVolumeID != "":
		return "volume", r.SourceVolumeID
	}
	return "", ""
}

func kindType(k VolumeKind) zfs.Type {
	if k == Block {
		return zfs.TypeVolume
	}
	return zfs.TypeFilesystem
}
