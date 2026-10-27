package endpoint

import (
	"maps"
	"slices"
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

// A dataset under a volume's name that is not that volume (unmanaged, or
// managed with another or no volume-name marker: a rename, a hand-set
// marker) is never answered as "already done", and an uncommitted one is
// never converged and committed (ADR 0003).
func TestPlanCreateRefusesForeignAndIncompatible(t *testing.T) {
	t.Parallel()
	tab := NewTable([]Row{
		vol("foreign", func(r *Row) { r.Managed = false }),
		vol("renamed", func(r *Row) { r.VolumeName = "orig" }),
		vol("renamed-half", func(r *Row) { r.VolumeName, r.Committed = "orig", false }),
		vol("nameless", func(r *Row) { r.VolumeName = "" }),
		vol("nameless-half", func(r *Row) { r.VolumeName, r.Committed = "", false }),
		vol("small", func(r *Row) { r.Refquota = 10 }),
		vol("thin", func(r *Row) { r.Type, r.Volsize = zfs.TypeVolume, 16384 }),
	})
	for _, r := range []CreateVolumeRequest{
		{Name: "foreign"},
		{Name: "renamed"},
		{Name: "renamed-half"},
		{Name: "nameless"},
		{Name: "nameless-half"},
		{Name: "small", CapacityBytes: 11},
		{Name: "small", Kind: Block, CapacityBytes: 1},
		{Name: "thin", Kind: Block, CapacityBytes: 16384, Sparse: false}, // thick requested
	} {
		if plan, err := pl.PlanCreateVolume(tab, r); code(err) != AlreadyExists || plan != nil {
			t.Errorf("%+v: %+v %v, want AlreadyExists and no plan", r, plan, err)
		}
	}
}

// The CSI code of every planner refusal, one row each: callers (the CSI
// sidecars) retry or give up by code, so "an error" is not enough. Rows
// with want OK are the boundary controls of a neighbouring refusal.
func TestPlannerErrorCodes(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("s", 120)
	zvol := func(r *Row) { r.Type, r.Volsize, r.BlockSz, r.Refres = zfs.TypeVolume, 1<<20, 16384, 1<<20 }
	tab := NewTable([]Row{
		vol("a", func(r *Row) { r.Refquota = 100 }),
		vol("ares", func(r *Row) { r.Refquota, r.Refres = 100, 100 }),
		vol("unl", nil),
		vol(long, nil),
		vol("src", nil), snap("src@s", nil),
		vol("zv", zvol), snap("zv@s", func(r *Row) { r.Volsize = 1 << 20 }),
		// clone sources that are not committed volumes the endpoint owns
		vol("unmanaged", func(r *Row) { r.Managed = false }), snap("unmanaged@s", nil),
		vol("renamed", func(r *Row) { r.VolumeName = "orig" }), snap("renamed@s", nil),
		vol("nameless", func(r *Row) { r.VolumeName = "" }), snap("nameless@s", nil),
		vol("half", func(r *Row) { r.Committed = false }), snap("half@s", nil),
		snap("src@admin", func(r *Row) { r.Managed, r.SnapshotName = false, "" }),
		snap("src@gone", func(r *Row) { r.Deferred = true }),
		vol("sparse", func(r *Row) { r.Type, r.Volsize, r.BlockSz = zfs.TypeVolume, 1<<20, 16384 }),
		snap("a@deleted", func(r *Row) { r.SnapshotName, r.SnapshotSource, r.Deferred = "deleted", "a", true }),
	})
	create := func(r CreateVolumeRequest) func() error {
		return func() error { _, err := pl.PlanCreateVolume(tab, r); return err }
	}
	fromSnap := func(src string) func() error {
		return create(CreateVolumeRequest{Name: "n", CapacityBytes: 1, SourceSnapshotID: src})
	}
	fromVol := func(src string) func() error {
		return create(CreateVolumeRequest{Name: "n", CapacityBytes: 1, SourceVolumeID: src})
	}
	for _, tc := range []struct {
		name string
		err  func() error
		want Code
	}{
		{"two content sources", create(CreateVolumeRequest{Name: "n", CapacityBytes: 1, SourceSnapshotID: "src@s", SourceVolumeID: "src"}), InvalidArgument},
		{"block without capacity", create(CreateVolumeRequest{Name: "n", Kind: Block}), InvalidArgument},
		{"capacity above the limit", create(CreateVolumeRequest{Name: "n", CapacityBytes: 200, LimitBytes: 100}), OutOfRange},
		{"existing sparse zvol, sparse again", create(CreateVolumeRequest{Name: "sparse", Kind: Block, CapacityBytes: 1 << 20, Sparse: true}), OK},
		{"existing sparse zvol, thick requested", create(CreateVolumeRequest{Name: "sparse", Kind: Block, CapacityBytes: 1 << 20}), AlreadyExists},
		{"snapshot name of one pending deletion", func() error { _, _, err := pl.PlanCreateSnapshot(tab, "deleted", "a"); return err }, AlreadyExists},
		{"sparse filesystem", create(CreateVolumeRequest{Name: "n", CapacityBytes: 1, Sparse: true}), InvalidArgument},
		{"reserved zvol", create(CreateVolumeRequest{Name: "n", Kind: Block, CapacityBytes: 1, Reserve: true}), InvalidArgument},
		{"reserve without size", create(CreateVolumeRequest{Name: "n", Reserve: true}), InvalidArgument},
		{"clone source snapshot name too long", create(CreateVolumeRequest{Name: strings.Repeat("d", 128), CapacityBytes: 1, SourceVolumeID: long}), InvalidArgument},
		{"filesystem from a zvol snapshot", fromSnap("zv@s"), InvalidArgument},
		{"filesystem from a zvol", fromVol("zv"), InvalidArgument},
		{"zvol from a filesystem snapshot", create(CreateVolumeRequest{Name: "n", Kind: Block, CapacityBytes: 1, SourceSnapshotID: "src@s"}), InvalidArgument},
		{"clone source above the limit", create(CreateVolumeRequest{Name: "n", Kind: Block, CapacityBytes: 1, LimitBytes: 1<<20 - 16384, SourceSnapshotID: "zv@s"}), OutOfRange},
		{"clone source exactly the limit", create(CreateVolumeRequest{Name: "n", Kind: Block, CapacityBytes: 1 << 20, LimitBytes: 1 << 20, SourceSnapshotID: "zv@s"}), OK},
		{"clone from snapshot of an owned volume", fromSnap("src@s"), OK},
		{"clone from snapshot of an unmanaged volume", fromSnap("unmanaged@s"), NotFound},
		{"clone from snapshot of a renamed volume", fromSnap("renamed@s"), NotFound},
		{"clone from snapshot of a nameless volume", fromSnap("nameless@s"), NotFound},
		{"clone from snapshot of an uncommitted volume", fromSnap("half@s"), NotFound},
		{"clone from an administrator's snapshot", fromSnap("src@admin"), NotFound},
		{"clone from a deleted snapshot", fromSnap("src@gone"), NotFound},
		{"clone from an owned volume", fromVol("src"), OK},
		{"clone from an unmanaged volume", fromVol("unmanaged"), NotFound},
		{"clone from a renamed volume", fromVol("renamed"), NotFound},
		{"clone from a nameless volume", fromVol("nameless"), NotFound},
		{"clone from an uncommitted volume", fromVol("half"), NotFound},
		{"existing volume larger than limit", create(CreateVolumeRequest{Name: "a", CapacityBytes: 50, LimitBytes: 60}), AlreadyExists},
		{"existing unlimited volume under a limit", create(CreateVolumeRequest{Name: "unl", LimitBytes: 100}), AlreadyExists},
		{"existing volume without the requested reservation", create(CreateVolumeRequest{Name: "a", CapacityBytes: 100, Reserve: true}), AlreadyExists},
		{"existing volume with an unrequested reservation", create(CreateVolumeRequest{Name: "ares", CapacityBytes: 100}), AlreadyExists},
		{"delete of a renamed volume", func() error { _, err := pl.PlanDeleteVolume(tab, "renamed"); return err }, FailedPrecondition},
		{"delete of a nameless volume", func() error { _, err := pl.PlanDeleteVolume(tab, "nameless"); return err }, FailedPrecondition},
		{"expand of a renamed volume", func() error { _, _, err := pl.PlanExpandVolume(tab, "renamed", 200, 0); return err }, NotFound},
		{"expand beyond limit", func() error { _, _, err := pl.PlanExpandVolume(tab, "a", 200, 100); return err }, OutOfRange},
		{"empty snapshot name", func() error { _, _, err := pl.PlanCreateSnapshot(tab, "", "a"); return err }, InvalidArgument},
		{"snapshot of a renamed volume", func() error { _, _, err := pl.PlanCreateSnapshot(tab, "x", "renamed"); return err }, NotFound},
	} {
		if err := tc.err(); code(err) != tc.want {
			t.Errorf("%s: got %v (%v), want code %d", tc.name, code(err), err, tc.want)
		}
	}
}

// An owned but uncommitted volume (an interrupted provision) converges to
// the request: a smaller quota grows, a reservation is set only when below
// the request and never lowered.
func TestPlanCreateConvergesUncommittedSize(t *testing.T) {
	t.Parallel()
	half := func(quota, res uint64) Row {
		return vol("h", func(r *Row) { r.Committed, r.Refquota, r.Refres = false, quota, res })
	}
	for _, tc := range []struct {
		name string
		row  Row
		req  CreateVolumeRequest
		set  map[string]string // the size op's props; nil: commit only
	}{
		{"smaller quota grows", half(50, 0), CreateVolumeRequest{Name: "h", CapacityBytes: 100}, map[string]string{"refquota": "100"}},
		{"unlimited gets the quota", half(0, 0), CreateVolumeRequest{Name: "h", CapacityBytes: 100}, map[string]string{"refquota": "100"}},
		{"larger quota is kept", half(200, 0), CreateVolumeRequest{Name: "h", CapacityBytes: 100}, nil},
		{"missing reservation is set", half(100, 0), CreateVolumeRequest{Name: "h", CapacityBytes: 100, Reserve: true}, map[string]string{"refreservation": "100"}},
		{"larger reservation is never lowered", half(100, 200), CreateVolumeRequest{Name: "h", CapacityBytes: 100, Reserve: true}, nil},
		{"equal reservation is not re-set", half(100, 100), CreateVolumeRequest{Name: "h", CapacityBytes: 100, Reserve: true}, nil},
	} {
		plan, err := pl.PlanCreateVolume(NewTable([]Row{tc.row}), tc.req)
		n := 1
		if tc.set != nil {
			n = 2
		}
		if err != nil || len(plan) != n || plan[n-1].Props[pl.Names.success()] != "true" ||
			tc.set != nil && !maps.Equal(plan[0].Props, tc.set) {
			t.Errorf("%s: plan %+v err %v, want size op %v then commit", tc.name, plan, err, tc.set)
		}
	}
}

// DeleteSnapshot: absent and already-deleted (deferred) snapshots are done,
// whoever's they are, with no op; a live snapshot the endpoint did not cut
// is refused; an owned one is destroyed deferred.
func TestPlanDeleteSnapshot(t *testing.T) {
	t.Parallel()
	foreign := func(r *Row) { r.Managed, r.SnapshotName = false, "" }
	tab := NewTable([]Row{
		vol("a", nil),
		snap("a@own", nil),
		snap("a@owngone", func(r *Row) { r.Deferred = true }),
		snap("a@admin", foreign),
		snap("a@admingone", func(r *Row) { foreign(r); r.Deferred = true }),
	})
	for _, tc := range []struct {
		id       string
		want     Code
		deferred bool // one deferred destroy of the snapshot, else no op
	}{
		{"a@own", OK, true},
		{"a@owngone", OK, false},
		{"a@admin", FailedPrecondition, false},
		{"a@admingone", OK, false},
		{"a@missing", OK, false},
		{"not-an-id", OK, false},
	} {
		plan, err := pl.PlanDeleteSnapshot(tab, tc.id)
		one := len(plan) == 1 && plan[0].Kind == OpDestroy && plan[0].Defer && plan[0].OkMissing && plan[0].Target == "tank/v/"+tc.id
		if code(err) != tc.want || tc.deferred != one || !tc.deferred && plan != nil {
			t.Errorf("%s: plan %+v err %v, want code %d deferred destroy %v", tc.id, plan, err, tc.want, tc.deferred)
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
	// the released origin first (deferred), then the volume, never -r
	plan, err := pl.PlanDeleteVolume(tab, "c")
	if err != nil || len(plan) != 2 || !plan[0].Defer || plan[0].Target != "tank/v/b@volume-source-for-volume-c" ||
		plan[1].Target != "tank/v/c" || plan[1].Recursive || plan[1].Defer {
		t.Fatalf("clone delete %+v %v", plan, err)
	}
	// a deferred (already deleted) snapshot does not block its volume; it
	// is destroyed by name, plainly, before the volume
	if plan, err := pl.PlanDeleteVolume(tab, "d"); err != nil || len(plan) != 2 ||
		plan[0].Target != "tank/v/d@x" || plan[0].Defer || plan[1].Target != "tank/v/d" || plan[1].Recursive {
		t.Fatalf("deferred %+v %v", plan, err)
	}
	if plan, err := pl.PlanDeleteVolume(tab, "gone"); err != nil || plan != nil {
		t.Fatalf("missing %+v %v", plan, err)
	}
}

// Deleting a clone releases its origin only when that origin is the
// endpoint's clone-source snapshot for this clone: stamped for it, or a
// legacy bare one under the reserved name. Anything else it was cloned
// from stays.
func TestPlanDeleteVolumeReleasesOnlyItsOwnOrigin(t *testing.T) {
	t.Parallel()
	const origin = "b@" + cloneSourceSnapshotPrefix + "c"
	for _, tc := range []struct {
		name    string
		origin  Row
		src     func(*Row)
		release bool
	}{
		{"stamped for c", snap(origin, stamp), nil, true},
		{"legacy bare under the reserved name", snap(origin, bare), nil, true},
		{"stamped for another clone", snap(origin, func(r *Row) { stamp(r); r.CloneSourceFor = "d" }), nil, false},
		{"unmanaged, with a CSI name", snap(origin, func(r *Row) { bare(r); r.SnapshotName = "x" }), nil, false},
		{"an administrator's snapshot", snap("b@admin", bare), nil, false},
		{"on an unmanaged volume", snap(origin, stamp), func(r *Row) { r.Managed = false }, false},
	} {
		rows := []Row{
			vol("b", tc.src), tc.origin,
			vol("c", func(r *Row) { r.Origin = "tank/v/" + tc.origin.ID }),
		}
		plan, err := pl.PlanDeleteVolume(NewTable(rows), "c")
		var got []string
		for _, op := range plan {
			got = append(got, op.Target)
		}
		want := []string{"tank/v/c"}
		if tc.release {
			want = []string{"tank/v/" + tc.origin.ID, "tank/v/c"}
		}
		if err != nil || !slices.Equal(got, want) || tc.release && !plan[0].Defer {
			t.Errorf("%s: plan %+v err %v, want %v", tc.name, plan, err, want)
		}
	}
}

// Crash window: CreateVolume(c from a volume) cut src@volume-source-for-
// volume-c and died before the clone, and the caller then deleted c. The
// delete of the absent c releases that snapshot (destroy -d, like a normal
// clone delete) when it is ours: stamped for c, on a volume the endpoint
// owns, unused. Anything else with the reserved name is left alone.
func TestPlanDeleteAbsentVolumeReleasesItsCloneSource(t *testing.T) {
	t.Parallel()
	const prefix = cloneSourceSnapshotPrefix
	for _, tc := range []struct {
		name string
		rows []Row
		want []string // deferred destroys, in order
	}{
		{"stamped for c", []Row{vol("src", nil), snap("src@"+prefix+"c", stamp)}, []string{"tank/v/src@" + prefix + "c"}},
		{"stamped for c on two sources", []Row{
			vol("a", nil), snap("a@"+prefix+"c", stamp), vol("b", nil), snap("b@"+prefix+"c", stamp),
		}, []string{"tank/v/a@" + prefix + "c", "tank/v/b@" + prefix + "c"}},
		{"nothing cut", []Row{vol("src", nil)}, nil},
		{"reserved name only (bare)", []Row{vol("src", nil), snap("src@"+prefix+"c", bare)}, nil},
		{"stamped for another clone", []Row{vol("src", nil), snap("src@"+prefix+"c", func(r *Row) { stamp(r); r.CloneSourceFor = "d" })}, nil},
		{"a CSI snapshot under the reserved name", []Row{vol("src", nil), snap("src@"+prefix+"c", func(r *Row) { stamp(r); r.SnapshotName = "x" })}, nil},
		{"on an unmanaged volume", []Row{vol("src", func(r *Row) { r.Managed = false }), snap("src@"+prefix+"c", stamp)}, nil},
		{"on a renamed volume", []Row{vol("src", func(r *Row) { r.VolumeName = "orig" }), snap("src@"+prefix+"c", stamp)}, nil},
		{"in use by a clone", []Row{
			vol("src", nil), snap("src@"+prefix+"c", stamp), vol("other", func(r *Row) { r.Origin = "tank/v/src@" + prefix + "c" }),
		}, nil},
		{"already released", []Row{vol("src", nil), snap("src@"+prefix+"c", func(r *Row) { stamp(r); r.Deferred = true })}, nil},
	} {
		plan, err := pl.PlanDeleteVolume(NewTable(tc.rows), "c")
		var got []string
		for _, op := range plan {
			if op.Kind != OpDestroy || !op.Defer || !op.OkMissing || op.Recursive {
				t.Errorf("%s: op %+v, want destroy -d, missing ok", tc.name, op)
			}
			got = append(got, op.Target)
		}
		if err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("%s: plan %v err %v, want %v", tc.name, got, err, tc.want)
		}
	}
	// the snapshot can hang off any volume: a table read for c alone cannot
	// say there is none
	if _, err := pl.PlanDeleteVolume(newPartialTable(nil, "c"), "c"); code(err) != Internal {
		t.Errorf("partial table: %v, want Internal", err)
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
	// an id of exactly 128 bytes keeps the name itself
	exact := strings.Repeat("e", 126)
	if _, id, err := pl.PlanCreateSnapshot(NewTable([]Row{vol("a", nil)}), exact, "a"); err != nil || id != "a@"+exact {
		t.Fatalf("128-byte id: %q %v", id, err)
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
