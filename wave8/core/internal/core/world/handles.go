package world

import (
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/wommy/empty/internal/core/name"
)

// Handles are proofs, minted only here. An op constructor (package op) takes
// a handle, never a name, so "an op targets something we do not own" does
// not compile outside this package: there is no way to build an OwnedVol
// for a Foreign row, or a FreeVol for an existing one.

// ---- request binding --------------------------------------------------------

// Request is anything decide can be asked: it says what it must see.
type Request interface{ Scope() Scope }

// Bound is a world together with a request it covers. decide takes only
// Bound values, so planning against a world that never read the request's
// volumes (a partial table read as "absent") does not compile.
type Bound[R Request] struct {
	w *World
	r R
}

// ErrScope: the world does not cover the request. A program bug.
var ErrScope = errors.New("world does not cover the request")

// CloneSourceFor refusals: the name is someone else's snapshot (HEAD:
// AlreadyExists), or our own released one that a clone still pins: never
// reused, since cloning a deferred snapshot inherits its pending destroy.
var (
	ErrCloneSourceTaken    = errors.New("exists and was not cut by the endpoint for this clone")
	ErrCloneSourceDeferred = errors.New("released by an earlier delete and still pinned by a clone")
)

// ErrStale: the world was not scanned under the caller's lease (B2).
var ErrStale = errors.New("world was not scanned under this request's locks")

// Lease names one acquisition of a request's locks. The endpoint mints it
// right after the locks are held (NewLease), scans with ParseUnder(lease),
// and binds with the same lease: a world from an earlier acquisition, from
// another request, or from a lock-free read (List's single-flight) cannot
// bind, so cannot mint a handle that leads to an op. It proves "scanned
// after I held the locks", not "the pool has not changed since": that is
// what the non-recursive destroy, TolNone on clone and the guid guard in
// Apply are for (OWN-15).
type Lease struct{ n uint64 }

var leases atomic.Uint64

func NewLease() Lease        { return Lease{leases.Add(1)} }
func (l Lease) IsZero() bool { return l.n == 0 }

// Bind checks coverage once.
func Bind[R Request](w *World, l Lease, r R) (Bound[R], error) {
	if w == nil || !w.scope.Includes(r.Scope()) {
		return Bound[R]{}, ErrScope
	}
	if l.n == 0 || w.lease != l {
		return Bound[R]{}, ErrStale
	}
	return Bound[R]{w, r}, nil
}

func (b Bound[R]) World() *World { return b.w }
func (b Bound[R]) Req() R        { return b.r }

// Marker is the full user-property name for a marker suffix (":managed_resource").
func (w *World) Marker(suffix string) string { return w.prefix + suffix }

// ---- volume handles -----------------------------------------------------------

// FreeVol is a covered id with no dataset, whose full name fits OpenZFS.
type FreeVol struct {
	w  *World
	id name.VolID
	ds name.Dataset
}

// Free mints a FreeVol.
func (w *World) Free(id name.VolID) (FreeVol, error) {
	if !w.scope.Covers(id) {
		return FreeVol{}, ErrScope
	}
	if _, ok := w.find(id); ok {
		return FreeVol{}, fmt.Errorf("%s exists", id)
	}
	ds, err := w.parent.Child(id)
	if err != nil {
		return FreeVol{}, err
	}
	return FreeVol{w, id, ds}, nil
}

// OwnedVol is an Uncommitted or Committed row.
type OwnedVol struct{ v VolView }

// UncommittedVol is a provision to resume.
type UncommittedVol struct{ OwnedVol }

// CommittedVol is a volume the CO has been given.
type CommittedVol struct{ OwnedVol }

// Owned mints an OwnedVol.
func (w *World) Owned(id name.VolID) (OwnedVol, bool) {
	v, ok := w.Vol(id)
	if !ok || (v.Own() != VolUncommitted && v.Own() != VolCommitted) {
		return OwnedVol{}, false
	}
	return OwnedVol{v}, true
}

// Uncommitted mints an UncommittedVol.
func (w *World) Uncommitted(id name.VolID) (UncommittedVol, bool) {
	o, ok := w.Owned(id)
	return UncommittedVol{o}, ok && o.v.Own() == VolUncommitted
}

// Committed mints a CommittedVol.
func (w *World) Committed(id name.VolID) (CommittedVol, bool) {
	o, ok := w.Owned(id)
	return CommittedVol{o}, ok && o.v.Own() == VolCommitted
}

func (o OwnedVol) View() VolView         { return o.v }
func (o OwnedVol) Dataset() name.Dataset { return o.v.Dataset() }
func (o OwnedVol) Guid() uint64          { return o.v.Guid() }
func (f FreeVol) Dataset() name.Dataset  { return f.ds }
func (f FreeVol) ID() name.VolID         { return f.id }
func (f FreeVol) World() *World          { return f.w }

// Target is a dataset a provision may set properties on and commit: one
// created earlier in the same plan (FreeVol), or one being resumed.
type Target interface {
	Dataset() name.Dataset
	target()
}

func (FreeVol) target()        {}
func (UncommittedVol) target() {}

// ---- snapshot handles -------------------------------------------------------

// Origin is something a clone may hang off.
type Origin interface {
	Snapshot() name.Snapshot
	// Source is the content-source marker pair the clone records.
	Source() (SrcKind, string)
	// Kind is the source volume's kind; a clone has the same.
	Kind() VolKind
	origin()
}

// RestoreSource is a live CSI snapshot of a committed volume we own.
type RestoreSource struct{ s SnapView }

// Restore mints a RestoreSource for a CSI snapshot id.
func (w *World) Restore(id name.SnapID) (RestoreSource, bool) {
	v, ok := w.Vol(id.Vol())
	if !ok || v.Own() != VolCommitted {
		return RestoreSource{}, false
	}
	s, ok := v.Snap(id.Component())
	if !ok || s.Own() != SnapCSI {
		return RestoreSource{}, false
	}
	return RestoreSource{s}, true
}

func (r RestoreSource) Snapshot() name.Snapshot   { return r.s.Snapshot() }
func (r RestoreSource) View() SnapView            { return r.s }
func (r RestoreSource) Source() (SrcKind, string) { return SrcSnapshot, r.s.ID() }
func (r RestoreSource) Kind() VolKind             { return r.s.Vol().Kind() }
func (RestoreSource) origin()                     {}

// CloneSource is "<src>@volume-source-for-volume-<id>" on a committed source:
// either already there (a resumed clone) or still to be cut.
type CloneSource struct {
	w        *World
	target   name.VolID
	kind     VolKind
	snap     name.Snapshot
	srcID    name.VolID
	existing bool
}

// CloneSourceFor names the clone source of src for target. A snapshot of
// that name that is not ours (an administrator took the reserved name) is
// refused: we never clone what we did not cut.
func (w *World) CloneSourceFor(src CommittedVol, target name.VolID) (CloneSource, error) {
	c := CloneSourcePrefix + target.String()
	sn, err := src.Dataset().Snap(c)
	if err != nil {
		return CloneSource{}, err
	}
	cs := CloneSource{w: src.v.w, target: target, kind: src.v.Kind(), snap: sn, srcID: src.v.ID()}
	if s, ok := src.v.Snap(c); ok {
		switch {
		case s.Own() != SnapCloneSource:
			return CloneSource{}, fmt.Errorf("%s: %w", sn, ErrCloneSourceTaken)
		case s.Deferred(): // released by a delete, still pinned (A3)
			return CloneSource{}, fmt.Errorf("%s: %w", sn, ErrCloneSourceDeferred)
		}
		cs.existing = true
	}
	return cs, nil
}

func (c CloneSource) Snapshot() name.Snapshot   { return c.snap }
func (c CloneSource) Exists() bool              { return c.existing }
func (c CloneSource) World() *World             { return c.w }
func (c CloneSource) For() name.VolID           { return c.target }
func (c CloneSource) Source() (SrcKind, string) { return SrcVolume, c.srcID.String() }
func (c CloneSource) Kind() VolKind             { return c.kind }
func (CloneSource) origin()                     {}

// ReleasableOrigin is a volume's clone-source origin that deleting the
// volume releases (`destroy -d`). It is ours only when all hold (OWN-13):
//   - the origin is exactly <parent>/<src>@volume-source-for-volume-<this id>
//   - src is a volume the endpoint owns, and the world covers it
//   - the snapshot carries our stamp for this clone (SnapCloneSource)
//   - it is not already deferred (an earlier, interrupted delete released it)
//
// HEAD also releases a bare (unmarked) snapshot of that name, as legacy
// democratic-csi; the core does not: the name is never ownership.
type ReleasableOrigin struct{ snap name.Snapshot }

// OriginVerdict is what deleting a volume does with its origin.
type OriginVerdict uint8

const (
	KeepOrigin      OriginVerdict = iota // none, or not ours to release
	ReleaseOrigin                        // ours: release it
	OriginUncovered                      // looks like ours, its volume was not scanned: widen and rescan
)

func (o OwnedVol) ReleasableOrigin() (ReleasableOrigin, OriginVerdict, name.VolID) {
	v := o.v
	if v.Origin() == "" {
		return ReleasableOrigin{}, KeepOrigin, name.VolID{}
	}
	sn, err := name.ParseSnapshot(v.Origin())
	if err != nil || sn.Component() != CloneSourcePrefix+v.ID().String() {
		return ReleasableOrigin{}, KeepOrigin, name.VolID{}
	}
	rel, ok := sn.Dataset().Rel(v.w.parent)
	if !ok {
		return ReleasableOrigin{}, KeepOrigin, name.VolID{}
	}
	src, err := name.ParseVolID(rel)
	if err != nil {
		return ReleasableOrigin{}, KeepOrigin, name.VolID{}
	}
	if !v.w.scope.Covers(src) {
		return ReleasableOrigin{}, OriginUncovered, src
	}
	sv, ok := v.w.Vol(src)
	if !ok || (sv.Own() != VolCommitted && sv.Own() != VolUncommitted) {
		return ReleasableOrigin{}, KeepOrigin, src
	}
	s, ok := sv.Snap(sn.Component())
	if !ok || s.Own() != SnapCloneSource || s.Deferred() {
		return ReleasableOrigin{}, KeepOrigin, src
	}
	return ReleasableOrigin{sn}, ReleaseOrigin, src
}

func (r ReleasableOrigin) Snapshot() name.Snapshot { return r.snap }

// DoomedSnap is a snapshot a DeleteVolume may destroy: ours and unpinned
// (a clone source nobody clones), or, only when the operator opted in, a
// foreign one nobody clones. Never a live CSI snapshot, never Invalid.
type DoomedSnap struct{ s SnapView }

// Doom mints a DoomedSnap.
func (s SnapView) Doom(destroyForeign bool) (DoomedSnap, bool) {
	if s.Clones() > 0 || s.Deferred() {
		return DoomedSnap{}, false
	}
	switch s.Own() {
	case SnapCloneSource:
		return DoomedSnap{s}, true
	case SnapForeign:
		return DoomedSnap{s}, destroyForeign
	}
	return DoomedSnap{}, false
}

func (d DoomedSnap) Snapshot() name.Snapshot { return d.s.Snapshot() }
func (d DoomedSnap) Guid() uint64            { return d.s.Guid() }

// DeferrableSnap is a snapshot a DeleteSnapshot or Reconcile may destroy
// with -d: a live CSI snapshot of an owned volume, or a stamped clone
// source. It is a different type from DoomedSnap on purpose (SNP-06): the
// plain destroy fails on a cloned snapshot, which for DeleteSnapshot would
// be FailedPrecondition forever; -d hides it until its last clone goes.
type DeferrableSnap struct{ s SnapView }

func (s SnapView) Deferrable() (DeferrableSnap, bool) {
	switch s.Own() {
	case SnapCSI, SnapCloneSource:
		return DeferrableSnap{s}, true
	}
	return DeferrableSnap{}, false // already deferred, foreign or invalid
}

func (d DeferrableSnap) Snapshot() name.Snapshot { return d.s.Snapshot() }
