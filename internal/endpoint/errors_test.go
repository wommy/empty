package endpoint

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/wommy/empty/internal/zfs"
)

// TestZfsErrCodes: rows of the error table (errRules). Each zfs failure
// maps to the code the CSI sidecars need: retryable for states that clear
// by themselves (busy, a pool that went read-only or away, a killed
// command whose outcome is unknown), final for ones only an administrator
// or the caller can change.
func TestZfsErrCodes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		op      OpKind
		err     error
		mutated bool
		want    Code
	}{
		{opRead, &zfs.Error{Kind: zfs.KindNotFound, Op: "get", Dataset: "tank/a"}, false, NotFound},
		{opRead, &zfs.Error{Kind: zfs.KindParentNotFound}, false, NotFound},
		{opRead, &zfs.Error{Kind: zfs.KindExists}, false, AlreadyExists},
		{opRead, &zfs.Error{Kind: zfs.KindBusy}, false, Unavailable},
		{OpCreateVol, &zfs.Error{Kind: zfs.KindBusy}, false, Unavailable},
		{OpSet, &zfs.Error{Kind: zfs.KindBusy}, true, Unavailable},
		// a volume or snapshot in use on DeleteVolume/DeleteSnapshot
		{OpDestroy, &zfs.Error{Kind: zfs.KindBusy}, false, FailedPrecondition},
		{OpDestroy, &zfs.Error{Kind: zfs.KindBusy}, true, FailedPrecondition},
		{opRead, &zfs.Error{Kind: zfs.KindHasClones}, false, FailedPrecondition},
		{opRead, &zfs.Error{Kind: zfs.KindHasChildren}, false, FailedPrecondition},
		{opRead, &zfs.Error{Kind: zfs.KindPrecondition}, false, FailedPrecondition},
		{opRead, &zfs.Error{Kind: zfs.KindPermission}, false, FailedPrecondition},
		{opRead, &zfs.Error{Kind: zfs.KindUnavailable}, false, Unavailable},
		{OpDestroy, &zfs.Error{Kind: zfs.KindUnavailable}, true, Unavailable},
		{opRead, &zfs.Error{Kind: zfs.KindInvalidProperty}, false, InvalidArgument},
		{opRead, &zfs.Error{Kind: zfs.KindNoSpace}, false, ResourceExhausted},
		{opRead, &zfs.Error{Kind: zfs.KindInterrupted, Err: fmt.Errorf("run: %w", context.DeadlineExceeded)}, false, DeadlineExceeded},
		{opRead, &zfs.Error{Kind: zfs.KindInterrupted, Err: fmt.Errorf("run: %w", context.Canceled)}, false, Aborted},
		{OpClone, &zfs.Error{Kind: zfs.KindNotFound}, true, NotFound},
		// unclassified: final before any change, retryable after one
		{opRead, &zfs.Error{Kind: zfs.KindUnknown}, false, Internal},
		// a read after a change: the change is what a retry must finish
		{opRead, &zfs.Error{Kind: zfs.KindUnknown}, true, Aborted},
		{opReadParent, &zfs.Error{Kind: zfs.KindUnknown}, true, Aborted},
		{OpCreateFS, &zfs.Error{Kind: zfs.KindUnknown}, false, Internal},
		{OpCreateFS, &zfs.Error{Kind: zfs.KindUnknown}, true, Aborted},
		{OpSet, errors.New("not a zfs error"), true, Aborted},
		{OpSet, errors.New("not a zfs error"), false, Internal},
	} {
		if got := CodeOf(zfsFail(tc.op, tc.err, tc.mutated, "op")); got != tc.want {
			t.Errorf("%v %v mutated=%v: code %d, want %d", tc.op, tc.err, tc.mutated, got, tc.want)
		}
	}
	if err := zfsFail(OpSet, nil, true, "op"); err != nil {
		t.Errorf("zfsFail(nil) = %v", err) // nil in, nil out: never a failure made up
	}
	if err := zfsErr(nil, "op"); err != nil {
		t.Errorf("zfsErr(nil) = %v", err)
	}
}

// TestErrTableMatchesHead pins the error table cell for cell to the
// mapping it replaced (the head* functions below, frozen copies of
// zfsErr, applyN's error switch, opErr, resumeErr, parentUnavailable and
// the scan's and GetCapacity's not-found branches at 7f5637f), for every
// (op, kind, mutated), through every production path: the table itself,
// zfsErr, opErr, resumeErr and parentUnavailable, codes and messages.
func TestErrTableMatchesHead(t *testing.T) {
	t.Parallel()
	for _, d := range diffErrTable(&errTable) {
		t.Error(d)
	}
	for _, c := range allErrCells() {
		for _, err := range errsOfKind(c.kind) {
			var got, want error
			switch {
			case c.op == opRead:
				got, want = zfsFail(opRead, err, c.mutated, "scan tank"), sinceHead(c, headZfsErr(err, "scan tank"), "")
				if !c.mutated {
					got = zfsErr(err, "scan tank")
				}
			case c.op == opReadParent:
				got, want = zfsFail(opReadParent, err, c.mutated, "scan tank"), sinceHead(c, headReadParent(err, "scan tank"), "")
				if zfs.IsNotFound(err) {
					g, w := parentUnavailable("tank", err), headParentUnavailable("tank", err)
					if CodeOf(g) != CodeOf(w) || g.Error() != w.Error() {
						t.Errorf("%+v %v parentUnavailable: got %d %q, head %d %q", c, err, CodeOf(g), g, CodeOf(w), w)
					}
				}
			default:
				plan, i := planAt(c)
				got, want = opErr(err, i, plan), headApplyErr(err, i, plan)
				// a resumed provision: an earlier attempt changed the pool
				first, _ := planAt(errCell{op: c.op})
				g, w := resumeErr(opErr(err, 0, first)), headResumeErr(headApplyErr(err, 0, first))
				if CodeOf(g) != CodeOf(w) || g.Error() != w.Error() {
					t.Errorf("%+v %v resumed: got %d %q, head %d %q", c, err, CodeOf(g), g, CodeOf(w), w)
				}
			}
			if CodeOf(got) != CodeOf(want) || got.Error() != want.Error() {
				t.Errorf("%+v %v: got %d %q, head %d %q", c, err, CodeOf(got), got, CodeOf(want), want)
			}
			if e := got.(*Error); e.Code != CodeOf(got) {
				t.Errorf("%+v %v: Code field %d, CodeOf %d", c, err, e.Code, CodeOf(got))
			}
		}
	}
}

// The comparison sees a change to any one cell.
func TestErrTableMatchesHeadSeesEveryCell(t *testing.T) {
	t.Parallel()
	for _, c := range allErrCells() {
		tbl := errTable
		m := 0
		if c.mutated {
			m = 1
		}
		was := tbl[c.op][c.kind][m]
		tbl[c.op][c.kind][m] = Internal
		if was == Internal {
			tbl[c.op][c.kind][m] = Aborted
		}
		if len(diffErrTable(&tbl)) != 1 {
			t.Errorf("cell %+v changed: diff %v, want exactly that cell", c, diffErrTable(&tbl))
		}
	}
}

// Every cell is set, and the ops and kinds cover what apply and zfs make.
func TestErrTableIsTotal(t *testing.T) {
	t.Parallel()
	for _, c := range allErrCells() {
		if c.code() == OK {
			t.Errorf("%+v: OK", c)
		}
	}
	for k := range opNames {
		if int(k) >= nErrOps || !slicesContains(mutatingOps, k) {
			t.Errorf("op %v is not in the error table", k)
		}
	}
	for _, k := range []OpKind{opRead, opReadParent} {
		if _, ok := opNames[k]; ok || slicesContains(mutatingOps, k) {
			t.Errorf("read op %d is an apply op: a new OpKind took its value", k)
		}
	}
	if s := zfs.Kind(zfs.NumKinds).String(); s != "unknown" {
		t.Errorf("zfs.NumKinds = %d is a named kind %q: the table misses it", zfs.NumKinds, s)
	}
	defer func() {
		if recover() == nil {
			t.Error("a table with an uncovered cell expanded")
		}
	}()
	expandErrRules(errRules[:len(errRules)-1])
}

func slicesContains(s []OpKind, k OpKind) bool {
	for _, x := range s {
		if x == k {
			return true
		}
	}
	return false
}

func allErrCells() []errCell {
	var cs []errCell
	for op := range nErrOps {
		for k := range nErrKinds {
			for _, m := range []bool{false, true} {
				cs = append(cs, errCell{OpKind(op), errKind(k), m})
			}
		}
	}
	return cs
}

// errsOfKind are the errors whose kind is k: for unknown, an unclassified
// zfs failure and an error that is no zfs failure at all.
func errsOfKind(k errKind) []error {
	switch {
	case k == kindDeadline:
		return []error{&zfs.Error{Kind: zfs.KindInterrupted, Op: "x", Dataset: "tank/a", Err: fmt.Errorf("run: %w", context.DeadlineExceeded)}}
	case zfs.Kind(k) == zfs.KindInterrupted:
		return []error{&zfs.Error{Kind: zfs.KindInterrupted, Op: "x", Dataset: "tank/a", Err: fmt.Errorf("run: %w", context.Canceled)}}
	case zfs.Kind(k) == zfs.KindUnknown:
		return []error{&zfs.Error{Kind: zfs.KindUnknown, Op: "x", Dataset: "tank/a", Stderr: "boom"}, errors.New("boom")}
	}
	return []error{&zfs.Error{Kind: zfs.Kind(k), Op: "x", Dataset: "tank/a", Stderr: "e"}}
}

// diffErrTable lists the cells of tbl that differ from the HEAD mapping.
func diffErrTable(tbl *[nErrOps][nErrKinds][2]Code) []string {
	var d []string
	head := headTable()
	for _, c := range allErrCells() {
		m := 0
		if c.mutated {
			m = 1
		}
		if got, want := tbl[c.op][c.kind][m], head[c.op][c.kind][m]; got != want {
			d = append(d, fmt.Sprintf("cell %+v: table %d, head %d", c, got, want))
		}
	}
	return d
}

// headTable is the HEAD mapping as a table, every cell by headCode.
var headTable = sync.OnceValue(func() (t [nErrOps][nErrKinds][2]Code) {
	for _, c := range allErrCells() {
		m := 0
		if c.mutated {
			m = 1
		}
		t[c.op][c.kind][m] = headCode(c)
	}
	return t
})

// changedSinceHead are the cells changed on purpose since 7f5637f. The
// rule is "an unclassified failure after a change is Aborted"; wave 25
// widened it to reads (a scan or get after a change: what a retry must
// finish is the change, whatever op failed).
func changedSinceHead(c errCell) bool {
	return c.mutated && c.kind == errKind(zfs.KindUnknown) && (c.op == opRead || c.op == opReadParent)
}

// sinceHead is the head mapping err of the cell c, amended where c changed
// on purpose since (changedSinceHead): Aborted, its message extended by
// because, as remapMutated does.
func sinceHead(c errCell, err error, because string) error {
	if !changedSinceHead(c) {
		return err
	}
	e := err.(*Error)
	return &Error{Code: Aborted, Msg: e.Msg + because, Err: e.Err}
}

// headCode is the code HEAD gave the cell c, amended by changedSinceHead.
func headCode(c errCell) Code {
	if changedSinceHead(c) {
		return Aborted
	}
	err := errsOfKind(c.kind)[0]
	switch c.op {
	case opRead:
		return CodeOf(headZfsErr(err, ""))
	case opReadParent:
		return CodeOf(headReadParent(err, ""))
	}
	plan, i := planAt(c)
	return CodeOf(headApplyErr(err, i, plan))
}

// planAt is a plan whose op i is c.op: the first op, or the second after a
// set when c is after a change.
func planAt(c errCell) (Plan, int) {
	op := Op{Kind: c.op, Target: "tank/b", Source: "tank/a@s"}
	if c.mutated {
		return Plan{{Kind: OpSet, Target: "tank/a"}, op}, 1
	}
	return Plan{op, {Kind: OpSet, Target: "tank/a"}}, 0
}

// headZfsErr is zfsErr at 7f5637f, verbatim. Do not edit: it is the
// reference the table is pinned to.
func headZfsErr(err error, what string) error {
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
		errors.Is(err, zfs.ErrPermission):
		code = FailedPrecondition
	case errors.Is(err, zfs.ErrBusy):
		code = Unavailable
	case errors.Is(err, zfs.ErrInterrupted):
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

// headApplyErr is applyN's error switch (its failing cases; the
// tolerances decide whether there is a failure at all, not its code) and
// opErr at 7f5637f, verbatim.
func headApplyErr(err error, i int, plan Plan) error {
	op := plan[i]
	if op.Kind == OpClone && zfs.IsNotFound(err) {
		return &Error{Code: NotFound, Msg: "clone source " + op.Source + " not found", Err: err}
	}
	e := headZfsErr(err, fmt.Sprintf("op %d/%d %s %s", i+1, len(plan), op.Kind, op.Target)).(*Error)
	switch {
	case op.Kind == OpDestroy && errors.Is(err, zfs.ErrBusy):
		e.Code = FailedPrecondition
	case e.Code == Internal && i > 0:
		e.Code = Aborted
		e.Msg += " (earlier ops applied; a retry converges)"
	}
	return e
}

// headReadParent is a read of the parent at 7f5637f: the scan of the
// whole parent and GetCapacity sent not-found to parentUnavailable, the
// rest to zfsErr.
func headReadParent(err error, what string) error {
	if zfs.IsNotFound(err) {
		return &Error{Code: Unavailable, Msg: what, Err: err}
	}
	return headZfsErr(err, what)
}

// headParentUnavailable is parentUnavailable at 7f5637f, verbatim.
func headParentUnavailable(parent string, err error) error {
	return &Error{Code: Unavailable, Msg: "parent dataset " + parent + " is unavailable", Err: err}
}

// headResumeErr is resumeErr at 7f5637f, verbatim.
func headResumeErr(err error) error {
	var e *Error
	if errors.As(err, &e) && e.Code == Internal && e.Err != nil {
		return &Error{Code: Aborted, Msg: e.Msg + " (resuming an earlier attempt; a retry converges)", Err: e.Err}
	}
	return err
}

// The message carries its cause: operators read it in the CSI events.
func TestErrorText(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		err  *Error
		want string
	}{
		{&Error{Code: NotFound, Msg: "m", Err: fmt.Errorf("cause")}, "m: cause"},
		{&Error{Code: NotFound, Msg: "m"}, "m"},
	} {
		if got := tc.err.Error(); got != tc.want {
			t.Errorf("Error() = %q, want %q", got, tc.want)
		}
	}
}

func TestCodeOfNeverReportsAFailureAsOK(t *testing.T) {
	if c := CodeOf(&Error{Msg: "zero code"}); c != Internal {
		t.Fatalf("non-nil *Error with zero Code: %v, want Internal", c)
	}
}

// resumeErr (wave 25 item 2): after a change, an unclassified failure is
// Aborted whatever made it (an op, a read, a scan whose output does not
// decode); a refusal made on purpose keeps its code.
func TestResumeErrKeepsRefusals(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
		want Code
	}{
		{"unclassified read", zfsErr(&zfs.Error{Kind: zfs.KindUnknown, Stderr: "boom"}, "scan"), Aborted},
		{"undecodable scan", errorf(Internal, "scan: tank/a: unparsable number %q", "x"), Aborted},
		{"read: not found", zfsErr(&zfs.Error{Kind: zfs.KindNotFound}, "scan"), NotFound},
		{"read: busy", zfsErr(&zfs.Error{Kind: zfs.KindBusy}, "scan"), Unavailable},
		{"served beyond its export", errorf(FailedPrecondition, "served beyond its export"), FailedPrecondition},
		{"source not found", errorf(NotFound, "source not found"), NotFound},
		{"not served yet", errorf(Unavailable, "not served"), Unavailable},
		{"vanished", errorf(Aborted, "vanished"), Aborted},
	} {
		got := resumeErr(tc.err)
		if CodeOf(got) != tc.want || !errors.Is(got, tc.err) && got != tc.err {
			t.Errorf("%s: %v (code %d), want code %d wrapping %v", tc.name, got, CodeOf(got), tc.want, tc.err)
		}
	}
	if resumeErr(nil) != nil {
		t.Error("resumeErr(nil) is not nil")
	}
}
