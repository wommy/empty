package decide

import (
	"context"
	"errors"

	"github.com/wommy/empty/internal/core/code"
	"github.com/wommy/empty/internal/core/op"
	"github.com/wommy/empty/internal/zfs"
)

// Class groups op kinds that fail alike.
type Class uint8

const (
	CCreate  Class = iota // create-fs, create-zvol
	CCut                  // cut-clone-source
	CClone                // clone
	CSet                  // set, commit
	CDestroy              // release-origin, destroy-snap, destroy-vol, defer-destroy-snap
	NClass
)

// ClassOf maps an op kind to its class.
func ClassOf(k op.Kind) Class {
	switch k {
	case op.KCreateFS, op.KCreateZvol:
		return CCreate
	case op.KCutCloneSource:
		return CCut
	case op.KClone:
		return CClone
	case op.KSet, op.KCommit:
		return CSet
	}
	return CDestroy
}

// ZKind extends zfs.Kind with the one distinction the codes need that the
// classifier does not make: a command killed by the caller's deadline.
type ZKind uint8

const NZKind = ZKind(zfs.KindInterrupted) + 2

// ZDeadline is KindInterrupted caused by the deadline (not a cancel).
const ZDeadline = ZKind(zfs.KindInterrupted) + 1

// Outcome of one failed op.
type Outcome struct {
	Done bool      // the op's work is already done (replay tolerance): continue
	Code code.Code // otherwise the plan stops with this code
}

// rule is one row of the error table; zero-valued selectors match anything.
type rule struct {
	class   []Class
	kind    []ZKind
	mutated []bool
	out     Outcome
}

func zk(k ...zfs.Kind) []ZKind {
	z := make([]ZKind, len(k))
	for i, x := range k {
		z[i] = ZKind(x)
	}
	return z
}

var (
	yes = []bool{true}
	no  = []bool{false}
)

// Rules is the error mapping as data. First match wins. Sources: CSI spec
// (CreateVolume/DeleteVolume error tables), type12 D01/D02/D12, CHECKPOINT
// decisions (missing parent = Unavailable, only not-found is absent).
var Rules = []rule{
	// replay tolerance: a destroy that finds nothing is done
	{class: []Class{CDestroy}, kind: zk(zfs.KindNotFound), out: Outcome{Done: true}},
	// in use, or something we did not judge appeared (a racing snapshot, a
	// child): CSI DeleteVolume "volume in use" is FAILED_PRECONDITION
	{class: []Class{CDestroy}, kind: zk(zfs.KindBusy, zfs.KindHasChildren, zfs.KindHasClones), out: Outcome{Code: code.FailedPrecondition}},
	// the clone's origin vanished after the scan: CSI "source does not exist"
	{class: []Class{CClone}, kind: zk(zfs.KindNotFound), out: Outcome{Code: code.NotFound}},
	// the clone source's name is deterministic per (source, clone): "exists"
	// at exec means the scan was stale (our own cut replayed, or a deferred
	// one still pinned). Rescan; never a final AlreadyExists (A3, ERR-10)
	{class: []Class{CCut}, kind: zk(zfs.KindExists), out: Outcome{Code: code.Aborted}},
	// a create found the name taken by someone else: never adopted (F4)
	{class: []Class{CCreate, CClone}, kind: zk(zfs.KindExists), out: Outcome{Code: code.AlreadyExists}},
	// the parent (configuration, maybe an exported pool) is gone: retry
	{kind: zk(zfs.KindParentNotFound, zfs.KindUnavailable), out: Outcome{Code: code.Unavailable}},
	// a dataset we just scanned vanished under a set: re-scan
	{class: []Class{CSet}, kind: zk(zfs.KindNotFound), out: Outcome{Code: code.Aborted}},
	{kind: zk(zfs.KindBusy), out: Outcome{Code: code.Unavailable}},
	{kind: zk(zfs.KindNoSpace), out: Outcome{Code: code.ResourceExhausted}},
	{kind: zk(zfs.KindInvalidProperty), out: Outcome{Code: code.InvalidArgument}},
	{kind: zk(zfs.KindPermission, zfs.KindPrecondition, zfs.KindHasChildren, zfs.KindHasClones, zfs.KindExists, zfs.KindNotFound), out: Outcome{Code: code.FailedPrecondition}},
	{kind: []ZKind{ZDeadline}, out: Outcome{Code: code.DeadlineExceeded}},
	{kind: zk(zfs.KindInterrupted), out: Outcome{Code: code.Aborted}},
	// unclassified: before any change nothing leaked (Internal); after one,
	// a final code would strand the half-made volume, so retryable (CX6)
	{kind: zk(zfs.KindUnknown), mutated: no, out: Outcome{Code: code.Internal}},
	{kind: zk(zfs.KindUnknown), mutated: yes, out: Outcome{Code: code.Aborted}},
}

// ErrTable is Rules expanded to every (class, kind, mutated): built once.
var ErrTable = func() (t [NClass][NZKind][2]Outcome) {
	for c := range NClass {
		for k := range NZKind {
			for m := range 2 {
				for _, r := range Rules {
					if match(r.class, c) && match(r.kind, k) && match(r.mutated, m == 1) {
						t[c][k][m] = r.out
						break
					}
				}
			}
		}
	}
	return t
}()

func match[T comparable](set []T, v T) bool {
	if len(set) == 0 {
		return true
	}
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

// Map is the outcome of op kind k failing with err after (mutated) or
// before any earlier op of its plan ran.
func Map(k op.Kind, err error, mutated bool) Outcome {
	z := ZKind(zfs.KindUnknown)
	var ze *zfs.Error
	if errors.As(err, &ze) {
		z = ZKind(ze.Kind)
	}
	if z == ZKind(zfs.KindInterrupted) && errors.Is(err, context.DeadlineExceeded) {
		z = ZDeadline
	}
	m := 0
	if mutated {
		m = 1
	}
	return ErrTable[ClassOf(k)][z][m]
}

// Runner runs zfs. Guid reads one dataset's guid (`zfs get -H -p -o value
// guid <ds>`); a missing dataset is a *zfs.Error of KindNotFound.
type Runner interface {
	Run(ctx context.Context, argv []string) error
	Guid(ctx context.Context, ds string) (uint64, error)
}

// Apply runs a plan in order. The only judgement here is the table above.
func Apply(ctx context.Context, run Runner, p op.Plan) *code.Refusal {
	for i, o := range p {
		if g, ok := o.(op.Guarded); ok {
			// rename-and-recreate between scan and destroy (OWN-15): the
			// name now holds another dataset. Missing is the op's own
			// replay tolerance, so let the destroy report it.
			ds, want := g.Guard()
			got, err := run.Guid(ctx, ds)
			var ze *zfs.Error
			switch {
			case err == nil && got != want:
				return code.Refuse(code.Aborted, "op %d/%d %s: %s is not the dataset scanned (guid %d, now %d)", i+1, len(p), o, ds, want, got)
			case err != nil && !(errors.As(err, &ze) && ze.Kind == zfs.KindNotFound):
				return code.Refuse(Map(o.Kind(), err, i > 0).Code, "op %d/%d %s: guid check: %v", i+1, len(p), o, err)
			}
		}
		err := run.Run(ctx, o.Argv())
		if err == nil {
			continue
		}
		out := Map(o.Kind(), err, i > 0)
		if out.Done && o.Tol() == op.TolMissing {
			continue
		}
		c := out.Code
		if out.Done { // tolerance the op does not declare: never silently done
			c = code.Internal
		}
		return code.Refuse(c, "op %d/%d %s: %v", i+1, len(p), o, err)
	}
	return nil
}
