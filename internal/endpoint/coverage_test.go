package endpoint

import (
	"context"
	"testing"

	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

// A table read for some volumes says nothing about the others: a planner
// that concludes from absence (delete of a missing volume is OK, reconcile
// collects what is not wanted, a create plans for a free name, snapshot
// names are unique) must refuse a table that does not cover what it
// judges, instead of reading "not scanned" as "not there".
func TestPlannersRequireACoveringTable(t *testing.T) {
	partial := newPartialTable([]Row{vol("a", nil)}, "a")
	if !partial.Covers("a") || partial.Covers("b") || partial.Whole() {
		t.Fatal("partial coverage wrong")
	}
	if whole := NewTable(nil); !whole.Whole() || !whole.Covers("b") {
		t.Fatal("NewTable is the whole parent")
	}
	if (Table{}).Covers("a") {
		t.Fatal("the zero table covers nothing")
	}

	if _, err := pl.PlanDeleteVolume(partial, "b"); code(err) != Internal {
		t.Errorf("delete uncovered: %v", err)
	}
	// a missing volume's delete also releases clone-source snapshots cut
	// for it, which hang off other volumes: only the whole parent says
	// there are none
	if _, err := pl.PlanDeleteVolume(newPartialTable(nil, "b"), "b"); code(err) != Internal {
		t.Errorf("delete missing on a table of b alone: %v", err)
	}
	if plan, err := pl.PlanDeleteVolume(NewTable(nil), "b"); err != nil || len(plan) != 0 {
		t.Errorf("delete missing: %v %v", plan, err)
	}
	if _, err := pl.PlanDeleteSnapshot(partial, "b@s"); code(err) != Internal {
		t.Errorf("delete snapshot uncovered: %v", err)
	}
	if _, _, err := pl.PlanExpandVolume(partial, "b", 1<<20, 0); code(err) != Internal {
		t.Errorf("expand uncovered: %v", err)
	}
	if _, err := pl.PlanCreateVolume(partial, CreateVolumeRequest{Name: "b"}); code(err) != Internal {
		t.Errorf("create uncovered: %v", err)
	}
	if _, err := pl.PlanCreateVolume(partial, CreateVolumeRequest{Name: "a2", SourceVolumeID: "c"}); code(err) != Internal {
		t.Errorf("create from uncovered source: %v", err)
	}
	// snapshot names are global: even the source volume's own table is not
	// enough to know the name is free
	if _, _, err := pl.PlanCreateSnapshot(partial, "s", "a"); code(err) != Internal {
		t.Errorf("snapshot on a partial table: %v", err)
	}
	if _, err := pl.PlanReconcile(partial, Desired{}); code(err) != Internal {
		t.Errorf("reconcile on a partial table: %v", err)
	}
	if _, _, err := pl.PlanUnpublishExport(partial, nil, "b", "10.0.0.1"); code(err) != Internal {
		t.Errorf("unpublish uncovered: %v", err)
	}
	if _, _, err := pl.PlanPublishExport(partial, nil, "b", NFSClient{Host: "10.0.0.1/32"}); code(err) != Internal {
		t.Errorf("publish uncovered: %v", err)
	}
}

func TestScanRecordsCoverage(t *testing.T) {
	ctx := context.Background()
	sim := zfstest.New("tank")
	e, err := New(zfs.New(sim), Config{Parent: "tank"})
	if err != nil {
		t.Fatal(err)
	}
	if tab, err := e.scan(ctx, ""); err != nil || !tab.Whole() {
		t.Fatalf("whole scan: %v", err)
	}
	if tab, err := e.scan(ctx, "x"); err != nil || tab.Whole() || !tab.Covers("x") || tab.Covers("y") {
		t.Fatalf("volume scan: %+v %v", tab, err)
	}
	if tab, err := e.scanFor(ctx, "x", "y@s", ""); err != nil || tab.Whole() || !tab.Covers("x") || !tab.Covers("y") || tab.Covers("z") {
		t.Fatalf("scanFor: %+v %v", tab, err)
	}
}
