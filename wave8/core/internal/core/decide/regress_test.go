package decide_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/wommy/empty/internal/core/code"
	"github.com/wommy/empty/internal/core/coretest"
	"github.com/wommy/empty/internal/core/decide"
	"github.com/wommy/empty/internal/core/name"
	"github.com/wommy/empty/internal/core/op"
	"github.com/wommy/empty/internal/core/req"
	"github.com/wommy/empty/internal/core/world"
	"github.com/wommy/empty/internal/zfs"
)

// Mechanism says how the core keeps a known bug from coming back.
type Mechanism string

const (
	Type  Mechanism = "type"  // the bad state or op cannot be built (compile, or a handle that cannot be minted)
	Parse Mechanism = "parse" // the world refuses the input zfs could not have printed
	Table Mechanism = "table" // a transition-table cell or the generated tests cover it
	Rule  Mechanism = "rule"  // a row of the error table
	Out   Mechanism = "out"   // outside CreateVolume/DeleteVolume: not ported in this prototype
)

type row struct {
	id, claim string
	how       Mechanism
	check     func(t *testing.T)
}

// The regression corpus (scratchpad CHECKPOINT.md and the reports it
// cites: wave5/impossible-reach.md F*, lifecycle-fsm.md CX*/I*,
// dod3/type12.md D*, impossible-types.md R*/Q*/O*), as rows.
func corpus() []row {
	v := parent + "/v"
	return []row{
		{"F1", "destroy -r took an unowned child of a volume", Table, func(t *testing.T) {
			sc, m := setupCommitted(t, "v", false)
			_ = m.Admin("create", v+"/user-data")
			wantRefusal(t, m, sc, "v", code.FailedPrecondition)
			sc2, m2 := setupCommitted(t, "v", false)
			p, _, _ := decideDelete(t, m2, sc2, "v", decide.Policy{})
			assertNoRecursive(t, p)
		}},
		{"F2", "volsize '-'/garbage read as 0 let Expand shrink", Parse, nil /* world_test: 7 geometry rows */},
		{"F3/CX4", "clone resume forgot the source quota", Table, func(t *testing.T) {
			sc := schema(t)
			m := world0(t, sc)
			in := req.CreateVolumeIn{Name: "v", SourceSnapshotID: "sf@s"}
			p, _, _ := decideCreate(t, m, sc, in)
			_ = apply(m, p[:1]) // crash right after the clone
			p2, ref, _ := decideCreate(t, m, sc, in)
			if ref != nil || apply(m, p2) != nil {
				t.Fatal(ref)
			}
			if got := volCap(t, m, sc, "v"); got != GiB {
				t.Fatalf("refquota %d after resume, want the source's %d", got, GiB)
			}
		}},
		{"F4/CX3", "clone OkExists adopted a racing foreign dataset", Type, func(t *testing.T) {
			sc := schema(t)
			m := world0(t, sc)
			p, _, _ := decideCreate(t, m, sc, req.CreateVolumeIn{Name: "v", SourceVolumeID: "sf", Reserve: true, Capacity: GiB})
			for _, o := range p {
				if o.Kind() == op.KClone && o.Tol() != op.TolNone {
					t.Fatal("clone tolerates EEXIST")
				}
			}
			_ = m.Admin("create", v) // the race
			if ref := apply(m, p); ref == nil || ref.Code != code.AlreadyExists {
				t.Fatalf("racing dataset: %v", ref)
			}
			if strings.Contains(m.Dump(), v+" f origin= deferred=false zfs-endpoint") {
				t.Fatal("ownership stamped on the foreign dataset")
			}
		}},
		{"F5", "clone outside the parent was invisible; destroy -r wedged reconcile", Table, func(t *testing.T) {
			sc, m := setupCommitted(t, "v", false)
			_ = m.Admin("snapshot", v+"@manual")
			_ = m.Admin("create", "tank/backup")
			_ = m.Admin("clone", v+"@manual", "tank/backup/x")
			wantRefusal(t, m, sc, "v", code.FailedPrecondition) // seen through the snapshot's clones property
		}},
		{"F6", "origin release destroyed a snapshot outside the tree / of another id", Type, func(t *testing.T) {
			sc, m := setupCommitted(t, "v", false)
			_ = m.Admin("create", "tank/archive")
			_ = m.Admin("create", "tank/archive/s")
			_ = m.Admin("snapshot", "tank/archive/s@"+world.CloneSourcePrefix+"v")
			_ = m.Admin("clone", "tank/archive/s@"+world.CloneSourcePrefix+"v", parent+"/w")
			m.SetLocal(parent+"/w", mk(":managed_resource"), "true")
			m.SetLocal(parent+"/w", mk(":csi_volume_name"), "w")
			p, ref, _ := decideDelete(t, m, sc, "w", decide.Policy{})
			if ref != nil {
				t.Fatal(ref)
			}
			for _, o := range p {
				if !strings.HasPrefix(o.Target(), parent+"/") {
					t.Fatalf("op outside the parent: %v", o)
				}
			}
		}},
		{"F7/R4/R5", "type disagreeing with the name was listed as a volume", Parse, nil /* world_test: 1 type rows */},
		{"F8", "zvol snapshot without its volume row", Parse, nil /* world_test: snapshot without its dataset */},
		{"F9", "names zfs accepts but ids refuse wedged reconcile", Parse, nil /* world_test: TestUnaddressableNamesAreDropped */},
		{"F10", "capacity near 2^64 wrapped to 0 in roundTo", Type, func(t *testing.T) {
			for _, c := range []uint64{1<<64 - 1, 1<<64 - 16383, 1 << 63} {
				if _, ref := req.ParseCreateVolume(req.CreateVolumeIn{Name: "v", Block: true, Capacity: c}, req.BlockSize{}); ref == nil || ref.Code != code.OutOfRange {
					t.Fatalf("%d: %v", c, ref)
				}
			}
		}},
		{"F11/I8", "CSI name without managed answered both success and FailedPrecondition", Parse, nil /* world_test: TestSnapshotOwnership I8 */},
		{"F12", "uncommitted rows served by Get/Expand", Type, func(t *testing.T) {
			sc := schema(t)
			m := world0(t, sc)
			p, _, _ := decideCreate(t, m, sc, req.CreateVolumeIn{Name: "v", Capacity: GiB})
			_ = apply(m, p[:len(p)-1])
			id, _ := name.ParseVolID("v")
			w := scan(t, m, sc, world.Vols(false, id))
			if _, ok := w.Committed(id); ok {
				t.Fatal("a CommittedVol handle for an uncommitted row")
			}
		}},
		{"F13/F14", "etab and nfs_clients parse gaps", Out, nil},
		{"F15/C2", "zero Planner volblocksize divided by zero", Type, func(t *testing.T) {
			r, ref := req.ParseCreateVolume(req.CreateVolumeIn{Name: "v", Block: true, Capacity: 1}, req.BlockSize{})
			if ref != nil || r.DefaultBS != 16384 {
				t.Fatal(ref, r.DefaultBS)
			}
		}},
		{"F16", "TOCTOU: name re-created between scan and op", Out, nil /* guid is in the world; guarded destroy is the channel-program follow-up */},
		{"CX1", "DeleteVolume crash leaked the clone source", Table, nil /* TestDeleteCells: every crash prefix, 'clone source leaked' */},
		{"CX2", "a sanoid snapshot racing the scan was destroyed by -r", Type, func(t *testing.T) {
			sc, m := setupCommitted(t, "v", false)
			p, ref, _ := decideDelete(t, m, sc, "v", decide.Policy{})
			if ref != nil {
				t.Fatal(ref)
			}
			_ = m.Admin("snapshot", v+"@autosnap") // the race
			if ref := apply(m, p); ref == nil || ref.Code != code.FailedPrecondition {
				t.Fatalf("got %v", ref)
			}
			if !m.Has(v + "@autosnap") {
				t.Fatal("racing snapshot destroyed")
			}
		}},
		{"CX5/D05", "received copies counted as owned", Parse, func(t *testing.T) {
			sc := schema(t)
			m := world0(t, sc)
			_ = m.Admin("create", v)
			for _, k := range []string{":managed_resource", ":provision_success"} {
				m.Receive(v, mk(k), "true")
			}
			m.Receive(v, mk(":csi_volume_name"), "v")
			wantRefusal(t, m, sc, "v", code.FailedPrecondition)
		}},
		{"CX6/D02", "a killed command after a mutation answered Internal (final)", Rule, func(t *testing.T) {
			kill := &zfs.Error{Kind: zfs.KindInterrupted, Err: context.DeadlineExceeded}
			if o := decide.Map(op.KCommit, kill, true); o.Code != code.DeadlineExceeded {
				t.Fatal(o)
			}
			if o := decide.Map(op.KSet, errors.New("?"), true); o.Code != code.Aborted {
				t.Fatal(o)
			}
			if o := decide.Map(op.KCreateFS, errors.New("?"), false); o.Code != code.Internal {
				t.Fatal(o)
			}
		}},
		{"D01/D12", "busy mapped by kind only; permission unmapped", Rule, func(t *testing.T) {
			busy := &zfs.Error{Kind: zfs.KindBusy}
			if decide.Map(op.KDestroyVol, busy, false).Code != code.FailedPrecondition || decide.Map(op.KSet, busy, true).Code != code.Unavailable {
				t.Fatal("busy")
			}
			if decide.Map(op.KCreateFS, &zfs.Error{Kind: zfs.KindPermission}, false).Code != code.FailedPrecondition {
				t.Fatal("permission")
			}
		}},
		{"D04", "a missing parent answered NotFound (final)", Rule, func(t *testing.T) {
			if decide.Map(op.KCreateFS, &zfs.Error{Kind: zfs.KindParentNotFound}, false).Code != code.Unavailable {
				t.Fatal("parent")
			}
		}},
		{"D07", "an unlimited filesystem was AlreadyExists for any required size", Table, func(t *testing.T) {
			sc, m := setupCommitted(t, "v", false)
			p, ref, _ := decideCreate(t, m, sc, req.CreateVolumeIn{Name: "v", Capacity: GiB})
			if ref != nil || len(p) != 0 {
				t.Fatal(p, ref)
			}
		}},
		{"D09/I13", "clone from an unowned source", Type, func(t *testing.T) {
			sc := schema(t)
			m := world0(t, sc)
			_ = m.Admin("snapshot", parent+"/sf@autosnap")
			for _, in := range []req.CreateVolumeIn{{Name: "v", SourceSnapshotID: "sf@autosnap"}, {Name: "v", SourceSnapshotID: "nope@s"}} {
				if _, ref, _ := decideCreate(t, m, sc, in); ref == nil || ref.Code != code.NotFound {
					t.Fatal(in, ref)
				}
			}
		}},
		{"D14", "a malformed DeleteVolume id was InvalidArgument", Type, func(t *testing.T) {
			if _, parsed, _ := req.ParseDeleteVolume("tank/vols/pvc-1"); parsed != req.NeverIssued {
				t.Fatal(parsed)
			}
		}},
		{"D17/dod3-F5", "phantom rows forged by a newline in a user value", Parse, nil /* world_test: TestPhantomRowsCannotBeForged */},
		{"I1/I2/I3", "marker combinations nobody writes treated as owned", Parse, nil /* world_test: TestOwnershipIsDerivedOnce */},
		{"I14", "snapshot or expand of an uncommitted volume", Out, nil /* CreateSnapshot/Expand take CommittedVol in the next step */},
		{"R7", "four drifting definitions of 'ours'", Type, nil /* one classify per kind in world/parse.go; handles carry it */},
		{"R8", "csi_volume_name literally '-' read as unset", Parse, nil /* world_test: TestOwnershipIsDerivedOnce */},
		{"Q1/Q2", "VolumeKind(7); sparse on fs; reserve on zvol; two sources", Type, func(t *testing.T) {
			for _, in := range []req.CreateVolumeIn{{Name: "v", Sparse: true}, {Name: "v", Block: true, Reserve: true, Capacity: 1}, {Name: "v", SourceVolumeID: "a", SourceSnapshotID: "a@b"}} {
				if _, ref := req.ParseCreateVolume(in, req.BlockSize{}); ref == nil || ref.Code != code.InvalidArgument {
					t.Fatal(in, ref)
				}
			}
		}},
		{"O1/O2/O3", "op fields that do not fit the kind; free tolerance flags; unnamed zero kind", Type, nil /* per-kind op structs; Tol() is a method of the kind; KInvalid named */},
		{"T1", "a partial table planned a delete that needs the whole parent", Type, nil /* Bound[R]: world.Bind refuses; DeleteVolume needs only the subtree (clones property) */},
		{"E1", "a *Error with Code OK reported success", Type, func(t *testing.T) {
			if code.Refuse(code.OK, "x").Code != code.Internal {
				t.Fatal("OK refusal")
			}
		}},
		{"N1 (new)", "resume committed a thick zvol for a sparse request (HEAD too)", Table, func(t *testing.T) {
			sc := schema(t)
			m := world0(t, sc)
			p, _, _ := decideCreate(t, m, sc, req.CreateVolumeIn{Name: "v", Block: true, Capacity: GiB})
			_ = apply(m, p[:len(p)-1])
			if _, ref, _ := decideCreate(t, m, sc, req.CreateVolumeIn{Name: "v", Block: true, Capacity: GiB, Sparse: true}); ref == nil || ref.Code != code.AlreadyExists {
				t.Fatal(ref)
			}
		}},
	}
}

func TestRegressionCorpus(t *testing.T) {
	counts := map[Mechanism]int{}
	for _, r := range corpus() {
		counts[r.how]++
		t.Run(r.id, func(t *testing.T) {
			if r.check != nil {
				r.check(t)
			}
		})
	}
	t.Logf("rows by mechanism: %v", counts)
}

func setupCommitted(t *testing.T, id string, block bool) (*world.Schema, *coretest.Model) {
	sc := schema(t)
	m := world0(t, sc)
	p, ref, _ := decideCreate(t, m, sc, req.CreateVolumeIn{Name: id, Block: block, Capacity: 0, Sparse: block})
	if block {
		p, ref, _ = decideCreate(t, m, sc, req.CreateVolumeIn{Name: id, Block: true, Capacity: GiB, Sparse: true})
	}
	if ref != nil || apply(m, p) != nil {
		t.Fatal(ref)
	}
	return sc, m
}

func wantRefusal(t *testing.T, m *coretest.Model, sc *world.Schema, id string, c code.Code) {
	t.Helper()
	p, ref, _ := decideDelete(t, m, sc, id, decide.Policy{DestroyForeignSnapshots: true})
	if ref == nil || ref.Code != c {
		t.Fatalf("want %v, got %v %v", c, p, ref)
	}
}

// no op renders -r: the model also refuses it
func assertNoRecursive(t *testing.T, p op.Plan) {
	if len(p) == 0 {
		t.Fatal("empty plan")
	}
	for _, o := range p {
		for _, a := range o.Argv() {
			if a == "-r" || a == "-R" {
				t.Fatalf("%v renders %s", o.Kind(), a)
			}
		}
	}
}

func volCap(t *testing.T, m *coretest.Model, sc *world.Schema, id string) uint64 {
	v, _ := name.ParseVolID(id)
	w := scan(t, m, sc, world.Vols(false, v))
	vv, _ := w.Vol(v)
	return vv.Capacity()
}
