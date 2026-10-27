package decide

import (
	"errors"
	"strings"

	"github.com/wommy/empty/internal/core/code"
	"github.com/wommy/empty/internal/core/name"
	"github.com/wommy/empty/internal/core/op"
	"github.com/wommy/empty/internal/core/req"
	"github.com/wommy/empty/internal/core/world"
)

// KindOf is a create request's column in the table.
func KindOf(r req.CreateVolume) ReqKind {
	switch r.Src.Kind() {
	case world.SrcSnapshot:
		return CreateFromSnapshot
	case world.SrcVolume:
		return CreateFromVolume
	}
	return CreatePlain
}

// CreateVolume decides how to make the world hold r's volume, committed.
func CreateVolume(b world.Bound[req.CreateVolume]) (op.Plan, *code.Refusal) {
	w, r := b.World(), b.Req()
	st := w.State(r.ID)
	c := Table[st][KindOf(r)]
	if c.Refuse != code.OK {
		return nil, code.Refuse(c.Refuse, "dataset for volume %s %s", r.ID, c.Why)
	}
	p, ref := createVolume(w, r, st)
	return check(c, p, ref)
}

// shape is what the request resolves to against this world: the size and
// block size a provision drives the volume to, and its source handle.
type shape struct {
	size   uint64 // Block: volsize. FS: refquota (0 = unlimited)
	bs     uint32 // Block only
	origin world.Origin
	// start is what a fresh clone has before its size set
	startSize, startRefres uint64
}

// resolve derives the target shape once, before branching on state, so a
// resume after a crash at any op drives the volume to the same end state as
// an uninterrupted run (impossible-reach F3, lifecycle CX4).
func resolve(w *world.World, r req.CreateVolume, needOrigin bool) (shape, *code.Refusal) {
	var s shape
	var srcSize uint64
	var srcBS uint32
	haveSrc := false
	switch r.Src.Kind() {
	case world.SrcSnapshot:
		rs, ok := w.Restore(r.Src.Snap())
		if ok {
			if rs.Kind() != r.Kind {
				return s, code.Refuse(code.InvalidArgument, "source is %v, requested %v", rs.Kind(), r.Kind)
			}
			s.origin, haveSrc = rs, true
			v := rs.View().Vol()
			srcBS, _ = v.BlockSize()
			srcSize = v.Capacity() // FS: the source volume's quota
			if r.Kind == world.Block {
				srcSize = rs.View().Volsize()
			}
			s.startSize = rs.View().Volsize()
		}
	case world.SrcVolume:
		cv, ok := w.Committed(r.Src.Vol())
		if ok {
			if cv.View().Kind() != r.Kind {
				return s, code.Refuse(code.InvalidArgument, "source is %v, requested %v", cv.View().Kind(), r.Kind)
			}
			cs, err := w.CloneSourceFor(cv, r.ID)
			switch {
			case errors.Is(err, world.ErrCloneSourceTaken):
				return s, code.Refuse(code.AlreadyExists, "clone source: %v", err)
			case errors.Is(err, world.ErrCloneSourceDeferred):
				return s, code.Refuse(code.Unavailable, "clone source: %v", err)
			case err != nil:
				return s, code.Refuse(code.InvalidArgument, "clone source: %v", err)
			}
			s.origin, haveSrc = cs, true
			srcBS, _ = cv.View().BlockSize()
			srcSize = cv.View().Capacity()
			if r.Kind == world.Block {
				s.startSize = srcSize
			}
		}
	}
	if needOrigin && r.Src.Kind() != world.SrcNone && !haveSrc {
		// only a CSI snapshot of, or a committed, volume we own: anything
		// else names nothing this endpoint issued (ADR 0003, type12 D09)
		return s, code.Refuse(code.NotFound, "source %s not found", r.Src.ID())
	}
	switch r.Kind {
	case world.Block:
		s.bs = r.BS
		if haveSrc {
			if r.BS != 0 && r.BS != srcBS {
				return s, code.Refuse(code.InvalidArgument, "volblocksize %d: a clone keeps its source's (%d)", r.BS, srcBS)
			}
			s.bs = srcBS
		} else if s.bs == 0 {
			s.bs = r.DefaultBS
		}
		s.size = r.Required.RoundUp(s.bs)
		if haveSrc && srcSize > s.size {
			s.size = srcSize // never smaller than the source
		}
	case world.FS:
		s.size = r.Required.N()
		if s.size == 0 && r.Limit.N() > 0 {
			s.size = r.Limit.N() // "at most limit" must not be unlimited
		}
		if haveSrc && s.size == 0 {
			s.size = srcSize
		}
		if r.Thick && s.size == 0 {
			return s, code.Refuse(code.InvalidArgument, "reserve needs a capacity or limit (the source is unlimited)")
		}
	}
	if l := r.Limit.N(); l > 0 && (s.size > l || (r.Kind == world.FS && s.size == 0)) {
		return s, code.Refuse(code.OutOfRange, "capacity %d exceeds limit %d (a clone is never smaller than its source)", s.size, l)
	}
	return s, nil
}

// sizeSet is what a fresh clone or a resumed provision still needs: it
// never shrinks, and thick zvols get refreservation=auto (zvol_auto_resv).
func sizeSet(kind world.VolKind, haveSize, haveRefres, want uint64, thick bool) []op.KV {
	var kv []op.KV
	if kind == world.Block {
		if want > haveSize {
			kv = append(kv, op.KV{K: "volsize", V: u64(want)})
		}
		if thick && haveRefres == 0 {
			kv = append(kv, op.KV{K: "refreservation", V: "auto"})
		}
		return kv
	}
	if want > 0 && (haveSize == 0 || want > haveSize) {
		kv = append(kv, op.KV{K: "refquota", V: u64(want)})
	}
	if thick && haveRefres < want {
		kv = append(kv, op.KV{K: "refreservation", V: u64(want)})
	}
	return kv
}

func createVolume(w *world.World, r req.CreateVolume, st world.State) (op.Plan, *code.Refusal) {
	switch st {
	case world.Absent:
		s, ref := resolve(w, r, true)
		if ref != nil {
			return nil, ref
		}
		free, err := w.Free(r.ID)
		if err != nil {
			return nil, code.Refuse(code.InvalidArgument, "volume %s: %v", r.ID, err)
		}
		var p op.Plan
		switch o := s.origin.(type) {
		case nil:
			if r.Kind == world.Block {
				vs, _ := op.NewVolsize(s.size, s.bs) // resolve rounded it
				props := r.Props
				if r.BS != 0 {
					props = props.With(op.KV{K: "volblocksize", V: u64(uint64(r.BS))})
				}
				p = op.Plan{op.NewCreateZvol(free, vs, !r.Thick, props)}
			} else {
				p = op.Plan{op.NewCreateFS(free, s.size, r.Thick, r.Props)}
			}
		default:
			if cs, ok := o.(world.CloneSource); ok {
				if cut, ok := op.NewCutCloneSource(cs); ok {
					p = append(p, cut)
				}
			}
			p = append(p, op.NewClone(o, free, r.Props))
			// a clone starts at its origin's volsize and without quota or
			// reservation
			if set, ok := op.NewSet(free, propsOf(sizeSet(r.Kind, s.startSize, 0, s.size, r.Thick))); ok {
				p = append(p, set)
			}
		}
		return append(p, op.NewCommit(w, free)), nil

	case world.Uncommitted:
		u, _ := w.Uncommitted(r.ID)
		v := u.View()
		sk, sid := v.Source()
		if v.Kind() != r.Kind || sk != r.Src.Kind() || sid != r.Src.ID() {
			return nil, code.Refuse(code.AlreadyExists, "volume %s is being provisioned as %v from %q %q", r.ID, v.Kind(), sk, sid)
		}
		s, ref := resolve(w, r, false) // the source may be gone by now; the clone is not
		if ref != nil {
			return nil, ref
		}
		if bs, _ := v.BlockSize(); r.BS != 0 && bs != r.BS {
			return nil, code.Refuse(code.AlreadyExists, "volume %s has volblocksize %d, requested %d", r.ID, bs, r.BS)
		}
		// resume only toward the request: sizes grow and reservations are
		// added, never removed, so a provision that already has more than
		// this request allows is another request's (found by the generated
		// table test; HEAD's sizeOps commits it and the identical retry is
		// AlreadyExists)
		if v.Thick() && !r.Thick {
			return nil, code.Refuse(code.AlreadyExists, "volume %s is being provisioned thick", r.ID)
		}
		if l := r.Limit.N(); l > 0 && v.Capacity() > l {
			return nil, code.Refuse(code.AlreadyExists, "volume %s is being provisioned at %d > limit %d", r.ID, v.Capacity(), l)
		}
		var p op.Plan
		if set, ok := op.NewSet(u, propsOf(sizeSet(r.Kind, v.Capacity(), v.Refres(), s.size, r.Thick))); ok {
			p = append(p, set)
		}
		local, _ := v.Local()
		var drift []op.KV
		for _, kv := range r.Props.All() {
			if k, ok := localKey(kv.K); ok && local[k] != kv.V {
				drift = append(drift, kv)
			}
		}
		if set, ok := op.NewSet(u, propsOf(drift)); ok {
			p = append(p, set)
		}
		return append(p, op.NewCommit(w, u)), nil

	case world.Committed:
		v, _ := w.Vol(r.ID)
		s, ref := resolve(w, r, false)
		if ref != nil && ref.Code != code.OutOfRange {
			return nil, ref
		}
		return nil, compatible(v, r, s.size)
	}
	return nil, code.Refuse(code.Internal, "unreachable state %v", st)
}

// compatible is CSI's CreateVolume idempotency rule for a committed volume.
func compatible(v world.VolView, r req.CreateVolume, want uint64) *code.Refusal {
	id := v.ID()
	sk, sid := v.Source()
	if v.Kind() != r.Kind || sk != r.Src.Kind() || sid != r.Src.ID() {
		return code.Refuse(code.AlreadyExists, "volume %s exists as %v from %q %q", id, v.Kind(), sk, sid)
	}
	have := v.Capacity()
	unlimited := v.Kind() == world.FS && have == 0 // satisfies any required size (type12 D07)
	if !unlimited && want > have {
		return code.Refuse(code.AlreadyExists, "volume %s has capacity %d < %d", id, have, want)
	}
	if l := r.Limit.N(); l > 0 && (unlimited || have > l) {
		return code.Refuse(code.AlreadyExists, "volume %s has capacity %d > limit %d", id, have, l)
	}
	if r.Thick != v.Thick() {
		return code.Refuse(code.AlreadyExists, "volume %s exists with thick=%v", id, v.Thick())
	}
	if bs, _ := v.BlockSize(); r.BS != 0 && bs != r.BS {
		return code.Refuse(code.AlreadyExists, "volume %s has volblocksize %d", id, bs)
	}
	local, _ := v.Local()
	for _, kv := range r.Props.All() {
		if k, ok := localKey(kv.K); !ok || local[k] != kv.V {
			return code.Refuse(code.AlreadyExists, "volume %s exists without %s=%s set locally", id, kv.K, kv.V)
		}
	}
	return nil
}

// Policy is configuration a decision reads.
type Policy struct {
	// DestroyForeignSnapshots lets DeleteVolume destroy snapshots it did
	// not cut (the -destroy-foreign-snapshots flag).
	DestroyForeignSnapshots bool
}

// DeleteVolume decides how to remove r's volume, or refuses (CSI: the
// volume MUST NOT be altered when it cannot be deleted).
func DeleteVolume(b world.Bound[req.DeleteVolume], pol Policy) (op.Plan, *code.Refusal) {
	w, r := b.World(), b.Req()
	st := w.State(r.ID)
	c := Table[st][Delete]
	if c.Refuse != code.OK {
		return nil, code.Refuse(c.Refuse, "dataset for volume %s %s", r.ID, c.Why)
	}
	if st == world.Absent {
		return check(c, nil, nil)
	}
	o, _ := w.Owned(r.ID)
	p, ref := deleteVolume(o, pol)
	return check(c, p, ref)
}

// DeleteScope is the second scan a vol-to-vol clone's delete needs: its
// origin hangs off another volume, whose stamp decides the release. The
// shell scans r.Scope(), and if DeleteScope says so, rescans the widened
// request (one extra zfs get, only for such clones).
func DeleteScope(w *world.World, r req.DeleteVolume) (req.DeleteVolume, bool) {
	o, ok := w.Owned(r.ID)
	if !ok {
		return r, false
	}
	if _, verdict, src := o.ReleasableOrigin(); verdict == world.OriginUncovered {
		r.Origin = src
		return r, true
	}
	return r, false
}

func deleteVolume(o world.OwnedVol, pol Policy) (op.Plan, *code.Refusal) {
	v := o.View()
	id := v.ID()
	if v.Children() > 0 {
		return nil, code.Refuse(code.FailedPrecondition, "volume %s has %d child datasets the endpoint did not make", id, v.Children())
	}
	var doomed []world.DoomedSnap
	var foreign []string
	for s := range v.Snaps() {
		if s.Clones() > 0 {
			return nil, code.Refuse(code.FailedPrecondition, "volume %s: snapshot %s has dependent clones", id, s.ID())
		}
		switch s.Own() {
		case world.SnapCSI:
			return nil, code.Refuse(code.FailedPrecondition, "volume %s has snapshot %s", id, s.ID())
		case world.SnapCSIDeferred:
			return nil, code.Refuse(code.FailedPrecondition, "volume %s: deleted snapshot %s is still held", id, s.ID())
		case world.SnapInvalid:
			return nil, code.Refuse(code.FailedPrecondition, "volume %s: snapshot %s has inconsistent ownership markers", id, s.ID())
		case world.SnapForeign:
			if d, ok := s.Doom(pol.DestroyForeignSnapshots); ok {
				doomed = append(doomed, d)
			} else {
				foreign = append(foreign, s.ID())
			}
		case world.SnapCloneSource:
			if d, ok := s.Doom(false); ok {
				doomed = append(doomed, d)
			}
		}
	}
	if len(foreign) > 0 {
		return nil, code.Refuse(code.FailedPrecondition, "volume %s has snapshots the endpoint did not make: %s (delete them, or run with -destroy-foreign-snapshots)",
			id, strings.Join(foreign, ", "))
	}
	var p op.Plan
	switch ro, verdict, src := o.ReleasableOrigin(); verdict {
	case world.ReleaseOrigin:
		p = append(p, op.NewReleaseOrigin(ro))
	case world.OriginUncovered: // the shell skipped DeleteScope: a bug, not a pool state
		return nil, code.Refuse(code.Internal, "volume %s: origin's volume %s was not scanned", id, src)
	}
	for _, d := range doomed {
		p = append(p, op.NewDestroySnap(d))
	}
	return append(p, op.NewDestroyVol(o)), nil
}

func propsOf(kv []op.KV) op.Props {
	p, _ := op.NewProps(nil)
	return p.With(kv...)
}

func localKey(n string) (world.LocalKey, bool) {
	for k, ln := range world.LocalNames {
		if ln == n {
			return world.LocalKey(k), true
		}
	}
	return 0, false
}

func u64(n uint64) string {
	var b [20]byte
	i := len(b)
	for {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
		if n == 0 {
			return string(b[i:])
		}
	}
}

var _ = name.MaxID
