package endpoint

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// Item 7 (wave22): rootErr maps a failed OpSetRoot. A classified *Error
// (LocalRoot's) keeps its code wherever it ran; the caller's deadline is
// DeadlineExceeded and its cancellation Aborted; anything else is
// Internal on the plan's first op and Aborted once an earlier op applied,
// as opErr maps an unclassified zfs failure.
func TestRootErrMapping(t *testing.T) {
	t.Parallel()
	plan := Plan{{Kind: OpCreateFS, Target: "tank/v/a"}, {Kind: OpSetRoot, Target: "tank/v/a"}}
	root := Plan{{Kind: OpSetRoot, Target: "tank/v/a"}}
	plain := errors.New("x")
	for _, tc := range []struct {
		name string
		err  error
		i    int
		plan Plan
		code Code
		msg  string
	}{
		{"classified, first op", &Error{Code: FailedPrecondition, Msg: "read-only"}, 0, root, FailedPrecondition, "op 1/1 set-root tank/v/a: read-only"},
		{"classified, after a change", &Error{Code: FailedPrecondition, Msg: "read-only"}, 1, plan, FailedPrecondition, "op 2/2"},
		{"deadline", context.DeadlineExceeded, 0, root, DeadlineExceeded, "op 1/1"},
		{"deadline, after a change", context.DeadlineExceeded, 1, plan, DeadlineExceeded, "op 2/2"},
		{"canceled", context.Canceled, 0, root, Aborted, "op 1/1"},
		{"unclassified, first op", plain, 0, root, Internal, "op 1/1 set-root tank/v/a"},
		{"unclassified, after a change", plain, 1, plan, Aborted, "earlier ops applied"},
	} {
		got := rootErr(tc.err, tc.i, tc.plan)
		if CodeOf(got) != tc.code || !strings.Contains(got.Error(), tc.msg) {
			t.Errorf("%s: %v (code %d), want code %d with %q", tc.name, got, CodeOf(got), tc.code, tc.msg)
		}
		var e *Error
		if !errors.As(tc.err, &e) && !errors.Is(got, tc.err) {
			t.Errorf("%s: %v does not wrap %v", tc.name, got, tc.err)
		}
	}
	// a resumed provision ran its create on an earlier attempt: there the
	// first op's unclassified failure is after a change too
	if got := resumeErr(rootErr(plain, 0, root)); CodeOf(got) != Aborted || !errors.Is(got, plain) {
		t.Errorf("unclassified, resumed: %v (code %d), want Aborted", got, CodeOf(got))
	}
}

// Item 8 (wave22): a Reconcile run whose context ends mid-collection (the
// drain: Daemon.Shutdown cancels handler contexts) destroys nothing more.
// The group that ran is reported, every later group is skipped as not
// attempted, and its volume is still there.
func TestReconcileCancelledMidRunStops(t *testing.T) {
	h := newA4(t)
	h.vol("keep", true)
	for _, id := range []string{"a", "b", "c"} {
		h.vol(id, true)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := digested(ctx, t, h.ep, ReconcileRequest{DesiredVolumeIDs: []string{"keep"}})
	destroys := 0
	h.sim.Fail = func(a []string) error {
		if a[0] == "destroy" {
			// shut down while the first group's destroy runs
			destroys++
			cancel()
		}
		return nil
	}
	p, err := h.ep.Reconcile(ctx, r)
	h.must(err)
	if destroys != 1 || len(p.Actions) != 1 {
		t.Fatalf("%d destroys, actions %+v: want the first group only", destroys, p.Actions)
	}
	gone := p.Actions[0].Target
	var skipped []string
	for _, s := range p.Skipped {
		if !strings.HasPrefix(s.Reason, SkipNotAttempted+": ") {
			t.Fatalf("skip %+v, want %q", s, SkipNotAttempted)
		}
		skipped = append(skipped, s.Target)
	}
	if len(skipped) != 2 || slices.Contains(skipped, gone) {
		t.Fatalf("skipped %v after %s, want the other two", skipped, gone)
	}
	for _, id := range skipped {
		if !h.has(a4Parent + "/" + id) {
			t.Fatalf("%s destroyed after the cancel", id)
		}
	}
}

// Reconcile re-scans and re-plans under the volume locks; a failed re-scan
// fails the run with nothing destroyed.
func TestReconcileRescanFailureDestroysNothing(t *testing.T) {
	h := newA4(t)
	ctx := context.Background()
	h.vol("keep", true)
	h.vol("a", true)
	r := digested(ctx, t, h.ep, ReconcileRequest{DesiredVolumeIDs: []string{"keep"}})
	scans := 0
	failed := errors.New("injected: re-scan failed")
	h.sim.Fail = func(a []string) error {
		if a[0] == "get" && slices.Contains(a, "-d") {
			if scans++; scans == 2 {
				return failed
			}
		}
		return nil
	}
	_, err := h.ep.Reconcile(ctx, r)
	if !errors.Is(err, failed) {
		t.Fatalf("re-scan failure not reported: %v", err)
	}
	if scans != 2 || !h.has(a4Parent+"/a") {
		t.Fatalf("%d scans; a destroyed: %v", scans, !h.has(a4Parent+"/a"))
	}
}
