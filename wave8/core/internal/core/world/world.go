// Package world is the parse boundary of the core: `zfs get -j -p` output in,
// an immutable, validated World out. It is the only package that decides
// what a dataset is and who owns it, and the only one that can mint the
// handles (FreeVol, OwnedVol, CSISnap, ...) that op constructors require.
//
// Everything OpenZFS guarantees about its own output (wave5/zfs-invariants.md
// §7) is checked once, in Parse. Nothing downstream re-checks.
package world

import (
	"iter"
	"slices"
	"strings"

	"github.com/wommy/empty/internal/core/name"
)

// VolKind is the kind of a volume row. There is no third value: a row zfs
// printed with another type never becomes a Vol.
type VolKind uint8

const (
	FS    VolKind = iota + 1 // filesystem
	Block                    // zvol
)

func (k VolKind) String() string {
	switch k {
	case FS:
		return "filesystem"
	case Block:
		return "block"
	}
	return "VolKind(?)"
}

// VolOwn is a volume row's ownership, derived once at parse from the
// locally set markers under the endpoint's prefix. The zero value is
// VolForeign: an unclassified row is never acted on.
type VolOwn uint8

const (
	VolForeign     VolOwn = iota // no local managed marker; received; another prefix; name marker for another id (renamed or copied); unaddressable name
	VolInvalid                   // our markers in a combination neither we nor democratic-csi write (lifecycle-fsm I1-I3): refused by every request
	VolUncommitted               // managed, name marker = id, no provision_success: a provision in flight or crashed
	VolCommitted                 // managed, name marker = id, provision_success: a volume the CO has been given
)

func (o VolOwn) String() string {
	return [...]string{"Foreign", "Invalid", "Uncommitted", "Committed"}[o]
}

// SnapOwn is a snapshot row's ownership. Zero value SnapForeign.
type SnapOwn uint8

const (
	SnapForeign     SnapOwn = iota // an administrator's, sanoid's, a received one, or any snapshot of a foreign volume
	SnapInvalid                    // our markers, inconsistent (I8-I10): never destroyed, even with -destroy-foreign-snapshots
	SnapCSI                        // a CSI snapshot we cut: managed, csi_snapshot_name set, source marker = its volume
	SnapCSIDeferred                // a CSI snapshot already deleted (defer_destroy=on, held by clones): gone as far as CSI knows
	SnapCloneSource                // "<vol>@volume-source-for-volume-<VolID>" on an owned volume, stamped managed + clone_source_for=<VolID>
)

func (o SnapOwn) String() string {
	return [...]string{"Foreign", "Invalid", "CSI", "CSIDeferred", "CloneSource"}[o]
}

// CloneSourcePrefix is the reserved snapshot name prefix (democratic-csi's).
const CloneSourcePrefix = "volume-source-for-volume-"

// SrcKind is a volume's recorded content source.
type SrcKind uint8

const (
	SrcNone SrcKind = iota
	SrcSnapshot
	SrcVolume
)

func (s SrcKind) String() string { return [...]string{"", "snapshot", "volume"}[s] }

// Vol is one filesystem or zvol directly under the parent. 120 bytes.
// Fields that do not apply to the kind are zero by construction (Parse).
type Vol struct {
	id       name.VolID // 16
	origin   string     // full origin snapshot name, "" if not a clone
	srcID    string     // content source id marker ("" iff src == SrcNone)
	volName  string     // csi_volume_name marker (== id for owned rows)
	guid     uint64
	size     uint64 // Block: volsize (>0, % bs == 0). FS: refquota (0 = unlimited)
	refres   uint64
	creation int64
	snapLo   uint32 // this volume's snapshots are snaps[snapLo:snapHi]
	snapHi   uint32
	bs       uint32 // Block: volblocksize (power of two, 512..16M). FS: 0
	children uint16 // datasets below this volume (never ours; refuse destroy)
	kind     VolKind
	own      VolOwn
	src      SrcKind
}

// Snap is one snapshot of a Vol. 72 bytes.
type Snap struct {
	comp     string // the part after '@'
	csiName  string // csi_snapshot_name marker, SnapCSI* only
	guid     uint64
	volsize  uint64 // Block heads: the volume's size when cut. FS heads: 0
	creation int64
	vol      uint32 // index into World.vols
	clones   uint32 // number of clones, anywhere in the pool (the `clones` property)
	own      SnapOwn
	deferred bool
}

// World is one scan, decoded. Immutable after Parse.
type World struct {
	parent  name.Dataset
	prefix  string
	scope   Scope
	lease   Lease  // zero: read-only (not scanned under locks)
	vols    []Vol  // sorted by id
	snaps   []Snap // grouped by vol, sorted by comp within a vol
	local   []Local
	dropped int // rows under the parent with names the endpoint never issues
}

// Parent is the configured parent dataset.
func (w *World) Parent() name.Dataset { return w.parent }

// Scope is what this world covers.
func (w *World) Scope() Scope { return w.scope }

// Len returns the number of volumes and snapshots.
func (w *World) Len() (vols, snaps int) { return len(w.vols), len(w.snaps) }

func (w *World) find(id name.VolID) (int, bool) {
	return slices.BinarySearchFunc(w.vols, id.String(), func(v Vol, s string) int { return strings.Compare(v.id.String(), s) })
}

// State is a volume id's lifecycle state in this world: Absent or the
// row's ownership. It is the row key of the decision table.
type State uint8

const (
	Absent State = iota
	Foreign
	Invalid
	Uncommitted
	Committed
	nStates
)

// NStates is the number of States (for tables indexed by State).
const NStates = int(nStates)

func (s State) String() string {
	return [...]string{"Absent", "Foreign", "Invalid", "Uncommitted", "Committed"}[s]
}

// State of id. Only meaningful for covered ids: decide reaches it through a
// Bound, which checked coverage.
func (w *World) State(id name.VolID) State {
	i, ok := w.find(id)
	if !ok {
		return Absent
	}
	return State(w.vols[i].own) + Foreign
}

// ---- read-only views --------------------------------------------------------

// VolView reads one volume row.
type VolView struct {
	w *World
	i int
}

// Vol returns the row for id.
func (w *World) Vol(id name.VolID) (VolView, bool) {
	i, ok := w.find(id)
	return VolView{w, i}, ok
}

// Vols yields every volume row in id order.
func (w *World) Vols() iter.Seq[VolView] {
	return func(yield func(VolView) bool) {
		for i := range w.vols {
			if !yield(VolView{w, i}) {
				return
			}
		}
	}
}

func (v VolView) r() *Vol                   { return &v.w.vols[v.i] }
func (v VolView) Guid() uint64              { return v.r().guid }
func (v VolView) ID() name.VolID            { return v.r().id }
func (v VolView) Kind() VolKind             { return v.r().kind }
func (v VolView) Own() VolOwn               { return v.r().own }
func (v VolView) Source() (SrcKind, string) { return v.r().src, v.r().srcID }
func (v VolView) Children() int             { return int(v.r().children) }
func (v VolView) Creation() int64           { return v.r().creation }
func (v VolView) Origin() string            { return v.r().origin }
func (v VolView) Refres() uint64            { return v.r().refres }

// Thick reports a reservation: refreservation > 0.
func (v VolView) Thick() bool { return v.r().refres > 0 }

// Capacity: volsize for a zvol, refquota for a filesystem (0 = unlimited).
func (v VolView) Capacity() uint64 { return v.r().size }

// BlockSize is the zvol's volblocksize; ok is false for filesystems.
func (v VolView) BlockSize() (uint32, bool) { return v.r().bs, v.r().kind == Block }

// Dataset is the row's full name.
func (v VolView) Dataset() name.Dataset {
	d, _ := v.w.parent.Child(v.r().id) // fits: Parse built it from a printed name
	return d
}

// Local returns the allowlisted properties set locally on the volume; ok is
// false when the scan did not fetch them (Scope.Local).
func (v VolView) Local() (Local, bool) {
	if v.w.local == nil {
		return Local{}, false
	}
	return v.w.local[v.i], true
}

// Snaps yields the volume's snapshots.
func (v VolView) Snaps() iter.Seq[SnapView] {
	return func(yield func(SnapView) bool) {
		r := v.r()
		for i := r.snapLo; i < r.snapHi; i++ {
			if !yield(SnapView{v.w, int(i)}) {
				return
			}
		}
	}
}

// Snap returns the volume's snapshot with component c.
func (v VolView) Snap(c string) (SnapView, bool) {
	r := v.r()
	s := v.w.snaps[r.snapLo:r.snapHi]
	i, ok := slices.BinarySearchFunc(s, c, func(s Snap, c string) int { return strings.Compare(s.comp, c) })
	return SnapView{v.w, int(r.snapLo) + i}, ok
}

// SnapView reads one snapshot row.
type SnapView struct {
	w *World
	i int
}

func (s SnapView) r() *Snap          { return &s.w.snaps[s.i] }
func (s SnapView) Vol() VolView      { return VolView{s.w, int(s.r().vol)} }
func (s SnapView) Component() string { return s.r().comp }
func (s SnapView) Own() SnapOwn      { return s.r().own }
func (s SnapView) Clones() int       { return int(s.r().clones) }
func (s SnapView) Guid() uint64      { return s.r().guid }
func (s SnapView) Deferred() bool    { return s.r().deferred }
func (s SnapView) CSIName() string   { return s.r().csiName }
func (s SnapView) Volsize() uint64   { return s.r().volsize }
func (s SnapView) ID() string        { return s.Vol().ID().String() + "@" + s.r().comp }
func (s SnapView) Snapshot() name.Snapshot {
	n, _ := s.Vol().Dataset().Snap(s.r().comp)
	return n
}
