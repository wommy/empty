package endpoint

// Directed repros from the wave-5 impossible-state review, ported against
// the fixes: each failed before its fix.

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

// R11: a capacity near 2^64 wrapped to 0 in roundTo: ExpandVolume(MaxUint64)
// succeeded at 1 MiB, and a clone asked for MaxUint64-1 committed at the
// snapshot's size.
func TestReachCapacityWrap(t *testing.T) {
	ctx := context.Background()
	sim := zfstest.New("tank")
	z := zfs.New(sim)
	if err := z.CreateFilesystem(ctx, rParent, nil, true); err != nil {
		t.Fatal(err)
	}
	ep, err := New(z, Config{Parent: rParent})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ep.CreateVolume(ctx, CreateVolumeRequest{Name: "a", Kind: Block, CapacityBytes: 1 << 20, Sparse: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := ep.ExpandVolume(ctx, "a", math.MaxUint64, 0); CodeOf(err) != OutOfRange {
		t.Errorf("ExpandVolume(MaxUint64): %v, want OutOfRange", err)
	}
	if _, err := ep.CreateSnapshot(ctx, "s", "a"); err != nil {
		t.Fatal(err)
	}
	_, err = ep.CreateVolume(ctx, CreateVolumeRequest{Name: "c", Kind: Block, CapacityBytes: math.MaxUint64 - 1, Sparse: true, SourceSnapshotID: "a@s"})
	if CodeOf(err) != OutOfRange {
		t.Errorf("CreateVolume(MaxUint64-1 from snapshot): %v, want OutOfRange", err)
	}
	if sim.Has(rParent + "/c") {
		t.Error("created")
	}
}

// R15: an uncommitted row (a provision in flight, or one Reconcile will
// collect as abandoned) was served by GetVolume, ExpandVolume, export and
// CreateSnapshot, while ListVolumes hid it.
func TestReachUncommittedServed(t *testing.T) {
	ctx := context.Background()
	sim := zfstest.New("tank")
	z := zfs.New(sim)
	if err := z.CreateFilesystem(ctx, rParent, nil, true); err != nil {
		t.Fatal(err)
	}
	ep, err := New(z, Config{Parent: rParent, FileExport: true, Etab: sim})
	if err != nil {
		t.Fatal(err)
	}
	if err := z.CreateFilesystem(ctx, rParent+"/u", map[string]string{
		rNames.managed(): "true", rNames.volumeName(): "u", "refquota": "1048576",
	}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := ep.GetVolume(ctx, "u"); CodeOf(err) != NotFound {
		t.Errorf("GetVolume: %v", err)
	}
	if _, err := ep.ExpandVolume(ctx, "u", 2<<20, 0); CodeOf(err) != NotFound {
		t.Errorf("ExpandVolume: %v", err)
	}
	if _, err := ep.CreateSnapshot(ctx, "s", "u"); CodeOf(err) != NotFound {
		t.Errorf("CreateSnapshot: %v", err)
	}
	if _, err := ep.PublishFileExport(ctx, "u", "10.0.0.1", false); CodeOf(err) != NotFound {
		t.Errorf("PublishFileExport: %v", err)
	}
	if _, err := ep.CreateVolume(ctx, CreateVolumeRequest{Name: "c", SourceVolumeID: "u"}); CodeOf(err) != NotFound {
		t.Errorf("clone from it: %v", err)
	}
	if sim.Prop(rParent+"/u", "refquota") != "1048576" || sim.Has(rParent+"/u@s") {
		t.Error("touched")
	}
}

// R6: a volume whose origin is a clone-source snapshot outside the parent
// planned `destroy -d` outside the endpoint's tree, and Reconcile locked
// a key no request ever takes.
func TestReachOriginOutsideParent(t *testing.T) {
	origin := "tank/archive/s@" + cloneSourceSnapshotPrefix + "c"
	raw := rawOf(dsCells(rParent+"/c", zfs.TypeFilesystem, merge(owned("c", true),
		map[string][2]string{"origin": {origin, "-"}})))
	tbl, ep := tableFromRaw(t, raw)
	plan, err := ep.plan.PlanDeleteVolume(tbl, "c")
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range plan {
		if !strings.HasPrefix(op.Target, rParent+"/") {
			t.Errorf("op %+v targets outside %s", op, rParent)
		}
	}
	rp, err := ep.plan.PlanReconcile(tbl, Desired{VolumeIDs: []string{"c"}, MinAge: time.Hour, Now: rNow})
	if err != nil {
		t.Fatal(err)
	}
	rp2, _ := ep.plan.PlanReconcile(tbl, Desired{VolumeIDs: []string{"x"}, MinAge: time.Hour, Now: rNow})
	for _, a := range append(rp.Actions, rp2.Actions...) {
		for _, v := range a.Volumes {
			if strings.Contains(v, "/") {
				t.Errorf("reconcile action %s locks %q", a.Target, v)
			}
		}
	}
}

// Lifecycle FSM I2: managed without the volume-name marker is a state
// neither the endpoint nor democratic-csi writes; it is not ours.
func TestManagedWithoutNameIsNotOwned(t *testing.T) {
	raw := rawOf(dsCells(rParent+"/m", zfs.TypeFilesystem, map[string][2]string{
		rNames.managed(): {"true", "local"}, rNames.success(): {"true", "local"},
	}))
	tbl, ep := tableFromRaw(t, raw)
	if r, _ := tbl.Get("m"); r.OwnedVolume() {
		t.Fatal("owned without a name marker")
	}
	if _, err := ep.GetVolume(context.Background(), "m"); CodeOf(err) != NotFound {
		t.Fatalf("GetVolume: %v", err)
	}
	if _, err := ep.plan.PlanDeleteVolume(tbl, "m"); CodeOf(err) != FailedPrecondition {
		t.Fatalf("DeleteVolume: %v", err)
	}
}

// R12: a Planner built without VolBlockSize divided by zero on any block
// request.
func TestReachPlannerZeroBlockSize(t *testing.T) {
	plan, err := Planner{Parent: rParent, Names: rNames}.PlanCreateVolume(NewTable(nil), CreateVolumeRequest{Name: "a", Kind: Block, CapacityBytes: 1})
	if err != nil || plan[0].Size != defaultVolBlockSize {
		t.Fatalf("plan %+v %v", plan, err)
	}
}
