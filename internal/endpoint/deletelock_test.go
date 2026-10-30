package endpoint

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

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

// DeleteSnapshot locks the snapshot and its volume, and CreateVolume from
// a source volume locks the source (it snapshots it to cut the clone):
// with either key held by another call they wait, then give up Aborted,
// leaving the snapshot and making no clone. LockWait runs on the
// synctest bubble's fake clock.
func TestSnapshotDeleteAndCloneTakeTheSourceLocks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	hold := func(t *testing.T, ep *Endpoint, key string) func() {
		t.Helper()
		release, ok := ep.locks.TryAcquire(key)
		if !ok {
			t.Fatalf("%s busy", key)
		}
		return release
	}
	for _, key := range []string{"snap:src@s", "vol:src"} {
		t.Run("DeleteSnapshot/"+key, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ep, sim := cloneFixture(t, func(r zfs.Runner) zfs.Runner { return r })
				ep.cfg.LockWait = time.Minute
				if _, err := ep.CreateSnapshot(ctx, "s", "src"); err != nil {
					t.Fatal(err)
				}
				defer hold(t, ep, key)()
				if err := ep.DeleteSnapshot(ctx, "src@s"); CodeOf(err) != Aborted {
					t.Fatalf("DeleteSnapshot with %s held: %v, want Aborted", key, err)
				}
				if !sim.Has("tank/v/src@s") {
					t.Fatal("snapshot destroyed under another call's lock")
				}
			})
		})
	}
	t.Run("CreateVolume from a source volume", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ep, sim := cloneFixture(t, func(r zfs.Runner) zfs.Runner { return r })
			ep.cfg.LockWait = time.Minute
			defer hold(t, ep, "vol:src")()
			_, err := ep.CreateVolume(ctx, CreateVolumeRequest{Name: "c2", CapacityBytes: 10 << 20, SourceVolumeID: "src"})
			if CodeOf(err) != Aborted {
				t.Fatalf("CreateVolume(clone of src) with vol:src held: %v, want Aborted", err)
			}
			if sim.Has("tank/v/c2") || sim.Has("tank/v/src@volume-source-for-volume-c2") {
				t.Fatal("clone cut under another call's lock on its source")
			}
		})
	})
}

// gateShareSet parks the first `zfs set sharenfs=...` after arm until
// release closes.
type gateShareSet struct {
	zfs.Runner
	mu      sync.Mutex
	armed   bool
	entered chan struct{}
	release chan struct{}
}

func (g *gateShareSet) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	g.mu.Lock()
	hold := g.armed && len(args) > 0 && args[0] == "set" &&
		slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, "sharenfs=") })
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

// PublishFileExport is a read-modify-write of the volume's sharenfs: it
// scans the client list, adds one and sets the whole list back. It holds
// vol:<id> (as Unpublish, Delete and Expand do), so two Publishes of one
// volume for different clients serialize and both lines land; without the
// lock the one that scanned first writes its list over the other's, which
// had already reported success. LockWait runs on the synctest bubble's
// fake clock.
func TestPublishFileExportLocksTheVolume(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	setup := func(t *testing.T) (*Endpoint, *zfstest.Sim, *gateShareSet) {
		sim := zfstest.New("tank")
		if err := zfs.New(sim).CreateFilesystem(ctx, "tank/v", nil, true); err != nil {
			t.Fatal(err)
		}
		g := &gateShareSet{Runner: sim, entered: make(chan struct{}), release: make(chan struct{})}
		ep, err := New(zfs.New(g), Config{Parent: "tank/v", FileExport: true, Etab: sim, LockWait: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ep.CreateVolume(ctx, CreateVolumeRequest{Name: "pvc", CapacityBytes: 10 << 20}); err != nil {
			t.Fatal(err)
		}
		return ep, sim, g
	}
	t.Run("held", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ep, sim, _ := setup(t)
			release, ok := ep.locks.TryAcquire("vol:pvc")
			if !ok {
				t.Fatal("vol:pvc busy")
			}
			defer release()
			if _, err := ep.PublishFileExport(ctx, "pvc", "10.0.0.1", false); CodeOf(err) != Aborted {
				t.Fatalf("Publish with vol:pvc held: %v, want Aborted", err)
			}
			if v := sim.Prop("tank/v/pvc", "sharenfs"); v != "off" {
				t.Fatalf("sharenfs %q written under another call's lock", v)
			}
		})
	})
	t.Run("concurrent publishes both land", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ep, sim, g := setup(t)
			g.mu.Lock()
			g.armed = true
			g.mu.Unlock()
			errs := make(chan error, 2)
			go func() { _, err := ep.PublishFileExport(ctx, "pvc", "10.0.0.1", false); errs <- err }()
			<-g.entered // the first has scanned and is writing its list
			go func() { _, err := ep.PublishFileExport(ctx, "pvc", "10.0.0.2", false); errs <- err }()
			synctest.Wait() // the second waits on the lock (unlocked, it has finished)
			close(g.release)
			for range 2 {
				if err := <-errs; err != nil {
					t.Fatal(err)
				}
			}
			if v := sim.Prop("tank/v/pvc", "sharenfs"); v != "rw=10.0.0.1/32:10.0.0.2/32" {
				t.Fatalf("sharenfs %q after two publishes that both succeeded", v)
			}
		})
	})
}
