package endpoint

// Ported proofs from the adversarial Reconcile review (adv4, written
// against bdf89d0). Each asserts the property it names and failed before
// its fix.

import (
	"context"
	"testing"
	"time"

	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

const (
	a4Parent = "tank/v"
	a4Old    = int64(1_000_000)
	a4Now    = a4Old + 10*3600
)

type a4 struct {
	t   *testing.T
	sim *zfstest.Sim
	z   *zfs.Client
	ep  *Endpoint
	n   props
}

func newA4(t *testing.T) *a4 {
	ctx := context.Background()
	sim := zfstest.New("tank")
	sim.Clock = func() int64 { return a4Old }
	z := zfs.New(sim)
	for _, d := range []string{a4Parent, "tank/other"} {
		if err := z.CreateFilesystem(ctx, d, nil, false); err != nil {
			t.Fatal(err)
		}
	}
	ep, err := New(z, Config{Parent: a4Parent, LockWait: 200 * time.Millisecond,
		Now: func() time.Time { return time.Unix(a4Now, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	return &a4{t: t, sim: sim, z: z, ep: ep, n: ep.names}
}

// vol makes an owned volume (committed when c), as CreateVolume would.
func (h *a4) vol(id string, c bool) {
	h.t.Helper()
	own := map[string]string{h.n.managed(): "true", h.n.volumeName(): id}
	if c {
		own[h.n.success()] = "true"
	}
	if err := h.z.CreateFilesystem(context.Background(), a4Parent+"/"+id, own, false); err != nil {
		h.t.Fatal(err)
	}
}

func (h *a4) has(name string) bool {
	ok, err := h.z.Exists(context.Background(), name)
	if err != nil {
		h.t.Fatal(err)
	}
	return ok
}

func (h *a4) must(err error) {
	h.t.Helper()
	if err != nil {
		h.t.Fatal(err)
	}
}

// F6: clone-source snapshots were recognised by name alone. A snapshot an
// admin or another tool cut with that name on a desired volume was
// collected.
func TestAdv4UserSnapshotWithReservedPrefixCollected(t *testing.T) {
	h := newA4(t)
	ctx := context.Background()
	h.vol("db", true)
	name := a4Parent + "/db@" + cloneSourceSnapshotPrefix + "manual-pre-upgrade"
	h.must(h.z.Snapshot(ctx, name, nil))
	_, err := h.ep.Reconcile(ctx, ReconcileRequest{DesiredVolumeIDs: []string{"db"}})
	h.must(err)
	if !h.has(name) {
		t.Fatal("unowned snapshot on a desired volume destroyed because of its name")
	}
}

// F7: the released origin of a collected clone was destroyed wherever it
// lived (outside the parent too) and whatever clone id it named.
func TestAdv4ReleasedOriginOutsideParent(t *testing.T) {
	h := newA4(t)
	ctx := context.Background()
	h.vol("keep", true)
	h.must(h.z.CreateFilesystem(ctx, "tank/other/golden", nil, false))
	origin := "tank/other/golden@" + cloneSourceSnapshotPrefix + "someone"
	h.must(h.z.Snapshot(ctx, origin, nil))
	h.must(h.z.Clone(ctx, origin, a4Parent+"/adopted", map[string]string{
		h.n.managed(): "true", h.n.volumeName(): "adopted", h.n.success(): "true"}))
	// and inside the parent, a stamp for another clone
	h.vol("src", true)
	other := a4Parent + "/src@" + cloneSourceSnapshotPrefix + "c2"
	h.must(h.z.Snapshot(ctx, other, map[string]string{h.n.managed(): "true", h.n.cloneSourceFor(): "someone-else"}))
	h.must(h.z.Clone(ctx, other, a4Parent+"/c2", map[string]string{
		h.n.managed(): "true", h.n.volumeName(): "c2", h.n.success(): "true"}))
	p, err := h.ep.Reconcile(ctx, ReconcileRequest{DesiredVolumeIDs: []string{"keep", "src"}})
	h.must(err)
	for _, a := range p.Actions {
		for _, v := range a.Volumes {
			if _, err := volumeID(v); err != nil {
				t.Errorf("action %s locks %q", a.Target, v)
			}
		}
	}
	if !h.has(origin) || !h.has(other) {
		t.Fatalf("a snapshot the endpoint does not own was released: %+v", p.Actions)
	}
	if h.has(a4Parent+"/adopted") || h.has(a4Parent+"/c2") {
		t.Fatalf("the clones themselves are ours and undesired: %+v", p)
	}
}

// The legacy rule: an unstamped origin is released with its clone where
// the origin proves it was cut for that clone, from an owned volume.
func TestLegacyUnstampedOriginReleasedWithItsClone(t *testing.T) {
	h := newA4(t)
	ctx := context.Background()
	h.vol("keep", true)
	h.vol("src", true)
	origin := a4Parent + "/src@" + cloneSourceSnapshotPrefix + "dst"
	h.must(h.z.Snapshot(ctx, origin, nil)) // democratic-csi cuts it bare
	h.must(h.z.Clone(ctx, origin, a4Parent+"/dst", map[string]string{
		h.n.managed(): "true", h.n.volumeName(): "dst", h.n.success(): "true"}))
	h.must(h.ep.DeleteVolume(ctx, "dst"))
	if h.has(origin) {
		t.Fatal("legacy origin not released with its clone")
	}
}

// F6, the create side: a clone never adopts (and pins) a snapshot that
// already holds the reserved name without the endpoint's stamp.
func TestCloneRefusesUnstampedReservedSnapshot(t *testing.T) {
	h := newA4(t)
	ctx := context.Background()
	h.vol("src", true)
	name := a4Parent + "/src@" + cloneSourceSnapshotPrefix + "dst"
	h.must(h.z.Snapshot(ctx, name, nil))
	_, err := h.ep.CreateVolume(ctx, CreateVolumeRequest{Name: "dst", SourceVolumeID: "src"})
	if CodeOf(err) != AlreadyExists || h.has(a4Parent+"/dst") {
		t.Fatalf("clone from an unstamped reserved snapshot: %v", err)
	}
}
