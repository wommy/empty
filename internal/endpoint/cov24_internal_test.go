package endpoint

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/wommy/empty/internal/zfs"
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
		// LocalRoot's own Internal is no classification: it is an
		// unclassified failure, as a bare error is
		{"Internal, first op", &Error{Code: Internal, Msg: "open"}, 0, root, Internal, "op 1/1 set-root tank/v/a"},
		{"Internal, after a change", &Error{Code: Internal, Msg: "open"}, 1, plan, Aborted, "earlier ops applied"},
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
	internal := &Error{Code: Internal, Msg: "open", Err: plain}
	if got := resumeErr(rootErr(internal, 0, root)); CodeOf(got) != Aborted || !errors.Is(got, plain) {
		t.Errorf("Internal, resumed: %v (code %d), want Aborted", got, CodeOf(got))
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

// Item 10 (wave22): DeleteVolume's lock passes are bounded. A clone's
// source held by another call through the whole delete is the lock error
// of the second pass (Aborted, after LockWait). A delete that names a new
// busy volume on each of its three passes (here: a stale clone-source
// snapshot for it appears on another, busy, volume while it waits) gives
// up Aborted "kept changing" rather than looping. Both leave the clone.
// LockWait runs on the synctest bubble's fake clock.
func TestDeleteVolumeLockPassesAreBounded(t *testing.T) {
	ctx := context.Background()
	t.Run("source held throughout", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ep, sim := cloneFixture(t, func(r zfs.Runner) zfs.Runner { return r })
			ep.cfg.LockWait = time.Minute
			release, ok := ep.locks.TryAcquire("vol:src")
			if !ok {
				t.Fatal("vol:src busy")
			}
			defer release()
			err := ep.DeleteVolume(ctx, "clone")
			if CodeOf(err) != Aborted || !strings.Contains(err.Error(), "operation pending") {
				t.Fatalf("DeleteVolume(clone) with vol:src held: %v, want the lock wait's Aborted", err)
			}
			if !sim.Has("tank/v/clone") {
				t.Fatal("clone destroyed")
			}
		})
	})
	t.Run("a new busy volume each pass", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ep, sim := cloneFixture(t, func(r zfs.Runner) zfs.Runner { return r })
			ep.cfg.LockWait = time.Minute
			z := zfs.New(sim)
			for _, id := range []string{"s2", "s3"} {
				if _, err := ep.CreateVolume(ctx, CreateVolumeRequest{Name: id, CapacityBytes: 10 << 20}); err != nil {
					t.Fatal(err)
				}
			}
			stamp := map[string]string{ep.names.managed(): "true", ep.names.cloneSourceFor(): "clone"}
			held := map[string]func(){}
			hold := func(v string) {
				r, ok := ep.locks.TryAcquire("vol:" + v)
				if !ok {
					t.Fatalf("vol:%s busy", v)
				}
				held[v] = r
			}
			hold("src")
			done := make(chan error, 1)
			go func() { done <- ep.DeleteVolume(ctx, "clone") }()
			// each pass waits for the volume the previous one found busy;
			// meanwhile another busy volume gains a source snapshot for clone
			for _, next := range []string{"s2", "s3"} {
				synctest.Wait()
				if err := z.Snapshot(ctx, "tank/v/"+next+"@"+cloneSourceSnapshotPrefix+"clone", stamp); err != nil {
					t.Fatal(err)
				}
				hold(next)
				for v, r := range held {
					if v != next {
						r()
						delete(held, v)
					}
				}
			}
			err := <-done
			if CodeOf(err) != Aborted || !strings.Contains(err.Error(), "kept changing") {
				t.Fatalf("DeleteVolume(clone): %v, want Aborted after three passes", err)
			}
			held["s3"]()
			if !sim.Has("tank/v/clone") {
				t.Fatal("clone destroyed")
			}
		})
	})
}

// Item 10 (wave22): the wait for a whole-parent scan slot (maxWholeScans,
// the namespace-deletion memory bound) ends with the caller's context:
// DeadlineExceeded on its deadline, Aborted on a cancel.
func TestWholeScanSlotWaitEndsWithTheContext(t *testing.T) {
	t.Parallel()
	h := newA4(t)
	for range maxWholeScans {
		h.ep.scans <- struct{}{}
	}
	expired, cancel1 := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancel1()
	if _, _, err := h.ep.ListVolumes(expired, 0, ""); CodeOf(err) != DeadlineExceeded || !strings.Contains(err.Error(), "scan slot") {
		t.Fatalf("expired: %v, want DeadlineExceeded waiting for a slot", err)
	}
	cancelled, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if _, _, err := h.ep.ListVolumes(cancelled, 0, ""); CodeOf(err) != Aborted || !strings.Contains(err.Error(), "scan slot") {
		t.Fatalf("cancelled: %v, want Aborted waiting for a slot", err)
	}
	<-h.ep.scans
	if _, _, err := h.ep.ListVolumes(context.Background(), 0, ""); err != nil {
		t.Fatalf("with a slot free: %v", err)
	}
}
