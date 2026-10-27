package endpoint

// Directed repros from the wave-5 impossible-state review, ported against
// the fixes: each failed before its fix.

import (
	"context"
	"math"
	"testing"

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

// R12: a Planner built without VolBlockSize divided by zero on any block
// request.
func TestReachPlannerZeroBlockSize(t *testing.T) {
	plan, err := Planner{Parent: rParent, Names: rNames}.PlanCreateVolume(NewTable(nil), CreateVolumeRequest{Name: "a", Kind: Block, CapacityBytes: 1})
	if err != nil || plan[0].Size != defaultVolBlockSize {
		t.Fatalf("plan %+v %v", plan, err)
	}
}
