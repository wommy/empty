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

// csi-sanity: a content source id the endpoint could never have issued
// names nothing, so it is NotFound (CSI), not InvalidArgument.
func TestPlanCreateMalformedSourceIsNotFound(t *testing.T) {
	for _, r := range []CreateVolumeRequest{
		{Name: "a", CapacityBytes: 1, SourceSnapshotID: "non-existing-snapshot-id"},
		{Name: "a", CapacityBytes: 1, SourceVolumeID: "no/such@volume"},
	} {
		if _, err := pl.PlanCreateVolume(NewTable(nil), r); code(err) != NotFound {
			t.Errorf("%+v: %v", r, err)
		}
	}
}

func TestPlanCreateIsEmptyWhenSatisfied(t *testing.T) {
	tab := NewTable([]Row{vol("a", func(r *Row) { r.Refquota = 100 })})
	plan, err := pl.PlanCreateVolume(tab, CreateVolumeRequest{Name: "a", CapacityBytes: 100})
	if err != nil || len(plan) != 0 {
		t.Fatalf("plan %v err %v", plan, err)
	}
}

// review C1/C2: a clone that never got its size or reservation (crash, or
// a failed set) must converge on retry, not answer AlreadyExists forever.
func TestPlanCreateConvergesUncommittedClone(t *testing.T) {
	tab := NewTable([]Row{
		vol("src", func(r *Row) { r.Type, r.Volsize, r.Refres = zfs.TypeVolume, 1<<30, 1<<30 }),
		snap("src@s", func(r *Row) { r.Volsize = 1 << 30 }),
		vol("dst", func(r *Row) {
			r.Type, r.Volsize, r.Committed = zfs.TypeVolume, 1<<30, false
			r.SourceType, r.SourceID = "snapshot", "src@s"
		}),
	})
	plan, err := pl.PlanCreateVolume(tab, CreateVolumeRequest{Name: "dst", Kind: Block, CapacityBytes: 2 << 30, SourceSnapshotID: "src@s"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 2 || plan[0].Props["volsize"] != "2147483648" || plan[0].Props["refreservation"] != "auto" ||
		plan[1].Props["democratic-csi:provision_success"] != "true" || plan[0].Target != "tank/v/dst" {
		t.Fatalf("plan %+v", plan)
	}
}

func TestPlanCreateRefusesForeignAndIncompatible(t *testing.T) {
	tab := NewTable([]Row{
		vol("foreign", func(r *Row) { r.Managed = false }),
		vol("small", func(r *Row) { r.Refquota = 10 }),
		vol("thin", func(r *Row) { r.Type, r.Volsize = zfs.TypeVolume, 16384 }),
	})
	for _, r := range []CreateVolumeRequest{
		{Name: "foreign"},
		{Name: "small", CapacityBytes: 11},
		{Name: "small", Kind: Block, CapacityBytes: 1},
		{Name: "thin", Kind: Block, CapacityBytes: 16384, Sparse: false}, // thick requested
	} {
		if _, err := pl.PlanCreateVolume(tab, r); code(err) != AlreadyExists {
			t.Errorf("%+v: %v", r, err)
		}
	}
}

// review M1: "at most limit" must not create an unlimited filesystem
func TestPlanCreateHonorsLimitOnly(t *testing.T) {
	plan, err := pl.PlanCreateVolume(NewTable(nil), CreateVolumeRequest{Name: "a", LimitBytes: 1 << 20})
	if err != nil || plan[0].Props["refquota"] != "1048576" {
		t.Fatalf("plan %+v %v", plan, err)
	}
}

// review C6 (zfs adversary): thick zvols keep zfs's auto refreservation
func TestPlanCreateThickZvolLeavesReservationToZFS(t *testing.T) {
	plan, err := pl.PlanCreateVolume(NewTable(nil), CreateVolumeRequest{Name: "z", Kind: Block, CapacityBytes: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}
	if _, set := plan[0].Props["refreservation"]; set || plan[0].Sparse {
		t.Fatalf("op %+v", plan[0])
	}
}

func TestPlanDeleteVolume(t *testing.T) {
	tab := NewTable([]Row{
		vol("a", nil),
		snap("a@s", nil),
		vol("b", nil),
		// an internal clone-source snapshot carries no ownership of its own
		snap("b@volume-source-for-volume-c", func(r *Row) { r.Managed, r.SnapshotName = false, "" }),
		vol("c", func(r *Row) { r.Origin = "tank/v/b@volume-source-for-volume-c" }),
		snap("d@x", func(r *Row) { r.Deferred = true }),
		vol("d", nil),
	})
	if _, err := pl.PlanDeleteVolume(tab, "a"); code(err) != FailedPrecondition {
		t.Fatalf("snapshotted: %v", err)
	}
	if _, err := pl.PlanDeleteVolume(tab, "b"); code(err) != FailedPrecondition {
		t.Fatalf("cloned: %v", err)
	}
	plan, err := pl.PlanDeleteVolume(tab, "c")
	if err != nil || len(plan) != 2 || !plan[1].Defer || plan[1].Target != "tank/v/b@volume-source-for-volume-c" {
		t.Fatalf("clone delete %+v %v", plan, err)
	}
	// a deferred (already deleted) snapshot does not block its volume
	if plan, err := pl.PlanDeleteVolume(tab, "d"); err != nil || len(plan) != 1 {
		t.Fatalf("deferred %+v %v", plan, err)
	}
	if plan, err := pl.PlanDeleteVolume(tab, "gone"); err != nil || plan != nil {
		t.Fatalf("missing %+v %v", plan, err)
	}
}

// review M2/M6
func TestPlanExpand(t *testing.T) {
	tab := NewTable([]Row{
		vol("unlimited", nil),
		vol("big", func(r *Row) { r.Type, r.Volsize, r.BlockSz = zfs.TypeVolume, 1<<20, 128<<10 }),
	})
	if plan, _, err := pl.PlanExpandVolume(tab, "unlimited", 1<<30, 0); err != nil || plan != nil {
		t.Fatalf("unlimited fs got a quota: %+v %v", plan, err)
	}
	plan, _, err := pl.PlanExpandVolume(tab, "big", 1<<20+1, 0)
	if err != nil || plan[0].Props["volsize"] != "1179648" { // next 128K multiple
		t.Fatalf("expand %+v %v", plan, err)
	}
}

// review H1: the internal prefix cannot be claimed by a CSI snapshot
func TestPlanSnapshotReservedPrefix(t *testing.T) {
	tab := NewTable([]Row{vol("a", nil)})
	if _, _, err := pl.PlanCreateSnapshot(tab, "volume-source-for-volume-x", "a"); code(err) != InvalidArgument {
		t.Fatal(err)
	}
}

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

// review (file-export adversary): a shared parent must not export new
// filesystems; zvols have no share properties
func TestPlanCreateFilesystemsAreNotShared(t *testing.T) {
	for _, r := range []CreateVolumeRequest{
		{Name: "fs", CapacityBytes: 1},
		{Name: "restored", SourceSnapshotID: "src@s"},
	} {
		tab := NewTable([]Row{vol("src", nil), snap("src@s", nil)})
		plan, err := pl.PlanCreateVolume(tab, r)
		if err != nil || plan[0].Props["sharenfs"] != "off" || plan[0].Props["sharesmb"] != "off" {
			t.Fatalf("%+v: %+v %v", r, plan, err)
		}
	}
	plan, err := pl.PlanCreateVolume(NewTable(nil), CreateVolumeRequest{Name: "z", Kind: Block, CapacityBytes: 1})
	if _, set := plan[0].Props["sharenfs"]; err != nil || set {
		t.Fatalf("zvol: %+v %v", plan, err)
	}
}
