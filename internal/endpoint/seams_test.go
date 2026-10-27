package endpoint

// Wave-12 seam regressions (S2, S3, S4, S6, S8). Each was a probe that
// passed while the disagreement was present; these assert the fixed
// behaviour, at the lowest level that proves it (ADR 0005).

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

// S2: a zvol clone keeps its source's volblocksize, so its size rounds to
// that, not to the planner's 16K default: zfs refuses a volsize that is
// not a multiple of the zvol's own block size.
func TestPlanCloneRoundsToSourceBlockSize(t *testing.T) {
	t.Parallel()
	const k128 = 128 << 10
	src := func(bs uint64) []Row {
		return []Row{
			vol("src", func(r *Row) { r.Type, r.Volsize, r.BlockSz, r.Refres = zfs.TypeVolume, 1<<30, bs, 1<<30 }),
			snap("src@s", func(r *Row) { r.Volsize, r.BlockSz, r.SnapshotSource = 1<<30, bs, "src" }),
		}
	}
	for _, c := range []struct {
		name     string
		bs       uint64
		req      CreateVolumeRequest
		wantSize string // "" = no volsize op
		code     Code
	}{
		{"volume source, 2e9 on 128K", k128,
			CreateVolumeRequest{Name: "c", Kind: Block, CapacityBytes: 2_000_000_000, SourceVolumeID: "src"}, "2000027648", OK},
		{"snapshot source, 2e9 on 128K", k128,
			CreateVolumeRequest{Name: "c", Kind: Block, CapacityBytes: 2_000_000_000, SourceSnapshotID: "src@s"}, "2000027648", OK},
		// the limit is checked against the source-rounded size: a 16K
		// rounding (2000011264) fits it, the clone's real size does not
		{"limit below the 128K-rounded size", k128,
			CreateVolumeRequest{Name: "c", Kind: Block, CapacityBytes: 2_000_000_000, LimitBytes: 2000011264, SourceVolumeID: "src"}, "", OutOfRange},
		// an 8K source: 24K is exact, and rounding to the 16K default
		// (32K) must not refuse a limit the clone meets
		{"8K source, exact size at the limit", 8 << 10,
			CreateVolumeRequest{Name: "c", Kind: Block, CapacityBytes: 2 << 30, LimitBytes: 2 << 30, SourceVolumeID: "src"}, "2147483648", OK},
		{"8K source, 24K never shrinks below the source", 8 << 10,
			CreateVolumeRequest{Name: "c", Kind: Block, CapacityBytes: 24 << 10, LimitBytes: 1 << 30, SourceVolumeID: "src"}, "", OK},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := pl.validateCreate(c.req); err != nil && c.code == OK {
				t.Fatalf("validateCreate: %v", err)
			}
			plan, err := pl.PlanCreateVolume(NewTable(src(c.bs)), c.req)
			if CodeOf(err) != c.code {
				t.Fatalf("code %v (%v), want %v", CodeOf(err), err, c.code)
			}
			if err != nil {
				return
			}
			got := ""
			for _, op := range plan {
				if v, ok := op.Props["volsize"]; ok {
					got = v
				}
			}
			if got != c.wantSize {
				t.Fatalf("volsize %q, want %q (plan %+v)", got, c.wantSize, plan)
			}
		})
	}
	// the resume of an uncommitted clone rounds the same way
	rows := append(src(k128), vol("c", func(r *Row) {
		r.Type, r.Volsize, r.BlockSz, r.Refres, r.Committed = zfs.TypeVolume, 1<<30, k128, 1<<30, false
		r.SourceType, r.SourceID = "volume", "src"
	}))
	plan, err := pl.PlanCreateVolume(NewTable(rows), CreateVolumeRequest{Name: "c", Kind: Block, CapacityBytes: 2_000_000_000, SourceVolumeID: "src"})
	if err != nil || len(plan) != 2 || plan[0].Props["volsize"] != "2000027648" {
		t.Fatalf("resume: %+v %v", plan, err)
	}
}

// S2 in the simulator, which refuses a volsize zfs would refuse: the clone
// of a 128K zvol grown to a decimal "2G" provisions, and a retry is a
// no-op.
func TestCloneOfLargeBlockZvolProvisions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ep, _ := simEndpoint(t, Config{})
	if _, err := ep.CreateVolume(ctx, CreateVolumeRequest{Name: "src", Kind: Block, CapacityBytes: 1 << 30,
		Properties: map[string]string{"volblocksize": "128K"}}); err != nil {
		t.Fatal(err)
	}
	req := CreateVolumeRequest{Name: "c", Kind: Block, CapacityBytes: 2_000_000_000, SourceVolumeID: "src"}
	for i := range 2 {
		v, err := ep.CreateVolume(ctx, req)
		if err != nil || v.CapacityBytes%(128<<10) != 0 || v.CapacityBytes < req.CapacityBytes {
			t.Fatalf("attempt %d: %+v %v", i, v, err)
		}
	}
}

// S2: a retry of a half-applied CreateVolume that fails the same way must
// not turn the first attempt's "a retry converges" (Aborted) into a final
// Internal: the earlier attempt's ops are applied either way.
func TestCreateResumeFailureStaysAborted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ep, sim := simEndpoint(t, Config{})
	if _, err := ep.CreateVolume(ctx, CreateVolumeRequest{Name: "src", Kind: Block, CapacityBytes: 1 << 30}); err != nil {
		t.Fatal(err)
	}
	sim.Fail = func(args []string) error {
		if args[0] == "set" && slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, "volsize=") }) {
			return &zfs.ExitError{Code: 1, Stderr: "internal error: Input/output error\n"}
		}
		return nil
	}
	req := CreateVolumeRequest{Name: "c", Kind: Block, CapacityBytes: 2 << 30, SourceVolumeID: "src"}
	for i := range 3 {
		_, err := ep.CreateVolume(ctx, req)
		if CodeOf(err) != Aborted {
			t.Fatalf("attempt %d: code %v (%v), want Aborted", i, CodeOf(err), err)
		}
	}
	// nothing half-applied: a request that never mutated stays Internal
	sim.Fail = func(args []string) error {
		if args[0] == "create" {
			return &zfs.ExitError{Code: 1, Stderr: "internal error: Input/output error\n"}
		}
		return nil
	}
	if _, err := ep.CreateVolume(ctx, CreateVolumeRequest{Name: "fresh"}); CodeOf(err) != Internal {
		t.Fatalf("fresh create: %v", err)
	}
}

// S3: the resume branch uses OwnedVolume. A managed row without the name
// marker is not ours (ADR 0003): CreateVolume writes nothing on it and
// says AlreadyExists, committed or not.
func TestCreateNeverStampsRowWithoutName(t *testing.T) {
	t.Parallel()
	for _, committed := range []bool{false, true} {
		over := map[string][2]string{rNames.managed(): {"true", "local"}}
		if committed {
			over[rNames.success()] = [2]string{"true", "local"}
		}
		r := &rawRunner{raw: rawOf(dsCells(rParent+"/m", zfs.TypeFilesystem, over))}
		ep := rEndpoint(t, r)
		_, err := ep.CreateVolume(context.Background(), CreateVolumeRequest{Name: "m"})
		if CodeOf(err) != AlreadyExists || len(r.mutations()) != 0 {
			t.Fatalf("committed=%v: code %v (%v), mutations %v", committed, CodeOf(err), err, r.mutations())
		}
	}
	// a name marker for another volume, likewise
	tab := NewTable([]Row{vol("x", func(r *Row) { r.VolumeName, r.Committed = "y", false })})
	if _, err := pl.PlanCreateVolume(tab, CreateVolumeRequest{Name: "x"}); CodeOf(err) != AlreadyExists {
		t.Fatalf("renamed: %v", err)
	}
}

// H-02 (same bug as S3), planner level: an uncommitted managed row without
// the name marker gets no plan, so its refquota is never touched.
func TestPlanCreateDoesNotAdoptUnnamedManaged(t *testing.T) {
	t.Parallel()
	stranger := vol("adopt", func(r *Row) { r.VolumeName, r.Committed, r.Refquota = "", false, 1<<20 })
	plan, err := pl.PlanCreateVolume(NewTable([]Row{stranger}), CreateVolumeRequest{Name: "adopt", CapacityBytes: 64 << 20})
	if CodeOf(err) != AlreadyExists || len(plan) != 0 {
		t.Fatalf("plan %+v %v", plan, err)
	}
}

// S6: OpenZFS 2.4's create and clone exit 0 when the mount fails, and a
// kill after the create ioctl skips the mount. With RequireMounted (root),
// a mountable filesystem is committed only once a scan shows it mounted;
// otherwise CreateVolume is Unavailable and the row stays uncommitted.
func TestPlanCreateCommitsOnlyMounted(t *testing.T) {
	t.Parallel()
	mp := pl
	mp.RequireMounted = true
	unmounted := func(mut func(*Row)) Table {
		return NewTable([]Row{vol("u", func(r *Row) {
			r.Committed, r.Mount, r.CanMount, r.Mounted = false, "/tank/v/u", "on", false
			if mut != nil {
				mut(r)
			}
		})})
	}
	for _, c := range []struct {
		name   string
		p      Planner
		tab    Table
		commit bool // else Unavailable
	}{
		{"unmounted, canmount=on", mp, unmounted(nil), false},
		{"mounted", mp, unmounted(func(r *Row) { r.Mounted = true }), true},
		{"canmount=noauto: the operator's choice", mp, unmounted(func(r *Row) { r.CanMount = "noauto" }), true},
		{"canmount=off", mp, unmounted(func(r *Row) { r.CanMount = "off" }), true},
		{"mountpoint=none", mp, unmounted(func(r *Row) { r.Mount = "none" }), true},
		{"mountpoint=legacy", mp, unmounted(func(r *Row) { r.Mount = "legacy" }), true},
		{"non-root: nothing can mount", pl, unmounted(nil), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			plan, err := c.p.PlanCreateVolume(c.tab, CreateVolumeRequest{Name: "u"})
			if c.commit {
				if err != nil || len(plan) != 1 || !c.p.commits(plan) {
					t.Fatalf("plan %+v %v, want the commit", plan, err)
				}
				return
			}
			if CodeOf(err) != Unavailable || len(plan) != 0 {
				t.Fatalf("plan %+v %v, want Unavailable", plan, err)
			}
		})
	}
	// a fresh filesystem create (or clone) is not committed in the same
	// plan: the commit waits for a scan that shows the mount
	for _, r := range []CreateVolumeRequest{{Name: "n"}, {Name: "n", SourceVolumeID: "a"}} {
		plan, err := mp.PlanCreateVolume(NewTable([]Row{vol("a", nil)}), r)
		if err != nil || len(plan) == 0 || mp.commits(plan) {
			t.Fatalf("%+v: plan %+v %v", r, plan, err)
		}
		if plan, _ := pl.PlanCreateVolume(NewTable([]Row{vol("a", nil)}), r); !pl.commits(plan) {
			t.Fatalf("non-root %+v: plan %+v", r, plan)
		}
	}
	// zvols mount nothing
	plan, err := mp.PlanCreateVolume(NewTable(nil), CreateVolumeRequest{Name: "z", Kind: Block, CapacityBytes: 1 << 30})
	if err != nil || !mp.commits(plan) {
		t.Fatalf("zvol: %+v %v", plan, err)
	}
}

// unmountAfterCreate is the simulator with OpenZFS 2.4's failed mount
// after a successful create or clone: the command exits 0, the dataset
// exists, and it is not mounted.
type unmountAfterCreate struct{ *zfstest.Sim }

func (u unmountAfterCreate) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := u.Sim.Run(ctx, name, args...)
	if err == nil && len(args) > 0 && (args[0] == "create" || args[0] == "clone") {
		ds := args[len(args)-1]
		d, gerr := zfs.New(u.Sim).Get(ctx, ds, "type")
		if gerr == nil && zfs.Type(d.Props["type"].Value) == zfs.TypeFilesystem {
			u.Sim.Unmount(ds)
		}
	}
	return out, err
}

// S6 end to end: the unmounted filesystem is never committed, and a retry
// stays Unavailable without writing anything until it is mounted.
func TestCreateUnmountedFilesystemIsUnavailable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sim := zfstest.New("tank")
	z := zfs.New(unmountAfterCreate{sim})
	if err := zfs.New(sim).CreateFilesystem(ctx, rParent, nil, true); err != nil {
		t.Fatal(err)
	}
	ep, err := New(z, Config{Parent: rParent, FileExport: true, Etab: sim})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		n := len(sim.Calls)
		_, err := ep.CreateVolume(ctx, CreateVolumeRequest{Name: "u", CapacityBytes: 1 << 30})
		if CodeOf(err) != Unavailable {
			t.Fatalf("attempt %d: %v", i, err)
		}
		if got := sim.Prop(rParent+"/u", rNames.success()); got == "true" {
			t.Fatalf("attempt %d committed an unmounted filesystem", i)
		}
		if i > 0 {
			for _, c := range sim.Calls[n:] {
				if c[1] != "get" {
					t.Fatalf("retry mutated: %v", c)
				}
			}
		}
	}
	// mounted (here by a sharenfs change, as libzfs does): the retry commits
	if err := zfs.New(sim).Set(ctx, rParent+"/u", map[string]string{"sharenfs": "on"}); err != nil {
		t.Fatal(err)
	}
	if v, err := ep.CreateVolume(ctx, CreateVolumeRequest{Name: "u", CapacityBytes: 1 << 30}); err != nil || v.Mountpoint == "" {
		t.Fatalf("after mount: %+v %v", v, err)
	}
	// a block volume is unaffected
	if _, err := ep.CreateVolume(ctx, CreateVolumeRequest{Name: "b", Kind: Block, CapacityBytes: 1 << 30}); err != nil {
		t.Fatal(err)
	}
}

// S4: the page token resumes after the id it names, whether or not that
// item still exists; only a token the endpoint could not have issued is
// Aborted.
func TestPageResumesAfterDeletedToken(t *testing.T) {
	t.Parallel()
	ids := []string{"a", "b", "c", "d"}
	self := func(s string) string { return s }
	got, next, err := page(ids, self, volumeToken, 2, "")
	if err != nil || !slices.Equal(got, []string{"a", "b"}) || next == "" {
		t.Fatalf("page 1: %v %q %v", got, next, err)
	}
	for _, rest := range [][]string{{"c", "d"}, {"a", "c", "d"}, {"c"}} {
		got, _, err := page(rest, self, volumeToken, 2, next)
		want := slices.DeleteFunc(slices.Clone(rest), func(s string) bool { return s <= "b" })
		if err != nil || !slices.Equal(got, want) {
			t.Fatalf("after %q over %v: %v %v, want %v", next, rest, got, err, want)
		}
	}
	for _, bad := range []string{"b", "nope", "invalid-token", tokenPrefix, tokenPrefix + "x/y"} {
		if _, _, err := page(ids, self, volumeToken, 2, bad); CodeOf(err) != Aborted {
			t.Errorf("token %q: %v", bad, err)
		}
	}
}

// S4 through the endpoint: deleting the token's volume between pages.
func TestListTokenSurvivesDeletedVolume(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ep, _ := simEndpoint(t, Config{})
	for _, n := range []string{"v1", "v2", "v3"} {
		if _, err := ep.CreateVolume(ctx, CreateVolumeRequest{Name: n}); err != nil {
			t.Fatal(err)
		}
	}
	vols, tok, err := ep.ListVolumes(ctx, 2, "")
	if err != nil || len(vols) != 2 {
		t.Fatal(vols, err)
	}
	if err := ep.DeleteVolume(ctx, vols[1].ID); err != nil {
		t.Fatal(err)
	}
	vols, next, err := ep.ListVolumes(ctx, 2, tok)
	if err != nil || len(vols) != 1 || vols[0].ID != "v3" || next != "" {
		t.Fatalf("after delete: %+v %q %v", vols, next, err)
	}
	if _, err := ep.CreateSnapshot(ctx, "s1", "v1"); err != nil {
		t.Fatal(err)
	}
	if _, err := ep.CreateSnapshot(ctx, "s2", "v3"); err != nil {
		t.Fatal(err)
	}
	snaps, stok, err := ep.ListSnapshots(ctx, "", "", 1, "")
	if err != nil || len(snaps) != 1 {
		t.Fatal(snaps, err)
	}
	if err := ep.DeleteSnapshot(ctx, snaps[0].ID); err != nil {
		t.Fatal(err)
	}
	snaps, _, err = ep.ListSnapshots(ctx, "", "", 1, stok)
	if err != nil || len(snaps) != 1 || snaps[0].ID != "v3@s2" {
		t.Fatalf("snapshots after delete: %+v %v", snaps, err)
	}
}

// S8: a desired id the endpoint could never issue names no volume, as
// everywhere else (issuedVolumeID): it is skipped and reported, and the
// rest of the run goes on. An empty id is a malformed request.
func TestReconcileSkipsNonIssuableDesiredID(t *testing.T) {
	t.Parallel()
	raw := rawOf(
		dsCells(rParent+"/a", zfs.TypeFilesystem, owned("a", true)),
		dsCells(rParent+"/b", zfs.TypeFilesystem, owned("b", true)),
	)
	ep := rEndpoint(t, &rawRunner{raw: raw})
	plan, err := ep.Reconcile(context.Background(), ReconcileRequest{DesiredVolumeIDs: []string{"a", "ns/pv"}, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(plan.Skipped, ReconcileSkip{Target: "ns/pv", Reason: SkipDesiredMissing}) {
		t.Errorf("skipped %+v", plan.Skipped)
	}
	if len(plan.Actions) != 1 || plan.Actions[0].Target != "b" || plan.Actions[0].Reason != ReasonUndesired {
		t.Errorf("actions %+v", plan.Actions)
	}
	if _, err := ep.Reconcile(context.Background(), ReconcileRequest{DesiredVolumeIDs: []string{"a", ""}, DryRun: true}); CodeOf(err) != InvalidArgument {
		t.Errorf("empty id: %v", err)
	}
}
