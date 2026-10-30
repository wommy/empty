package endpoint

import (
	"fmt"
	"testing"

	"github.com/wommy/empty/internal/zfs"
)

// PlanRollbackCreate (ADR 0010) is pure, like every planner: the rollback
// of a fresh create is planDelete's destroys, each tied to the guid the
// re-scan read, behind Resume's own guards (owned, this name, this kind
// and source, uncommitted); and, when the clone never made its row, the
// release of the one clone-source snapshot it cut, if unused and stamped
// for it. Anything else is left alone (nil) or refused.
func TestPlanRollbackCreate(t *testing.T) {
	t.Parallel()
	const cut = "src@" + cloneSourceSnapshotPrefix + "c"
	fresh := func(r *Row) { r.Committed, r.GUID = false, "g-"+r.ID }
	clone := func(r *Row) {
		fresh(r)
		r.SourceType, r.SourceID, r.Origin = "volume", "src", "tank/v/"+cut
	}
	stamped := func(r *Row) { stamp(r); r.GUID = "g-cut" }
	fsReq := CreateVolumeRequest{Name: "v"}
	cloneReq := CreateVolumeRequest{Name: "c", SourceVolumeID: "src"}
	for _, tc := range []struct {
		name string
		t    Table
		req  CreateVolumeRequest
		want string // the plan, one destroy per op; "" for none
		code Code   // a refusal's
	}{
		{"uncommitted", NewTable([]Row{vol("v", fresh)}), fsReq, "tank/v/v g-v", OK},
		{"uncommitted zvol", NewTable([]Row{vol("v", func(r *Row) { fresh(r); r.Type = zfs.TypeVolume })}),
			CreateVolumeRequest{Name: "v", Kind: Block}, "tank/v/v g-v", OK},
		{"absent", NewTable(nil), fsReq, "", OK},
		{"committed", NewTable([]Row{vol("v", func(r *Row) { fresh(r); r.Committed = true })}), fsReq, "", OK},
		{"unmanaged", NewTable([]Row{vol("v", func(r *Row) { fresh(r); r.Managed = false })}), fsReq, "", OK},
		{"another name marker", NewTable([]Row{vol("v", func(r *Row) { fresh(r); r.VolumeName = "w" })}), fsReq, "", OK},
		{"no name marker", NewTable([]Row{vol("v", func(r *Row) { fresh(r); r.VolumeName = "" })}), fsReq, "", OK},
		{"another kind", NewTable([]Row{vol("v", fresh)}), CreateVolumeRequest{Name: "v", Kind: Block}, "", OK},
		{"another source", NewTable([]Row{vol("c", clone), vol("src", nil), snap(cut, stamped)}),
			CreateVolumeRequest{Name: "c", SourceVolumeID: "other"}, "", OK},
		{"no source asked", NewTable([]Row{vol("c", clone), vol("src", nil), snap(cut, stamped)}),
			CreateVolumeRequest{Name: "c"}, "", OK},
		{"children", NewTable([]Row{vol("v", func(r *Row) { fresh(r); r.Children = []string{"v/x"} })}), fsReq, "", FailedPrecondition},
		{"a CSI snapshot", NewTable([]Row{vol("v", fresh), snap("v@s", nil)}), fsReq, "", FailedPrecondition},
		{"a held snapshot", NewTable([]Row{vol("v", fresh), snap("v@x", func(r *Row) { r.Managed, r.SnapshotName, r.Holds = false, "", 1 })}),
			fsReq, "", FailedPrecondition},
		{"a foreign snapshot", NewTable([]Row{vol("v", fresh), snap("v@x", func(r *Row) { r.Managed, r.SnapshotName = false, "" })}),
			fsReq, "", FailedPrecondition},
		{"a dependent clone", NewTable([]Row{vol("v", fresh), snap("v@x", func(r *Row) { r.Managed, r.SnapshotName = false, "" }),
			vol("w", func(r *Row) { r.Origin = "tank/v/v@x" })}), fsReq, "", FailedPrecondition},
		// a clone of a volume: its origin, released first (deferred), then the clone
		{"clone", NewTable([]Row{vol("c", clone), vol("src", nil), snap(cut, stamped)}), cloneReq,
			"tank/v/" + cut + " g-cut -d; tank/v/c g-c", OK},
		// the clone failed after the cut: only the cut is released
		{"cut, no clone", NewTable([]Row{vol("src", nil), snap(cut, stamped)}), cloneReq, "tank/v/" + cut + " g-cut -d", OK},
		{"cut, read for the rollback", newPartialTable([]Row{vol("src", nil), snap(cut, stamped)}, "c", "src"), cloneReq,
			"tank/v/" + cut + " g-cut -d", OK},
		{"cut unstamped (reserved name only)", NewTable([]Row{vol("src", nil), snap(cut, bare)}), cloneReq, "", OK},
		{"cut stamped for another clone", NewTable([]Row{vol("src", nil), snap(cut, func(r *Row) { stamped(r); r.CloneSourceFor = "d" })}),
			cloneReq, "", OK},
		{"cut in use by another clone", NewTable([]Row{vol("src", nil), snap(cut, stamped), vol("o", func(r *Row) { r.Origin = "tank/v/" + cut })}),
			cloneReq, "", OK},
		{"cut already released", NewTable([]Row{vol("src", nil), snap(cut, func(r *Row) { stamped(r); r.Deferred = true })}), cloneReq, "", OK},
		{"cut on a source not ours", NewTable([]Row{vol("src", func(r *Row) { r.Managed = false }), snap(cut, stamped)}), cloneReq, "", OK},
		// a table that cannot say the volume or the source is absent
		{"source not read", newPartialTable(nil, "c"), cloneReq, "", Internal},
		{"volume not read", newPartialTable([]Row{vol("src", nil)}, "src"), cloneReq, "", Internal},
	} {
		plan, err := pl.PlanRollbackCreate(tc.t, tc.req)
		got := ""
		for i, op := range plan {
			if op.Kind != OpDestroy || !op.OkMissing || op.Recursive {
				t.Errorf("%s: op %+v, want a plain destroy, missing ok", tc.name, op)
			}
			if i > 0 {
				got += "; "
			}
			got += fmt.Sprintf("%s %s", op.Target, op.GUID)
			if op.Defer {
				got += " -d"
			}
		}
		if code(err) != tc.code || got != tc.want {
			t.Errorf("%s: plan %q, err %v; want %q, code %v", tc.name, got, err, tc.want, tc.code)
		}
	}
}
