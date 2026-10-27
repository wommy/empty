package endpoint

// Wave-12 seam regressions (S2, S3, S4, S6, S8). Each was a probe that
// passed while the disagreement was present; these assert the fixed
// behaviour, at the lowest level that proves it (ADR 0005).

import (
	"context"
	"slices"
	"testing"

	"github.com/wommy/empty/internal/zfs"
)

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
