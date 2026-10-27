package endpoint

// Deletion never uses `destroy -r` (adv4 F5; wave-5 lifecycle FSM): the
// volume's own snapshots are destroyed by name, then the volume plainly,
// so zfs refuses on anything the plan did not judge.

import (
	"context"
	"errors"
	"testing"

	"github.com/wommy/empty/internal/zfs"
)

// hookRunner runs before() outside the simulator's lock, so the hook can
// itself issue zfs commands: an out-of-band writer racing the endpoint.
type hookRunner struct {
	inner  zfs.Runner
	before func(args []string)
}

func (h *hookRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if h.before != nil {
		h.before(args)
	}
	return h.inner.Run(ctx, name, args...)
}

// F5: a dataset someone made below a filesystem volume is invisible to the
// scan; `destroy -r` took it. Reconcile now leaves it, and says so.
func TestAdv4ChildDatasetsDestroyed(t *testing.T) {
	h := newA4(t)
	ctx := context.Background()
	h.vol("keep", true)
	h.vol("old", true)
	h.must(h.z.CreateFilesystem(ctx, a4Parent+"/old/someone-elses", nil, false))
	h.must(h.z.Snapshot(ctx, a4Parent+"/old/someone-elses@s", nil))
	p, err := h.ep.Reconcile(ctx, ReconcileRequest{DesiredVolumeIDs: []string{"keep"}})
	h.must(err)
	if !h.has(a4Parent+"/old/someone-elses@s") || !h.has(a4Parent+"/old") {
		t.Fatalf("unowned child dataset destroyed: %+v", p)
	}
	// the scan sees the child, so the volume is refused at plan time and
	// reported against it, and nothing ran: no destroy zfs refuses on
	// every run (OWN-12), so a second reconcile plans nothing either
	want := "volume old has datasets below it the endpoint did not make: old/someone-elses"
	if len(p.Actions) != 0 || len(p.Skipped) != 1 || p.Skipped[0].Target != "old" || p.Skipped[0].Reason != want {
		t.Fatalf("plan %+v", p)
	}
	again, err := h.ep.Reconcile(ctx, ReconcileRequest{DesiredVolumeIDs: []string{"keep"}, DryRun: true})
	h.must(err)
	if len(again.Actions) != 0 {
		t.Fatalf("reconcile twice: %+v", again)
	}
}

// HOLD-REPLAN: a user hold on a snapshot of an undesired volume (a backup
// tool pinning it for send) makes zfs refuse its plain destroy on every
// run. The scan reads userrefs, so the volume is reported, not planned; the
// stamped orphan clone source is marked with -d, which zfs accepts on a
// held snapshot. A second reconcile plans nothing, and once released the
// next one collects the volume.
func TestReconcileReportsHeldSnapshots(t *testing.T) {
	h := newA4(t)
	ctx := context.Background()
	h.vol("keep", true)
	h.vol("old", true)
	src := a4Parent + "/old@volume-source-for-volume-gone"
	h.must(h.z.Snapshot(ctx, src, map[string]string{h.n.managed(): "true", h.n.cloneSourceFor(): "gone"}))
	zfsRun := func(args ...string) {
		_, err := h.sim.Run(ctx, "zfs", args...)
		h.must(err)
	}
	zfsRun("hold", "backup", src)
	p, err := h.ep.Reconcile(ctx, ReconcileRequest{DesiredVolumeIDs: []string{"keep"}})
	h.must(err)
	acts, skips := summary(p)
	want := "old:volume old has held snapshot old@volume-source-for-volume-gone (1 user hold): zfs release it first"
	if len(acts) != 1 || acts[0] != "old@volume-source-for-volume-gone:"+ReasonOrphanCloneSource || len(skips) != 1 || skips[0] != want {
		t.Fatalf("first run: actions %q skipped %q", acts, skips)
	}
	if !h.has(src) || !h.has(a4Parent+"/old") {
		t.Fatal("a held snapshot or its volume was destroyed")
	}
	again, err := h.ep.Reconcile(ctx, ReconcileRequest{DesiredVolumeIDs: []string{"keep"}, DryRun: true})
	h.must(err)
	if acts, skips := summary(again); len(acts) != 0 || len(skips) != 1 || skips[0] != want {
		t.Fatalf("reconcile twice: actions %q skipped %q", acts, skips)
	}
	// the last release reaps the deferred snapshot; the volume goes next
	zfsRun("release", "backup", src)
	if h.has(src) {
		t.Fatal("release of a deferred snapshot's last hold did not destroy it")
	}
	p, err = h.ep.Reconcile(ctx, ReconcileRequest{DesiredVolumeIDs: []string{"keep"}})
	h.must(err)
	if acts, skips := summary(p); len(acts) != 1 || acts[0] != "old:"+ReasonUndesired || len(skips) != 0 || h.has(a4Parent+"/old") {
		t.Fatalf("after release: actions %q skipped %q", acts, skips)
	}
}

// DeleteVolume: the refusal is zfs's own, FailedPrecondition, and nothing
// below the volume is touched.
func TestDeleteVolumeSparesChildDatasets(t *testing.T) {
	h := newA4(t)
	ctx := context.Background()
	h.vol("old", true)
	h.must(h.z.CreateFilesystem(ctx, a4Parent+"/old/child", nil, false))
	err := h.ep.DeleteVolume(ctx, "old")
	if CodeOf(err) != FailedPrecondition || !h.has(a4Parent+"/old/child") {
		t.Fatalf("DeleteVolume over a foreign child: %v", err)
	}
}

// FSM counterexample 2: a snapshot cut between the scan and the destroy
// (sanoid) is not destroyed with the volume.
func TestDeleteVolumeSparesSnapshotCutAfterScan(t *testing.T) {
	h := newA4(t)
	ctx := context.Background()
	h.vol("v", true)
	cut := false
	hr := &hookRunner{inner: h.sim, before: func(args []string) {
		if !cut && args[0] == "destroy" {
			cut = true
			h.must(h.z.Snapshot(ctx, a4Parent+"/v@autosnap", nil))
		}
	}}
	ep, err := New(zfs.New(hr), h.ep.cfg)
	h.must(err)
	if err := ep.DeleteVolume(ctx, "v"); CodeOf(err) != FailedPrecondition {
		t.Fatalf("DeleteVolume: %v", err)
	}
	if !h.has(a4Parent + "/v@autosnap") {
		t.Fatal("a snapshot cut after the scan was destroyed")
	}
}

// Crash window (wave7 suite): CreateVolume(c from a volume) cut the
// clone-source snapshot and died before the clone; the caller gave up and
// deleted c. The delete of the absent c releases the snapshot instead of
// leaving it for Reconcile, and a repeat is still OK.
func TestDeleteOfAbsentCloneReleasesItsCloneSource(t *testing.T) {
	t.Parallel()
	h := newA4(t)
	ctx := context.Background()
	h.vol("a", true)
	h.sim.Fail = func(args []string) error {
		if args[0] == "clone" {
			return errors.New("crash")
		}
		return nil
	}
	if _, err := h.ep.CreateVolume(ctx, CreateVolumeRequest{Name: "c", SourceVolumeID: "a"}); err == nil {
		t.Fatal("want the injected crash")
	}
	h.sim.Fail = nil
	origin := a4Parent + "/a@" + cloneSourceSnapshotPrefix + "c"
	if !h.has(origin) || h.has(a4Parent+"/c") {
		t.Fatalf("setup: origin=%v clone=%v", h.has(origin), h.has(a4Parent+"/c"))
	}
	for i := range 2 {
		h.must(h.ep.DeleteVolume(ctx, "c"))
		if h.has(origin) {
			t.Fatalf("delete %d: clone-source snapshot left behind", i)
		}
	}
	if !h.has(a4Parent + "/a") {
		t.Fatal("source volume destroyed")
	}
}

// FSM counterexample 1: a DeleteVolume of a clone interrupted after its
// first op, then retried, must not orphan the clone-source snapshot. The
// origin is released (deferred) first, so zfs reaps it with the clone.
func TestDeleteCloneInterruptedThenRetriedReleasesOrigin(t *testing.T) {
	h := newA4(t)
	ctx := context.Background()
	h.vol("a", true)
	if _, err := h.ep.CreateVolume(ctx, CreateVolumeRequest{Name: "b", SourceVolumeID: "a"}); err != nil {
		t.Fatal(err)
	}
	origin := a4Parent + "/a@" + cloneSourceSnapshotPrefix + "b"
	for crashAfter := 1; ; crashAfter++ {
		n := 0
		h.sim.Fail = func(args []string) error {
			if args[0] != "destroy" {
				return nil
			}
			if n++; n > crashAfter {
				return errors.New("crash")
			}
			return nil
		}
		err := h.ep.DeleteVolume(ctx, "b")
		h.sim.Fail = nil
		if err == nil {
			break
		}
		h.must(h.ep.DeleteVolume(ctx, "b")) // the retry
		if h.has(origin) || h.has(a4Parent+"/b") {
			t.Fatalf("crash after %d destroys: retry OK but origin=%v clone=%v", crashAfter, h.has(origin), h.has(a4Parent+"/b"))
		}
		return
	}
}
