// Package decide is the core's pure decision: (Bound world+request) →
// (Plan | Refusal). The lifecycle is a transition table (Table): for each
// (state of the target id, request kind) it states the refusal, or the op
// kinds a plan may contain in order, the state it ends in, the states a
// crash may leave behind, and the cross-row refusals it may return. Every
// decision is checked against its cell before it is returned, and the
// tests are generated from the table.
package decide

import (
	"fmt"
	"slices"

	"github.com/wommy/empty/internal/core/code"
	"github.com/wommy/empty/internal/core/op"
	"github.com/wommy/empty/internal/core/world"
)

// ReqKind is the column key of the table.
type ReqKind uint8

const (
	CreatePlain ReqKind = iota
	CreateFromSnapshot
	CreateFromVolume
	Delete
	NReq
)

func (r ReqKind) String() string {
	return [...]string{"CreatePlain", "CreateFromSnapshot", "CreateFromVolume", "Delete"}[r]
}

// Step is one position of a plan: one of Kinds, possibly absent (Opt), or
// repeated any number of times including none (Many).
type Step struct {
	Kinds []op.Kind
	Opt   bool
	Many  bool
}

// Cell is one transition.
type Cell struct {
	Refuse code.Code     // != OK: the request is refused here, always, with no ops
	Steps  []Step        // the plan's op kinds, in order
	To     world.State   // the target's state after the whole plan
	Mid    []world.State // states legal after a crash between ops
	Errs   []code.Code   // cross-row refusals the decision may return instead
	Why    string        // for refusals: the stable message
}

func one(k ...op.Kind) Step  { return Step{Kinds: k} }
func opt(k ...op.Kind) Step  { return Step{Kinds: k, Opt: true} }
func many(k ...op.Kind) Step { return Step{Kinds: k, Opt: true, Many: true} }

var (
	createSteps = map[ReqKind][]Step{
		CreatePlain:        {one(op.KCreateFS, op.KCreateZvol), one(op.KCommit)},
		CreateFromSnapshot: {one(op.KClone), opt(op.KSet), one(op.KCommit)},
		CreateFromVolume:   {opt(op.KCutCloneSource), one(op.KClone), opt(op.KSet), one(op.KCommit)},
	}
	resumeSteps = []Step{opt(op.KSet), opt(op.KSet), one(op.KCommit)}
	// lifecycle-fsm R1: release the clone source first (zfs keeps it while
	// this clone lives), destroy the judged snapshots, then the volume
	// without -r
	deleteSteps = []Step{opt(op.KReleaseOrigin), many(op.KDestroySnap), one(op.KDestroyVol)}
	createErrs  = []code.Code{code.NotFound, code.InvalidArgument, code.OutOfRange, code.Unavailable} // Unavailable: own clone source released and still pinned
	deleteErrs  = []code.Code{code.FailedPrecondition}
)

// Table is the lifecycle of a volume id under the parent, as data.
var Table = func() (t [world.NStates][NReq]Cell) {
	for _, rk := range []ReqKind{CreatePlain, CreateFromSnapshot, CreateFromVolume} {
		t[world.Absent][rk] = Cell{Steps: createSteps[rk], To: world.Committed,
			Mid: []world.State{world.Absent, world.Uncommitted}, Errs: append([]code.Code{code.AlreadyExists}, createErrs...)}
		t[world.Foreign][rk] = Cell{Refuse: code.AlreadyExists, Why: "exists and is not managed by this endpoint"}
		t[world.Invalid][rk] = Cell{Refuse: code.AlreadyExists, Why: "exists with inconsistent ownership markers"}
		t[world.Uncommitted][rk] = Cell{Steps: resumeSteps, To: world.Committed,
			Mid: []world.State{world.Uncommitted}, Errs: append([]code.Code{code.AlreadyExists}, createErrs...)}
		t[world.Committed][rk] = Cell{To: world.Committed, Errs: []code.Code{code.AlreadyExists}}
	}
	t[world.Absent][Delete] = Cell{To: world.Absent}
	t[world.Foreign][Delete] = Cell{Refuse: code.FailedPrecondition, Why: "is not managed by this endpoint"}
	t[world.Invalid][Delete] = Cell{Refuse: code.FailedPrecondition, Why: "has inconsistent ownership markers"}
	t[world.Uncommitted][Delete] = Cell{Steps: deleteSteps, To: world.Absent, Mid: []world.State{world.Uncommitted}, Errs: deleteErrs}
	t[world.Committed][Delete] = Cell{Steps: deleteSteps, To: world.Absent, Mid: []world.State{world.Committed}, Errs: deleteErrs}
	return t
}()

// Defined reports whether a cell was filled in (the zero Cell is not:
// it has no refusal and its To is Absent with no steps, which only
// Absent×Delete legitimately is).
func (c Cell) Defined(from world.State, rk ReqKind) bool {
	return c.Refuse != code.OK || len(c.Steps) > 0 || c.To != world.Absent || (from == world.Absent && rk == Delete)
}

// Conforms reports whether kinds is a plan the cell allows.
func (c Cell) Conforms(kinds []op.Kind) bool {
	i := 0
	for _, s := range c.Steps {
		n := 0
		for i < len(kinds) && slices.Contains(s.Kinds, kinds[i]) && (n == 0 || s.Many) {
			i, n = i+1, n+1
		}
		if n == 0 && !s.Opt {
			return false
		}
	}
	return i == len(kinds)
}

// check is the runtime half of the table: a decision outside its cell is a
// bug, refused as Internal before anything runs.
func check(c Cell, p op.Plan, r *code.Refusal) (op.Plan, *code.Refusal) {
	if r != nil {
		if c.Refuse == code.OK && !slices.Contains(c.Errs, r.Code) {
			return nil, code.Refuse(code.Internal, "decision refused %v, outside its cell (allowed %v): %s", r.Code, c.Errs, r.Why)
		}
		return nil, r
	}
	if c.Refuse != code.OK {
		return nil, code.Refuse(code.Internal, "decision planned where its cell refuses")
	}
	if !c.Conforms(p.Kinds()) {
		return nil, code.Refuse(code.Internal, "plan %v outside its cell %v", p.Kinds(), fmt.Sprint(c.Steps))
	}
	return p, nil
}
