package decide_test

// Rows for the wave8 adversary findings (scratchpad/wave8/adversary.md) and
// the spec rows they cite. Golden rows pin argv and tolerance: those are
// the real race defences (B1), which a type change could silently alter.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/wommy/empty/internal/core/code"
	"github.com/wommy/empty/internal/core/coretest"
	"github.com/wommy/empty/internal/core/decide"
	"github.com/wommy/empty/internal/core/op"
	"github.com/wommy/empty/internal/core/req"
	"github.com/wommy/empty/internal/core/world"
	"github.com/wommy/empty/internal/zfs"
)

type golden struct {
	argv string
	tol  op.Tol
}

func render(p op.Plan) []golden {
	var g []golden
	for _, o := range p {
		g = append(g, golden{strings.Join(o.Argv(), " "), o.Tol()})
	}
	return g
}

// TestGoldenPlans: OWN-08/C5 (clone TolNone), OWN-13 (stamped cut),
// CX2/F1 (destroy never -r), SNP-06 (only release-origin defers).
func TestGoldenPlans(t *testing.T) {
	sc := schema(t)
	m := world0(t, sc)
	p, ref, _ := decideCreate(t, m, sc, req.CreateVolumeIn{Name: "c", SourceVolumeID: "sf"})
	if ref != nil {
		t.Fatal(ref)
	}
	want := []golden{
		{"snapshot -o zfs-endpoint:clone_source_for=c -o zfs-endpoint:managed_resource=true tank/k8s/sf@volume-source-for-volume-c", op.TolNone},
		{"clone -o sharenfs=off -o sharesmb=off -o zfs-endpoint:csi_volume_content_source_id=sf -o zfs-endpoint:csi_volume_content_source_type=volume -o zfs-endpoint:csi_volume_name=c -o zfs-endpoint:managed_resource=true tank/k8s/sf@volume-source-for-volume-c tank/k8s/c", op.TolNone},
		{"set refquota=1073741824 tank/k8s/c", op.TolNone},
		{"set zfs-endpoint:provision_success=true tank/k8s/c", op.TolNone},
	}
	if got := render(p); !slices.Equal(got, want) {
		t.Fatalf("create from volume:\n got %v\nwant %v", got, want)
	}
	if r := apply(m, p); r != nil {
		t.Fatal(r)
	}
	p, ref, _ = decideDelete(t, m, sc, "c", decide.Policy{})
	if ref != nil {
		t.Fatal(ref)
	}
	want = []golden{
		{"destroy -d tank/k8s/sf@volume-source-for-volume-c", op.TolMissing},
		{"destroy tank/k8s/c", op.TolMissing},
	}
	if got := render(p); !slices.Equal(got, want) {
		t.Fatalf("delete clone:\n got %v\nwant %v", got, want)
	}
	// a stamped orphan clone source on the deleted volume: plain destroy
	// (a racing clone must fail it, not hide it), then the volume
	_ = m.Admin("snapshot", "-o", mk(":managed_resource=true"), "-o", mk(":clone_source_for=gone"), "tank/k8s/sb@"+world.CloneSourcePrefix+"gone")
	_ = m.Admin("destroy", "tank/k8s/sb@s")
	p, ref, _ = decideDelete(t, m, sc, "sb", decide.Policy{})
	if ref != nil {
		t.Fatal(ref)
	}
	want = []golden{
		{"destroy tank/k8s/sb@volume-source-for-volume-gone", op.TolMissing},
		{"destroy tank/k8s/sb", op.TolMissing},
	}
	if got := render(p); !slices.Equal(got, want) {
		t.Fatalf("delete with orphan clone source:\n got %v\nwant %v", got, want)
	}
}

// OWN-13: the reserved name without our stamp is never ours.
func TestCloneSourceIsOursOnlyByStamp(t *testing.T) {
	t.Run("create-refuses-an-admin-snapshot-of-that-name", func(t *testing.T) {
		sc := schema(t)
		m := world0(t, sc)
		_ = m.Admin("snapshot", "tank/k8s/sf@"+world.CloneSourcePrefix+"b")
		_, ref, _ := decideCreate(t, m, sc, req.CreateVolumeIn{Name: "b", SourceVolumeID: "sf"})
		if ref == nil || ref.Code != code.AlreadyExists {
			t.Fatalf("want AlreadyExists, got %v", ref)
		}
	})
	t.Run("delete-never-releases-an-unstamped-origin", func(t *testing.T) {
		sc := schema(t)
		m := world0(t, sc)
		origin := "tank/k8s/sf@" + world.CloneSourcePrefix + "w"
		_ = m.Admin("snapshot", origin)
		_ = m.Admin("clone", origin, parent+"/w")
		m.SetLocal(parent+"/w", mk(":managed_resource"), "true")
		m.SetLocal(parent+"/w", mk(":csi_volume_name"), "w")
		m.SetLocal(parent+"/w", mk(":provision_success"), "true")
		p, ref, _ := decideDelete(t, m, sc, "w", decide.Policy{})
		if ref != nil {
			t.Fatal(ref)
		}
		if got := render(p); !slices.Equal(got, []golden{{"destroy tank/k8s/w", op.TolMissing}}) {
			t.Fatalf("got %v", got)
		}
		if apply(m, p) != nil || !m.Has(origin) {
			t.Fatal("the administrator's snapshot went away")
		}
	})
}

// A3: our released clone source, still pinned, is never reused, and
// "exists" on the cut is a rescan, never a final AlreadyExists.
func TestDeferredCloneSourceIsNotReused(t *testing.T) {
	sc := schema(t)
	m := world0(t, sc)
	p, _, _ := decideCreate(t, m, sc, req.CreateVolumeIn{Name: "c", SourceVolumeID: "sf"})
	if apply(m, p) != nil {
		t.Fatal("create")
	}
	origin := "tank/k8s/sf@" + world.CloneSourcePrefix + "c"
	_ = m.Admin("create", "tank/backup")
	_ = m.Admin("clone", origin, "tank/backup/pin") // pins it past the delete
	p, ref, _ := decideDelete(t, m, sc, "c", decide.Policy{})
	if ref != nil || apply(m, p) != nil {
		t.Fatal("delete", ref)
	}
	if !m.Has(origin) {
		t.Fatal("pinned origin reaped")
	}
	_, ref, _ = decideCreate(t, m, sc, req.CreateVolumeIn{Name: "c", SourceVolumeID: "sf"})
	if ref == nil || ref.Code != code.Unavailable {
		t.Fatalf("want Unavailable, got %v", ref)
	}
	exists := &zfs.Error{Kind: zfs.KindExists}
	for _, mutated := range []bool{false, true} {
		if o := decide.Map(op.KCutCloneSource, exists, mutated); o.Done || o.Code != code.Aborted {
			t.Fatalf("cut exists (mutated=%v) -> %+v", mutated, o)
		}
	}
}

// OWN-15/B1-C2: an administrator renames the volume away and creates a
// new dataset under its name between scan and destroy. The guid guard
// makes the RPC Aborted; the new dataset survives.
func TestRenameAndRecreateIsNotDestroyed(t *testing.T) {
	sc, m := setupCommitted(t, "r", false)
	p, ref, _ := decideDelete(t, m, sc, "r", decide.Policy{})
	if ref != nil {
		t.Fatal(ref)
	}
	_ = m.Admin("destroy", parent+"/r") // the model has no rename: same effect on the name
	_ = m.Admin("create", "-o", mk(":managed_resource=true"), "-o", mk(":csi_volume_name=r"), parent+"/r")
	r := apply(m, p)
	if r == nil || r.Code != code.Aborted {
		t.Fatalf("want Aborted, got %v", r)
	}
	if !m.Has(parent + "/r") {
		t.Fatal("destroyed the recreated dataset")
	}
}

// B1-C1: sanoid snapshots between scan and destroy: the non-recursive
// destroy fails; nothing the scan did not judge is taken along.
func TestRacingSnapshotStopsTheDestroy(t *testing.T) {
	sc, m := setupCommitted(t, "r", false)
	p, _, _ := decideDelete(t, m, sc, "r", decide.Policy{})
	_ = m.Admin("snapshot", parent+"/r@autosnap")
	if r := apply(m, p); r == nil || r.Code != code.FailedPrecondition {
		t.Fatalf("want FailedPrecondition, got %v", r)
	}
	if !m.Has(parent + "/r@autosnap") {
		t.Fatal("racing snapshot destroyed")
	}
}

// B2: a world binds only under the lease it was scanned with; a lock-free
// read (Parse) binds nothing.
func TestBindNeedsTheScanLease(t *testing.T) {
	sc, m := setupCommitted(t, "r", false)
	r, _, _ := req.ParseDeleteVolume("r")
	docs := func() []io.Reader {
		var rs []io.Reader
		for _, d := range m.Docs(sc, r.Scope()) {
			rs = append(rs, bytes.NewReader(d))
		}
		return rs
	}
	ro, _ := sc.Parse(r.Scope(), docs()...)
	l1, l2 := world.NewLease(), world.NewLease()
	w1, _ := sc.ParseUnder(l1, r.Scope(), docs()...)
	for _, c := range []struct {
		w *world.World
		l world.Lease
	}{{ro, l1}, {w1, l2}} {
		if _, err := world.Bind(c.w, c.l, r); !errors.Is(err, world.ErrStale) {
			t.Fatalf("want ErrStale, got %v", err)
		}
	}
	if _, err := world.Bind(w1, l1, r); err != nil {
		t.Fatal(err)
	}
}

// A guid probe that fails for another reason than "gone" stops the plan.
type guidFails struct{ *coretest.Model }

func (guidFails) Guid(context.Context, string) (uint64, error) {
	return 0, &zfs.Error{Kind: zfs.KindPermission}
}

func TestGuidProbeFailureStops(t *testing.T) {
	sc, m := setupCommitted(t, "r", false)
	p, _, _ := decideDelete(t, m, sc, "r", decide.Policy{})
	if r := decide.Apply(context.Background(), guidFails{m}, p); r == nil || r.Code != code.FailedPrecondition {
		t.Fatalf("got %v", r)
	}
	if !m.Has(parent + "/r") {
		t.Fatal("destroyed without a guid check")
	}
}
