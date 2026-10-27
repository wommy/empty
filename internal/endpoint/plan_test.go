package endpoint

import (
	"strings"
	"testing"

	"github.com/wommy/empty/internal/zfs"
)

// Planner tests are pure: a Table of rows in, a Plan or error out. No zfs,
// no simulator; every interesting state is one literal away.

var pl = Planner{Parent: "tank/v", Names: props{"democratic-csi"}, VolBlockSize: 16384}

func vol(id string, mut func(*Row)) Row {
	r := Row{Name: "tank/v/" + id, ID: id, Type: zfs.TypeFilesystem, Managed: true, Committed: true, VolumeName: id}
	if mut != nil {
		mut(&r)
	}
	return r
}

func snap(id string, mut func(*Row)) Row {
	r := Row{Name: "tank/v/" + id, ID: id, Type: zfs.TypeSnapshot, Managed: true, SnapshotName: id[len(id)-1:]}
	if mut != nil {
		mut(&r)
	}
	return r
}

func code(err error) Code { return CodeOf(err) }

// csi-sanity creates a snapshot with a 128-byte name; its id must still fit
// 128 bytes, and re-creating it must find the hashed snapshot again.
func TestPlanSnapshotLongAndUnusualNames(t *testing.T) {
	long := strings.Repeat("a", 128)
	for _, name := range []string{long, "snap with spaces", "ünïcode"} {
		tab := NewTable([]Row{vol("pvc-0123456789", nil)})
		plan, id, err := pl.PlanCreateSnapshot(tab, name, "pvc-0123456789")
		if err != nil || len(plan) != 1 || len(id) > 128 || !zfs.ValidSnapshot("tank/v/"+id) {
			t.Fatalf("%q: plan %+v id %q err %v", name, plan, id, err)
		}
		done := NewTable([]Row{vol("pvc-0123456789", nil), snap(id, func(r *Row) { r.SnapshotName = name })})
		if again, id2, err := pl.PlanCreateSnapshot(done, name, "pvc-0123456789"); err != nil || again != nil || id2 != id {
			t.Fatalf("%q: retry plan %+v id %q err %v", name, again, id2, err)
		}
	}
	if _, _, err := pl.PlanCreateSnapshot(NewTable([]Row{vol("a", nil)}), strings.Repeat("a", 129), "a"); code(err) != InvalidArgument {
		t.Fatalf("129 bytes: %v", err)
	}
}

// A thick filesystem whose last Expand applied refquota but not
// refreservation (ENOSPC) is under-reserved: Expand re-plans the
// reservation up to its capacity instead of reporting it done.
func TestPlanExpandReplansHalfAppliedReservation(t *testing.T) {
	half := vol("thick", func(r *Row) { r.Refquota, r.Refres = 10<<30, 1<<30 })
	for _, capacity := range []uint64{10 << 30, 2 << 30} {
		plan, _, err := pl.PlanExpandVolume(NewTable([]Row{half}), "thick", capacity, 0)
		if err != nil || len(plan) != 1 || plan[0].Props["refreservation"] != u64(10<<30) {
			t.Fatalf("capacity %d: %+v %v", capacity, plan, err)
		}
		if _, ok := plan[0].Props["refquota"]; ok {
			t.Fatalf("capacity %d: refquota re-set: %+v", capacity, plan)
		}
	}
	whole := vol("thick", func(r *Row) { r.Refquota, r.Refres = 10<<30, 10<<30 })
	if plan, _, err := pl.PlanExpandVolume(NewTable([]Row{whole}), "thick", 10<<30, 0); err != nil || plan != nil {
		t.Fatalf("reserved volume: %+v %v", plan, err)
	}
	thin := vol("thin", func(r *Row) { r.Refquota = 10 << 30 })
	if plan, _, err := pl.PlanExpandVolume(NewTable([]Row{thin}), "thin", 10<<30, 0); err != nil || plan != nil {
		t.Fatalf("thin volume: %+v %v", plan, err)
	}
}
