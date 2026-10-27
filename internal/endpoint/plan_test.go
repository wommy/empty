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
