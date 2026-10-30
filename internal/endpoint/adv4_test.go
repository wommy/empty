package endpoint

// Ported proofs from the adversarial Reconcile review (adv4, written
// against bdf89d0). Each asserts the property it names and failed before
// its fix.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

const (
	a4Parent = "tank/v"
	a4Old    = int64(1_000_000)
	a4Now    = a4Old + 10*3600
)

type a4 struct {
	t   *testing.T
	sim *zfstest.Sim
	z   *zfs.Client
	ep  *Endpoint
	n   props
}

func newA4(t *testing.T) *a4 {
	ctx := context.Background()
	sim := zfstest.New("tank")
	sim.Clock = func() int64 { return a4Old }
	z := zfs.New(sim)
	for _, d := range []string{a4Parent, "tank/other"} {
		if err := z.CreateFilesystem(ctx, d, nil, false); err != nil {
			t.Fatal(err)
		}
	}
	ep, err := New(z, Config{Parent: a4Parent, LockWait: 200 * time.Millisecond,
		Now: func() time.Time { return time.Unix(a4Now, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	return &a4{t: t, sim: sim, z: z, ep: ep, n: ep.names}
}

// vol makes an owned volume (committed when c), as CreateVolume would.
func (h *a4) vol(id string, c bool) {
	h.t.Helper()
	// mountpoint pinned at create, as CreateVolume does since wave14
	own := map[string]string{h.n.managed(): "true", h.n.volumeName(): id, "mountpoint": "/" + a4Parent + "/" + id}
	if c {
		own[h.n.success()] = "true"
	}
	if err := h.z.CreateFilesystem(context.Background(), a4Parent+"/"+id, own, false); err != nil {
		h.t.Fatal(err)
	}
}

func (h *a4) has(name string) bool {
	ok, err := h.z.Exists(context.Background(), name)
	if err != nil {
		h.t.Fatal(err)
	}
	return ok
}

func (h *a4) must(err error) {
	h.t.Helper()
	if err != nil {
		h.t.Fatal(err)
	}
}

// F1: a CreateVolume retry that commits an old uncommitted row while
// Reconcile waits for its lock. Reconcile re-planned under the lock, saw a
// committed volume absent from the (stale) desired set, and destroyed it
// right after CreateVolume returned success to the provisioner.
//
// In a synctest bubble the sleep below ends only once Reconcile is durably
// blocked (waiting for vol:pvc-x) or finished: the interleaving is exact,
// not a 50ms guess, and costs no wall time.
func TestAdv4CommitRaceDestroysJustProvisionedVolume(t *testing.T) {
	t.Parallel()
	synctest.Test(t, adv4CommitRace)
}

func adv4CommitRace(t *testing.T) {
	h := newA4(t)
	ctx := context.Background()
	h.vol("keep", true)
	h.vol("pvc-x", false) // first CreateVolume failed hours ago (e.g. ENOSPC)

	// CreateVolume("pvc-x") retry: lock, scan, plan, apply
	release, err := h.ep.lock(ctx, "vol:pvc-x")
	h.must(err)
	type res struct {
		p   ReconcilePlan
		err error
	}
	done := make(chan res, 1)
	go func() {
		p, err := h.ep.Reconcile(ctx, ReconcileRequest{DesiredVolumeIDs: []string{"keep"}})
		done <- res{p, err}
	}()
	time.Sleep(50 * time.Millisecond) // bubble: reconcile has scanned and reached the lock
	tbl, err := h.ep.scanFor(ctx, "pvc-x")
	h.must(err)
	plan, err := h.ep.plan.PlanCreateVolume(tbl, CreateVolumeRequest{Name: "pvc-x"})
	h.must(err)
	h.must(h.ep.apply(ctx, plan))
	_, err = h.ep.readVolume(ctx, "pvc-x")
	h.must(err) // CreateVolume returns OK to the provisioner here
	release()

	r := <-done
	if r.err != nil && CodeOf(r.err) != Aborted {
		t.Fatal(r.err)
	}
	if !h.has(a4Parent + "/pvc-x") {
		t.Fatalf("committed volume pvc-x destroyed right after a successful CreateVolume: %+v", r.p)
	}
}

// F1b: a tiny MinAge (the API had no lower bound) let an in-flight
// first-time CreateVolume be collected. Below the floor is refused.
func TestAdv4TinyMinAgeInFlightCreate(t *testing.T) {
	h := newA4(t)
	ctx := context.Background()
	h.sim.Clock = func() int64 { return a4Now - 5 } // made 5s ago, in flight
	h.vol("keep", true)
	h.vol("pvc-y", false)
	_, err := h.ep.Reconcile(ctx, ReconcileRequest{DesiredVolumeIDs: []string{"keep"}, MinAge: time.Second})
	if CodeOf(err) != InvalidArgument || !h.has(a4Parent+"/pvc-y") {
		t.Fatalf("MinAge=1s: %v", err)
	}
	if _, err := h.ep.Reconcile(ctx, ReconcileRequest{DesiredVolumeIDs: []string{"keep"}, MinAge: MinReconcileMinAge}); err != nil {
		t.Fatal(err)
	}
	if !h.has(a4Parent + "/pvc-y") {
		t.Fatal("in-flight volume destroyed")
	}
}

// F1, no lock race: the commit lands between the caller's list and the
// scan. The volume is aged from its commit, not its first attempt.
func TestReconcileAgesFromCommit(t *testing.T) {
	h := newA4(t)
	ctx := context.Background()
	h.vol("keep", true)
	h.vol("pvc-z", false) // created at a4Old, hours before a4Now
	if _, err := h.ep.CreateVolume(ctx, CreateVolumeRequest{Name: "pvc-z"}); err != nil {
		t.Fatal(err) // the retry commits at a4Now
	}
	p, err := h.ep.Reconcile(ctx, ReconcileRequest{DesiredVolumeIDs: []string{"keep"}})
	h.must(err)
	if !h.has(a4Parent+"/pvc-z") || len(p.Skipped) != 1 || p.Skipped[0].Reason != SkipTooYoung {
		t.Fatalf("just-committed volume: %+v", p)
	}
}

// F3: one undesired volume whose destroy fails (a foreign clone outside
// the parent here) aborted the run with an empty plan: earlier destroys
// already happened but were not reported, and every later volume starved.
func TestAdv4OneFailureStarvesAndHidesDestroys(t *testing.T) {
	h := newA4(t)
	ctx := context.Background()
	h.vol("keep", true)
	h.vol("a", true)
	h.vol("b", true)
	h.vol("c", true)
	// an admin clones b's (foreign) snapshot outside the parent
	h.must(h.z.Snapshot(ctx, a4Parent+"/b@backup", nil))
	h.must(h.z.Clone(ctx, a4Parent+"/b@backup", "tank/other/restore", nil))

	p, err := h.ep.Reconcile(ctx, digested(ctx, t, h.ep, ReconcileRequest{DesiredVolumeIDs: []string{"keep"}}))
	h.must(err)
	if h.has(a4Parent+"/a") || h.has(a4Parent+"/c") || !h.has(a4Parent+"/b") || !h.has("tank/other/restore") {
		t.Fatalf("plan %+v", p)
	}
	if len(p.Actions) != 2 {
		t.Fatalf("destroys not reported: %+v", p)
	}
}

// F3b: a destroy that fails at zfs (busy: an open zvol, a hold) is
// reported against its volume, and the rest of the run goes on.
func TestAdv4BusyDestroyAbortsRun(t *testing.T) {
	h := newA4(t)
	ctx := context.Background()
	h.vol("keep", true)
	for _, id := range []string{"a", "b", "c"} {
		h.vol(id, true)
	}
	h.sim.Fail = func(args []string) error {
		if args[0] == "destroy" && args[len(args)-1] == a4Parent+"/b" {
			return &zfs.ExitError{Code: 1, Stderr: "cannot destroy 'tank/v/b': dataset is busy\n"}
		}
		return nil
	}
	p, err := h.ep.Reconcile(ctx, digested(ctx, t, h.ep, ReconcileRequest{DesiredVolumeIDs: []string{"keep"}}))
	h.must(err)
	if h.has(a4Parent+"/a") || h.has(a4Parent+"/c") || !h.has(a4Parent+"/b") {
		t.Fatalf("plan %+v", p)
	}
	if len(p.Actions) != 2 || len(p.Skipped) != 1 || p.Skipped[0].Target != "b" || !strings.Contains(p.Skipped[0].Reason, "busy") {
		t.Fatalf("plan %+v", p)
	}
}

// A clone collected first that fails leaves its source unattempted: the
// source's destroy would only be refused for the clone anyway.
func TestReconcileSkipsSourceOfFailedClone(t *testing.T) {
	h := newA4(t)
	ctx := context.Background()
	h.vol("keep", true)
	h.vol("src", true)
	// created and committed long ago
	old, err := New(h.z, Config{Parent: a4Parent, Now: func() time.Time { return time.Unix(a4Old, 0) }})
	h.must(err)
	if _, err := old.CreateVolume(ctx, CreateVolumeRequest{Name: "cl", SourceVolumeID: "src"}); err != nil {
		t.Fatal(err)
	}
	h.sim.Fail = func(args []string) error {
		if args[0] == "destroy" && args[len(args)-1] == a4Parent+"/cl" {
			return &zfs.ExitError{Code: 1, Stderr: "cannot destroy 'tank/v/cl': dataset is busy\n"}
		}
		return nil
	}
	p, err := h.ep.Reconcile(ctx, digested(ctx, t, h.ep, ReconcileRequest{DesiredVolumeIDs: []string{"keep"}}))
	h.must(err)
	if !h.has(a4Parent+"/src") || !h.has(a4Parent+"/cl") {
		t.Fatalf("plan %+v", p)
	}
	var reasons []string
	for _, s := range p.Skipped {
		reasons = append(reasons, s.Target+": "+s.Reason)
	}
	if len(reasons) != 2 || reasons[1] != "src: "+SkipDependentFailed {
		t.Fatalf("skipped %q", reasons)
	}
	// the released origin stays, deferred: zfs reaps it with the clone
	h.sim.Fail = nil
	if _, err := h.ep.Reconcile(ctx, digested(ctx, t, h.ep, ReconcileRequest{DesiredVolumeIDs: []string{"keep"}})); err != nil {
		t.Fatal(err)
	}
	if h.has(a4Parent+"/src") || h.has(a4Parent+"/cl") || h.has(a4Parent+"/src@"+cloneSourceSnapshotPrefix+"cl") {
		t.Fatal("not converged after the failure cleared")
	}
}

// F8: Reconcile took every key at once; one busy volume (any op longer
// than LockWait) aborted the whole run.
func TestAdv4OneBusyLockAbortsWholeRun(t *testing.T) {
	h := newA4(t)
	ctx := context.Background()
	h.vol("keep", true)
	h.vol("a", true)
	h.vol("z", true)
	release, err := h.ep.lock(ctx, "vol:z") // e.g. a slow DeleteVolume/expand on z
	h.must(err)
	defer release()
	start := time.Now()
	p, err := h.ep.Reconcile(ctx, digested(ctx, t, h.ep, ReconcileRequest{DesiredVolumeIDs: []string{"keep"}}))
	if err != nil || h.has(a4Parent+"/a") || !h.has(a4Parent+"/z") {
		t.Fatalf("a not collected because z is busy: %+v %v", p, err)
	}
	if len(p.Skipped) != 1 || p.Skipped[0].Target != "z" || p.Skipped[0].Reason != SkipBusy {
		t.Fatalf("busy z not reported: %+v", p)
	}
	if time.Since(start) >= h.ep.cfg.LockWait {
		t.Fatal("waited for the busy lock")
	}
	if h.ep.locks.Held("vol:a") {
		t.Fatal("lock not released")
	}
}

// F2: no guard against a truncated or wrong desired set: one page of a
// paginated PV list, or a set from another cluster, destroyed everything.
func TestAdv4PartialDesiredSetMassDeletes(t *testing.T) {
	h := newA4(t)
	ctx := context.Background()
	for i := range 50 {
		h.vol(fmt.Sprintf("pvc-%02d", i), true)
	}
	h.vol("abandoned", false)
	p, err := h.ep.Reconcile(ctx, ReconcileRequest{DesiredVolumeIDs: []string{"pvc-00"}})
	if CodeOf(err) != FailedPrecondition || len(p.Actions) != 50 {
		t.Fatalf("over the cap: %d actions, %v", len(p.Actions), err)
	}
	p2, err := h.ep.Reconcile(ctx, ReconcileRequest{DesiredVolumeIDs: []string{"pvc-from-other-cluster"}})
	h.must(err)
	for i := range 50 {
		if !h.has(fmt.Sprintf("%s/pvc-%02d", a4Parent, i)) {
			t.Fatalf("pvc-%02d destroyed: %+v", i, p2)
		}
	}
	// the set naming nothing of ours still collects what never depends on it
	if len(p2.Actions) != 1 || p2.Actions[0].Target != "abandoned" {
		t.Fatalf("second: %+v", p2)
	}
}

func TestReconcileMaxDeletionsAndDigest(t *testing.T) {
	h := newA4(t)
	ctx := context.Background()
	for _, id := range []string{"keep", "a", "b", "c"} {
		h.vol(id, true)
	}
	keep := []string{"keep"}
	// without a digest the digest gate refuses first: the max-deletions
	// check below must be reached with one, or deleting it passes
	_, err := h.ep.Reconcile(ctx, ReconcileRequest{DesiredVolumeIDs: keep, MaxDeletions: 2})
	if CodeOf(err) != FailedPrecondition || !strings.Contains(err.Error(), "without a plan digest") {
		t.Fatalf("no digest: %v", err)
	}
	_, err = h.ep.Reconcile(ctx, digested(ctx, t, h.ep, ReconcileRequest{DesiredVolumeIDs: keep, MaxDeletions: 2}))
	if CodeOf(err) != FailedPrecondition || !strings.Contains(err.Error(), "more than max deletions 2") {
		t.Fatalf("3 > 2: %v", err)
	}
	for _, id := range []string{"a", "b", "c"} {
		if !h.has(a4Parent + "/" + id) {
			t.Fatalf("%s destroyed over the max deletions", id)
		}
	}
	if _, err := h.ep.Reconcile(ctx, ReconcileRequest{DesiredVolumeIDs: keep, MaxDeletions: 2, UnlimitedDeletions: true}); CodeOf(err) != InvalidArgument {
		t.Fatalf("both: %v", err)
	}
	dry, err := h.ep.Reconcile(ctx, ReconcileRequest{DesiredVolumeIDs: keep, DryRun: true})
	h.must(err)
	if dry.Digest == "" || len(dry.Actions) != 3 {
		t.Fatalf("dry %+v", dry)
	}
	// the state moved since the dry run: refused, nothing applied
	h.vol("d", true)
	if _, err := h.ep.Reconcile(ctx, ReconcileRequest{DesiredVolumeIDs: keep, PlanDigest: dry.Digest}); CodeOf(err) != Aborted || !h.has(a4Parent+"/a") {
		t.Fatalf("stale digest: %v", err)
	}
	keep = append(keep, "d")
	dry, err = h.ep.Reconcile(ctx, ReconcileRequest{DesiredVolumeIDs: keep, DryRun: true})
	h.must(err)
	done, err := h.ep.Reconcile(ctx, ReconcileRequest{DesiredVolumeIDs: keep, PlanDigest: dry.Digest, MaxDeletions: 3})
	h.must(err)
	if done.Digest != dry.Digest || len(done.Actions) != 3 || h.has(a4Parent+"/a") {
		t.Fatalf("echoed digest: %+v", done)
	}
}

// MaxDeletions 0 is DefaultMaxDeletions, also for a digested run: 11
// undesired volumes are refused whole.
func TestReconcileDefaultMaxDeletionsWithDigest(t *testing.T) {
	h := newA4(t)
	ctx := context.Background()
	h.vol("keep", true)
	for i := range DefaultMaxDeletions + 1 {
		h.vol(fmt.Sprintf("x%02d", i), true)
	}
	r := digested(ctx, t, h.ep, ReconcileRequest{DesiredVolumeIDs: []string{"keep"}})
	_, err := h.ep.Reconcile(ctx, r)
	if CodeOf(err) != FailedPrecondition || !strings.Contains(err.Error(), fmt.Sprintf("more than max deletions %d", DefaultMaxDeletions)) {
		t.Fatalf("%d > default: %v", DefaultMaxDeletions+1, err)
	}
	for i := range DefaultMaxDeletions + 1 {
		if !h.has(fmt.Sprintf("%s/x%02d", a4Parent, i)) {
			t.Fatalf("x%02d destroyed over the default max deletions", i)
		}
	}
	// exactly the default passes
	h.must(h.z.Destroy(ctx, a4Parent+"/x00", zfs.DestroyOptions{}))
	p, err := h.ep.Reconcile(ctx, digested(ctx, t, h.ep, ReconcileRequest{DesiredVolumeIDs: []string{"keep"}}))
	h.must(err)
	if len(p.Actions) != DefaultMaxDeletions {
		t.Fatalf("at the default: %+v", p)
	}
}

// F6: clone-source snapshots were recognised by name alone. A snapshot an
// admin or another tool cut with that name on a desired volume was
// collected.
func TestAdv4UserSnapshotWithReservedPrefixCollected(t *testing.T) {
	h := newA4(t)
	ctx := context.Background()
	h.vol("db", true)
	name := a4Parent + "/db@" + cloneSourceSnapshotPrefix + "manual-pre-upgrade"
	h.must(h.z.Snapshot(ctx, name, nil))
	_, err := h.ep.Reconcile(ctx, ReconcileRequest{DesiredVolumeIDs: []string{"db"}})
	h.must(err)
	if !h.has(name) {
		t.Fatal("unowned snapshot on a desired volume destroyed because of its name")
	}
}

// F7: the released origin of a collected clone was destroyed wherever it
// lived (outside the parent too) and whatever clone id it named.
func TestAdv4ReleasedOriginOutsideParent(t *testing.T) {
	h := newA4(t)
	ctx := context.Background()
	h.vol("keep", true)
	h.must(h.z.CreateFilesystem(ctx, "tank/other/golden", nil, false))
	origin := "tank/other/golden@" + cloneSourceSnapshotPrefix + "someone"
	h.must(h.z.Snapshot(ctx, origin, nil))
	h.must(h.z.Clone(ctx, origin, a4Parent+"/adopted", map[string]string{
		h.n.managed(): "true", h.n.volumeName(): "adopted", h.n.success(): "true"}))
	// and inside the parent, a stamp for another clone
	h.vol("src", true)
	other := a4Parent + "/src@" + cloneSourceSnapshotPrefix + "c2"
	h.must(h.z.Snapshot(ctx, other, map[string]string{h.n.managed(): "true", h.n.cloneSourceFor(): "someone-else"}))
	h.must(h.z.Clone(ctx, other, a4Parent+"/c2", map[string]string{
		h.n.managed(): "true", h.n.volumeName(): "c2", h.n.success(): "true"}))
	p, err := h.ep.Reconcile(ctx, digested(ctx, t, h.ep, ReconcileRequest{DesiredVolumeIDs: []string{"keep", "src"}}))
	h.must(err)
	for _, a := range p.Actions {
		for _, v := range a.Volumes {
			if _, err := volumeID(v); err != nil {
				t.Errorf("action %s locks %q", a.Target, v)
			}
		}
	}
	if !h.has(origin) || !h.has(other) {
		t.Fatalf("a snapshot the endpoint does not own was released: %+v", p.Actions)
	}
	if h.has(a4Parent+"/adopted") || h.has(a4Parent+"/c2") {
		t.Fatalf("the clones themselves are ours and undesired: %+v", p)
	}
}

// The legacy rule: an unstamped origin is released with its clone where
// the origin proves it was cut for that clone, from an owned volume.
func TestLegacyUnstampedOriginReleasedWithItsClone(t *testing.T) {
	h := newA4(t)
	ctx := context.Background()
	h.vol("keep", true)
	h.vol("src", true)
	origin := a4Parent + "/src@" + cloneSourceSnapshotPrefix + "dst"
	h.must(h.z.Snapshot(ctx, origin, nil)) // democratic-csi cuts it bare
	h.must(h.z.Clone(ctx, origin, a4Parent+"/dst", map[string]string{
		h.n.managed(): "true", h.n.volumeName(): "dst", h.n.success(): "true"}))
	h.must(h.ep.DeleteVolume(ctx, "dst"))
	if h.has(origin) {
		t.Fatal("legacy origin not released with its clone")
	}
}

// F6, the create side: a clone never adopts (and pins) a snapshot that
// already holds the reserved name without the endpoint's stamp.
func TestCloneRefusesUnstampedReservedSnapshot(t *testing.T) {
	h := newA4(t)
	ctx := context.Background()
	h.vol("src", true)
	name := a4Parent + "/src@" + cloneSourceSnapshotPrefix + "dst"
	h.must(h.z.Snapshot(ctx, name, nil))
	_, err := h.ep.CreateVolume(ctx, CreateVolumeRequest{Name: "dst", SourceVolumeID: "src"})
	if CodeOf(err) != AlreadyExists || h.has(a4Parent+"/dst") {
		t.Fatalf("clone from an unstamped reserved snapshot: %v", err)
	}
}
