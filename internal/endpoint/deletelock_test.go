package endpoint

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

// gateDestroy parks the first `zfs destroy` after arm until release closes.
type gateDestroy struct {
	zfs.Runner
	mu      sync.Mutex
	armed   bool
	entered chan struct{}
	release chan struct{}
}

func (g *gateDestroy) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	g.mu.Lock()
	hold := g.armed && len(args) > 0 && args[0] == "destroy"
	if hold {
		g.armed = false
	}
	g.mu.Unlock()
	if hold {
		close(g.entered)
		<-g.release
	}
	return g.Runner.Run(ctx, name, args...)
}

// cloneFixture is an endpoint on the simulator with volume "src" and its
// clone "clone" (cut through src@volume-source-for-volume-clone).
func cloneFixture(t *testing.T, r func(zfs.Runner) zfs.Runner) (*Endpoint, *zfstest.Sim) {
	t.Helper()
	ctx := context.Background()
	sim := zfstest.New("tank")
	if err := zfs.New(sim).CreateFilesystem(ctx, "tank/v", nil, true); err != nil {
		t.Fatal(err)
	}
	ep, err := New(zfs.New(r(sim)), Config{Parent: "tank/v"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ep.CreateVolume(ctx, CreateVolumeRequest{Name: "src", CapacityBytes: 10 << 20}); err != nil {
		t.Fatal(err)
	}
	if _, err := ep.CreateVolume(ctx, CreateVolumeRequest{Name: "clone", CapacityBytes: 10 << 20, SourceVolumeID: "src"}); err != nil {
		t.Fatal(err)
	}
	return ep, sim
}

// Deleting a clone releases its origin, a snapshot of the source volume:
// while it does, the source is locked, so an Expand or CreateSnapshot of
// the source waits (here: until its lock wait ends, Aborted).
func TestDeleteCloneLocksItsSource(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		g := &gateDestroy{entered: make(chan struct{}), release: make(chan struct{})}
		ep, _ := cloneFixture(t, func(r zfs.Runner) zfs.Runner { g.Runner = r; return g })
		g.mu.Lock()
		g.armed = true
		g.mu.Unlock()
		done := make(chan error, 1)
		go func() { done <- ep.DeleteVolume(ctx, "clone") }()
		<-g.entered // parked on the release of src@volume-source-for-volume-clone
		if !ep.locks.Held("vol:src") {
			t.Error("vol:src not held while its origin snapshot is released")
		}
		if _, err := ep.ExpandVolume(ctx, "src", 20<<20, 0); CodeOf(err) != Aborted {
			t.Errorf("ExpandVolume(src) during the release: %v, want Aborted", err)
		}
		if _, err := ep.CreateSnapshot(ctx, "during", "src"); CodeOf(err) != Aborted {
			t.Errorf("CreateSnapshot(src) during the release: %v, want Aborted", err)
		}
		close(g.release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

// A source busy when the delete finds it needs it: the delete drops its
// lock and waits for both, in sorted order, then completes once the
// source is free. Nothing of the clone is touched while it waits.
func TestDeleteCloneWaitsForBusySource(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		ep, sim := cloneFixture(t, func(r zfs.Runner) zfs.Runner { return r })
		hold, ok := ep.locks.TryAcquire("vol:src")
		if !ok {
			t.Fatal("vol:src busy")
		}
		done := make(chan error, 1)
		go func() { done <- ep.DeleteVolume(ctx, "clone") }()
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("delete ran while its source was locked: %v", err)
		default:
		}
		if !sim.Has("tank/v/clone") {
			t.Fatal("clone deleted while its source was locked")
		}
		hold()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if sim.Has("tank/v/clone") || sim.Has("tank/v/src@volume-source-for-volume-clone") {
			t.Fatal("clone or origin left after the delete")
		}
	})
}

func TestVolumesOf(t *testing.T) {
	t.Parallel()
	plan := Plan{
		{Kind: OpDestroy, Target: "tank/v/src@volume-source-for-volume-c"},
		{Kind: OpDestroy, Target: "tank/v/c@x"},
		{Kind: OpDestroy, Target: "tank/v/c"},
		{Kind: OpDestroy, Target: "tank/other/z"},
	}
	if got := pl.volumesOf(plan); len(got) != 2 || got[0] != "src" || got[1] != "c" {
		t.Fatalf("volumesOf: %v", got)
	}
}
