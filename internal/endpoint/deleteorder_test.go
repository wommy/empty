package endpoint

// Deletion never uses `destroy -r` (adv4 F5; wave-5 lifecycle FSM): the
// volume's own snapshots are destroyed by name, then the volume plainly,
// so zfs refuses on anything the plan did not judge.

import (
	"context"
	"errors"
	"strings"
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
	// zfs's refusal is reported against the volume, and nothing ran
	if len(p.Actions) != 0 || len(p.Skipped) != 1 || p.Skipped[0].Target != "old" ||
		!strings.HasPrefix(p.Skipped[0].Reason, SkipFailed+": ") || !strings.Contains(p.Skipped[0].Reason, "has children") {
		t.Fatalf("plan %+v", p)
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
