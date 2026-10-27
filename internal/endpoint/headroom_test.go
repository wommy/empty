package endpoint

import (
	"context"
	"testing"

	"github.com/wommy/empty/internal/zfs"
)

func TestPlanReserves(t *testing.T) {
	t.Parallel()
	const G = 1 << 30
	tbl := NewTable([]Row{
		{Name: "tank/v/thick", ID: "thick", Type: zfs.TypeVolume, Volsize: 4 * G, Refres: 4 * G, BlockSz: 16384},
		{Name: "tank/v/thin", ID: "thin", Type: zfs.TypeVolume, Volsize: 4 * G, BlockSz: 16384},
		{Name: "tank/v/thin@s", ID: "thin@s", Type: zfs.TypeSnapshot, Volsize: 4 * G},
		{Name: "tank/v/grown", ID: "grown", Type: zfs.TypeVolume, Volsize: 40 * G, BlockSz: 16384},
		{Name: "tank/v/grown@s", ID: "grown@s", Type: zfs.TypeSnapshot, Volsize: 4 * G},
		{Name: "tank/v/fs", ID: "fs", Type: zfs.TypeFilesystem, Refquota: 2 * G, Refres: 2 * G},
	})
	set := func(target string, kv ...string) Op {
		m := map[string]string{}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return Op{Kind: OpSet, Target: target, Props: m}
	}
	commit := set("tank/v/new", "democratic-csi:provision_success", "true")
	for _, c := range []struct {
		name    string
		plan    Plan
		need    uint64
		creates bool
	}{
		{"empty plan (idempotent retry)", nil, 0, false},
		{"commit-only resume", Plan{commit}, 0, false},
		{"thick zvol create", Plan{{Kind: OpCreateVol, Target: "tank/v/new", Size: G}, commit}, G, true},
		{"sparse zvol create", Plan{{Kind: OpCreateVol, Target: "tank/v/new", Size: G, Sparse: true}, commit}, 0, true},
		{"thin filesystem", Plan{{Kind: OpCreateFS, Target: "tank/v/new", Props: map[string]string{"refquota": "1073741824"}}}, 0, true},
		{"reserved filesystem", Plan{{Kind: OpCreateFS, Target: "tank/v/new", Props: map[string]string{"refquota": "1073741824", "refreservation": "1073741824"}}}, G, true},
		{"thick clone of a snapshot: auto reserves the origin's volsize", Plan{
			{Kind: OpClone, Source: "tank/v/thin@s", Target: "tank/v/new"},
			set("tank/v/new", "refreservation", "auto"),
		}, 4 * G, true},
		{"thick clone of a snapshot older than its volume's expand: the snapshot's volsize", Plan{
			{Kind: OpClone, Source: "tank/v/grown@s", Target: "tank/v/new"},
			set("tank/v/new", "refreservation", "auto"),
		}, 4 * G, true},
		{"thick clone of the snapshot the plan cuts: the volume's volsize", Plan{
			{Kind: OpSnapshot, Target: "tank/v/grown@cut"},
			{Kind: OpClone, Source: "tank/v/grown@cut", Target: "tank/v/new"},
			set("tank/v/new", "refreservation", "auto"),
		}, 40 * G, true},
		{"thick clone grown at once", Plan{
			{Kind: OpClone, Source: "tank/v/thin@s", Target: "tank/v/new"},
			set("tank/v/new", "volsize", "6442450944", "refreservation", "auto"),
		}, 6 * G, true},
		{"expand a thick zvol: the delta", Plan{set("tank/v/thick", "volsize", "6442450944")}, 2 * G, false},
		{"expand a thin zvol", Plan{set("tank/v/thin", "volsize", "6442450944")}, 0, false},
		{"expand a reserved filesystem", Plan{set("tank/v/fs", "refquota", "3221225472", "refreservation", "3221225472")}, G, false},
		{"thin a thick zvol", Plan{set("tank/v/thick", "refreservation", "none")}, 0, false},
		{"thicken a thin zvol", Plan{set("tank/v/thin", "refreservation", "auto")}, 4 * G, false},
		{"destroys reserve nothing", Plan{{Kind: OpDestroy, Target: "tank/v/thick"}}, 0, false},
	} {
		need, creates := pl.planReserves(tbl, c.plan)
		if need != c.need || creates != c.creates {
			t.Errorf("%s: need %d creates %v, want %d %v", c.name, need, creates, c.need, c.creates)
		}
	}
}

func TestCheckHeadroom(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		avail, need, floor uint64
		refused            bool
	}{
		{avail: 0, need: 100, floor: 0},   // off
		{avail: 100, need: 50, floor: 50}, // exactly at the floor
		{avail: 100, need: 51, floor: 50, refused: true},
		{avail: 40, need: 0, floor: 50, refused: true},  // already below: a thin create
		{avail: 40, need: 100, floor: 1, refused: true}, // need above avail: no wrap
		{avail: ^uint64(0), need: 1, floor: 1},
	} {
		err := checkHeadroom(c.avail, c.need, c.floor, "create volume x")
		if refused := err != nil; refused != c.refused {
			t.Errorf("%+v: %v", c, err)
		}
		if err != nil && CodeOf(err) != ResourceExhausted {
			t.Errorf("%+v: code %d, want ResourceExhausted", c, CodeOf(err))
		}
	}
}

// End to end on the simulator: refused before anything is applied, never
// on a retry of what already exists, and off by default.
func TestMinFreeBytesGuard(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const G = 1 << 30
	ep, sim := simEndpoint(t, Config{MinFreeBytes: G})
	sim.PoolSize = 10 * G
	thick := CreateVolumeRequest{Name: "thick", Kind: Block, CapacityBytes: 9*G + G/2}
	before := len(sim.Calls)
	if _, err := ep.CreateVolume(ctx, thick); CodeOf(err) != ResourceExhausted {
		t.Fatalf("thick 9.5G on 10G with a 1G floor: %v, want ResourceExhausted", err)
	}
	for _, c := range sim.Calls[before:] {
		if c[1] != "get" && c[1] != "list" {
			t.Fatalf("refused create ran %v", c)
		}
	}
	thin := thick
	thin.Name, thin.Sparse = "thin", true
	if _, err := ep.CreateVolume(ctx, thin); err != nil {
		t.Fatalf("sparse create: %v", err)
	}
	ok := CreateVolumeRequest{Name: "ok", Kind: Block, CapacityBytes: 8 * G}
	if _, err := ep.CreateVolume(ctx, ok); err != nil {
		t.Fatalf("thick 8G: %v", err)
	}
	if _, err := ep.ExpandVolume(ctx, "ok", 9*G+G/2, 0); CodeOf(err) != ResourceExhausted {
		t.Fatalf("expand past the floor: %v", err)
	}
	// the pool is now below the floor: a retry of a committed create is
	// still OK, while a new thin create is refused
	sim.PoolSize = G / 2
	if _, err := ep.CreateVolume(ctx, ok); err != nil {
		t.Fatalf("idempotent retry below the floor: %v", err)
	}
	thin.Name = "thin2"
	if _, err := ep.CreateVolume(ctx, thin); CodeOf(err) != ResourceExhausted {
		t.Fatalf("new thin create below the floor: %v", err)
	}
	// a thick restore of a 4G snapshot whose volume has since grown to
	// 40G reserves 4G, not 40G
	ep, sim = simEndpoint(t, Config{MinFreeBytes: G})
	sim.PoolSize = 100 * G
	if _, err := ep.CreateVolume(ctx, CreateVolumeRequest{Name: "grown", Kind: Block, CapacityBytes: 4 * G, Sparse: true}); err != nil {
		t.Fatal(err)
	}
	snap, err := ep.CreateSnapshot(ctx, "before-grow", "grown")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ep.ExpandVolume(ctx, "grown", 40*G, 0); err != nil {
		t.Fatal(err)
	}
	sim.PoolSize = 10 * G
	if _, err := ep.CreateVolume(ctx, CreateVolumeRequest{Name: "restored", Kind: Block, CapacityBytes: 4 * G, SourceSnapshotID: snap.ID}); err != nil {
		t.Fatalf("thick 4G restore of a snapshot taken before its volume grew to 40G: %v", err)
	}
	// off by default
	off, offSim := simEndpoint(t, Config{})
	offSim.PoolSize = 0
	if _, err := off.CreateVolume(ctx, CreateVolumeRequest{Name: "a", Kind: Block, CapacityBytes: G, Sparse: true}); err != nil {
		t.Fatalf("guard off: %v", err)
	}
}

// On the simulator, with an operator's refreservation or refquota on the
// parent. Written to hold whether the simulator's available follows
// OpenZFS (adds back the parent's unused refreservation, caps at its
// refquota) or not (subtracts every reservation, ignores refquota).
func TestMinFreeBytesGuardParentSpace(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const G = 1 << 30
	// a 5G refreservation on the parent leaves its children 5G of 10G:
	// 4.5G thick would leave 0.5G, under the 1G floor
	ep, sim := simEndpoint(t, Config{MinFreeBytes: G})
	sim.PoolSize = 10 * G
	if err := ep.zfs.Set(ctx, rParent, map[string]string{"refreservation": "5368709120"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ep.CreateVolume(ctx, CreateVolumeRequest{Name: "big", Kind: Block, CapacityBytes: 4*G + G/2}); CodeOf(err) != ResourceExhausted {
		t.Fatalf("thick 4.5G beside the parent's unused 5G refreservation: %v, want ResourceExhausted", err)
	}
	// a 1G refquota on the parent limits what it references, not its
	// children: 4G thick of 10G passes
	ep, sim = simEndpoint(t, Config{MinFreeBytes: G})
	sim.PoolSize = 10 * G
	if err := ep.zfs.Set(ctx, rParent, map[string]string{"refquota": "1073741824"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ep.CreateVolume(ctx, CreateVolumeRequest{Name: "big", Kind: Block, CapacityBytes: 4 * G}); err != nil {
		t.Fatalf("thick 4G under a parent with a 1G refquota: %v", err)
	}
}

// TestChildAvail pins the guard's reading of the parent against OpenZFS:
// a new child gets dsl_dir_space_available of the parent's dir, while the
// parent's available (dsl_dataset_space) adds back the parent's own
// unused refreservation and is capped by its refquota, neither of which
// applies to children. s is the parent's properties as zfs get prints
// them; up is the same bound one level up (the grandparent).
func TestChildAvail(t *testing.T) {
	t.Parallel()
	const G = 1 << 30
	for _, c := range []struct {
		name  string
		s     parentSpace
		up    uint64
		hasUp bool
		want  uint64
	}{
		{name: "no reservations: available is the children's", s: parentSpace{avail: 10 * G}, want: 10 * G},
		// dir space 6G; available adds back the unused 4G refreservation
		{name: "the parent's unused refreservation is not the children's", s: parentSpace{avail: 10 * G, refres: 4 * G}, want: 6 * G},
		// dir space 5G, refreservation 5G of which 2G is taken by the
		// parent's own data (unique): available 8G. Without unique this
		// reads 3G, short by min(unique, refreservation): a lower bound
		{name: "a partly used refreservation: a lower bound", s: parentSpace{avail: 8 * G, refres: 5 * G}, want: 3 * G},
		{name: "refreservation above available: no wrap", s: parentSpace{avail: G, refres: 4 * G}, want: 0},
		// a quota is a dsl_dir limit: available already holds it
		{name: "a quota without a refquota", s: parentSpace{avail: 4 * G, quota: 6 * G, used: 2 * G}, want: 4 * G},
		// available is min(dir space, refquota - referenced) = 1G, while
		// the children get what the grandparent gives: 10G
		{name: "the parent's refquota does not limit children", s: parentSpace{avail: G, refquota: G}, up: 10 * G, hasUp: true, want: 10 * G},
		{name: "a refquota with an unused refreservation", s: parentSpace{avail: G / 2, refres: 4 * G, refquota: 5 * G, used: 4 * G}, up: 10 * G, hasUp: true, want: 10 * G},
		{name: "a refquota and a quota: the quota binds children", s: parentSpace{avail: G, refquota: G, quota: 6 * G, used: 2 * G}, up: 10 * G, hasUp: true, want: 4 * G},
		{name: "a refquota and a reservation: its unused part is the children's", s: parentSpace{avail: G, refquota: G, resv: 3 * G, used: G}, up: 5 * G, hasUp: true, want: 7 * G},
		{name: "a refquota over quota", s: parentSpace{avail: 0, refquota: G, quota: 2 * G, used: 3 * G}, up: 10 * G, hasUp: true, want: 0},
		{name: "a refquota on the pool root: the lower bound", s: parentSpace{avail: G, refres: G / 2, refquota: G}, want: G / 2},
		{name: "a lower bound above the walk is kept", s: parentSpace{avail: 3 * G, refquota: 5 * G}, up: G, hasUp: true, want: 3 * G},
	} {
		if got := childAvail(c.s, c.up, c.hasUp); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}
}
