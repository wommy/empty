// Package req parses wire requests once into typed requests whose fields
// are always valid: ids that name something the endpoint could have issued,
// capacities that cannot overflow, at most one content source, and a single
// "thick" flag instead of two kind-specific booleans.
package req

import (
	"math"
	"strconv"

	"github.com/wommy/empty/internal/core/code"
	"github.com/wommy/empty/internal/core/name"
	"github.com/wommy/empty/internal/core/op"
	"github.com/wommy/empty/internal/core/world"
	"github.com/wommy/empty/internal/endpoint"
)

// Bytes is a capacity of at most MaxInt64 (CSI's int64). Rounding it up to
// any block size up to 16 MiB cannot overflow uint64 (impossible-reach F10).
type Bytes struct{ n uint64 }

// N is the byte count.
func (b Bytes) N() uint64 { return b.n }

// RoundUp rounds to a multiple of bs (bs > 0).
func (b Bytes) RoundUp(bs uint32) uint64 {
	if r := b.n % uint64(bs); r != 0 {
		return b.n + uint64(bs) - r
	}
	return b.n
}

func bytes(n uint64) (Bytes, *code.Refusal) {
	if n > math.MaxInt64 {
		return Bytes{}, code.Refuse(code.OutOfRange, "capacity %d exceeds %d", n, uint64(math.MaxInt64))
	}
	return Bytes{n}, nil
}

// BlockSize is a zvol block size: a power of two in [512, 16 MiB]. The
// zero value is OpenZFS's default, 16 KiB, so no zero ever divides
// (impossible-reach F15).
type BlockSize struct{ n uint32 }

// NewBlockSize validates n (zvol.c zvol_check_volblocksize).
func NewBlockSize(n uint64) (BlockSize, bool) {
	return BlockSize{uint32(n)}, n >= 512 && n <= 16<<20 && n&(n-1) == 0
}

// N is the size in bytes.
func (b BlockSize) N() uint32 {
	if b.n == 0 {
		return 16384
	}
	return b.n
}

// Source is at most one content source: a sum type by construction.
type Source struct {
	kind world.SrcKind
	snap name.SnapID
	vol  name.VolID
}

func (s Source) Kind() world.SrcKind { return s.kind }

// Snap is the snapshot source (Kind == SrcSnapshot).
func (s Source) Snap() name.SnapID { return s.snap }

// Vol is the volume source (Kind == SrcVolume).
func (s Source) Vol() name.VolID { return s.vol }

// ID is the source id as the volume records it.
func (s Source) ID() string {
	switch s.kind {
	case world.SrcSnapshot:
		return s.snap.String()
	case world.SrcVolume:
		return s.vol.String()
	}
	return ""
}

// CreateVolumeIn is the wire request (proto CreateVolumeRequest).
type CreateVolumeIn struct {
	Name             string
	Block            bool
	Capacity, Limit  uint64
	Sparse, Reserve  bool
	SourceSnapshotID string
	SourceVolumeID   string
	Properties       map[string]string
}

// CreateVolume is a parsed CreateVolume request.
type CreateVolume struct {
	ID        name.VolID
	Kind      world.VolKind
	Required  Bytes // 0: unlimited (fs) or the source's size (clone)
	Limit     Bytes // 0: none
	Thick     bool  // Block: !sparse. FS: reserve
	Src       Source
	Props     op.Props    // canonical allowlisted natives, without volblocksize
	Local     world.Local // the same values by key, for compatibility checks
	BS        uint32      // requested volblocksize; 0 = unspecified
	DefaultBS uint32      // the endpoint's default for new zvols
}

// Scope: the target and the source volume, with the Local columns.
func (r CreateVolume) Scope() world.Scope {
	ids := []name.VolID{r.ID}
	switch r.Src.kind {
	case world.SrcSnapshot:
		ids = append(ids, r.Src.snap.Vol())
	case world.SrcVolume:
		ids = append(ids, r.Src.vol)
	}
	return world.Vols(true, ids...)
}

// ParseCreateVolume parses and validates a CreateVolume request once.
func ParseCreateVolume(in CreateVolumeIn, def BlockSize) (CreateVolume, *code.Refusal) {
	defaultBS := def.N()
	var r CreateVolume
	id, err := name.ParseVolID(in.Name)
	if err != nil {
		return r, code.Refuse(code.InvalidArgument, "invalid volume name %q: %v", in.Name, err)
	}
	r.ID, r.DefaultBS = id, defaultBS
	r.Kind = world.FS
	if in.Block {
		r.Kind = world.Block
	}
	var ref *code.Refusal
	if r.Required, ref = bytes(in.Capacity); ref != nil {
		return r, ref
	}
	if r.Limit, ref = bytes(in.Limit); ref != nil {
		return r, ref
	}
	switch {
	case in.Block && in.Reserve:
		return r, code.Refuse(code.InvalidArgument, "reserve applies to filesystems; use sparse for zvols")
	case !in.Block && in.Sparse:
		return r, code.Refuse(code.InvalidArgument, "sparse applies to zvols; filesystems are thin unless reserved")
	}
	r.Thick = in.Reserve || (in.Block && !in.Sparse)

	switch {
	case in.SourceSnapshotID != "" && in.SourceVolumeID != "":
		return r, code.Refuse(code.InvalidArgument, "at most one content source")
	case in.SourceSnapshotID != "":
		s, err := name.ParseSnapID(in.SourceSnapshotID)
		if err != nil { // ids are opaque: one we never issued names nothing
			return r, code.Refuse(code.NotFound, "source snapshot %s not found", in.SourceSnapshotID)
		}
		r.Src = Source{kind: world.SrcSnapshot, snap: s}
	case in.SourceVolumeID != "":
		v, err := name.ParseVolID(in.SourceVolumeID)
		if err != nil {
			return r, code.Refuse(code.NotFound, "source volume %s not found", in.SourceVolumeID)
		}
		r.Src = Source{kind: world.SrcVolume, vol: v}
	}
	if r.Src.kind == world.SrcNone {
		if in.Block && in.Capacity == 0 {
			return r, code.Refuse(code.InvalidArgument, "block volume needs a capacity")
		}
		if !in.Block && in.Reserve && in.Capacity == 0 && in.Limit == 0 {
			return r, code.Refuse(code.InvalidArgument, "reserve needs a capacity or limit")
		}
	}

	kind := endpoint.Filesystem
	if in.Block {
		kind = endpoint.Block
	}
	canon, err := endpoint.ValidateProperties(kind, in.Properties)
	if err != nil {
		return r, code.Refuse(code.InvalidArgument, "%v", err)
	}
	native := map[string]string{}
	for k, v := range canon {
		if k == "volblocksize" {
			n, _ := strconv.ParseUint(v, 10, 32)
			r.BS = uint32(n)
			continue
		}
		native[k] = v
		for lk, ln := range world.LocalNames {
			if ln == k {
				r.Local[lk] = v
			}
		}
	}
	if r.Props, err = op.NewProps(native); err != nil {
		return r, code.Refuse(code.InvalidArgument, "%v", err)
	}
	if r.Src.kind == world.SrcNone && in.Block {
		bs := r.BS
		if bs == 0 {
			bs = defaultBS
		}
		if l := r.Limit.n; l > 0 && r.Required.RoundUp(bs) > l {
			return r, code.Refuse(code.OutOfRange, "capacity %d (rounded) exceeds limit %d", r.Required.RoundUp(bs), l)
		}
	} else if l := r.Limit.n; l > 0 && r.Required.n > l {
		return r, code.Refuse(code.OutOfRange, "capacity %d exceeds limit %d", r.Required.n, l)
	}
	return r, nil
}

// DeleteVolume is a parsed DeleteVolume request.
type DeleteVolume struct {
	ID     name.VolID
	Origin name.VolID // zero, or the volume its clone-source origin hangs off (decide.DeleteScope)
}

// Scope: the volume's own subtree, plus its origin's volume when it is a
// volume-to-volume clone. Clones anywhere are visible through its
// snapshots' `clones` property, so no whole-parent scan is needed (dod3 F2).
func (r DeleteVolume) Scope() world.Scope {
	if r.Origin.IsZero() {
		return world.Vols(false, r.ID)
	}
	return world.Vols(false, r.ID, r.Origin)
}

// Parsed is the outcome of parsing an id-only request.
type Parsed uint8

const (
	Proceed     Parsed = iota
	NeverIssued        // a non-empty id the endpoint could never have issued: it names nothing
	Refused
)

// ParseDeleteVolume: an empty id is InvalidArgument; an id we never issue
// is already deleted (CSI: OK) (type12 D14).
func ParseDeleteVolume(id string) (DeleteVolume, Parsed, *code.Refusal) {
	if id == "" {
		return DeleteVolume{}, Refused, code.Refuse(code.InvalidArgument, "volume id is required")
	}
	v, err := name.ParseVolID(id)
	if err != nil {
		return DeleteVolume{}, NeverIssued, nil
	}
	return DeleteVolume{ID: v}, Proceed, nil
}
