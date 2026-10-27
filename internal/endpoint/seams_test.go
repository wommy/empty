package endpoint

// Wave-12 seam regressions (S2, S3, S4, S6, S8). Each was a probe that
// passed while the disagreement was present; these assert the fixed
// behaviour, at the lowest level that proves it (ADR 0005).

import (
	"context"
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
