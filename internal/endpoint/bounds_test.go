package endpoint

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

// Regression tests for the wave 11 hardening findings that bound memory
// and message sizes. They run on the simulator built here (never on the
// harness), so the real-ZFS leg runs them too, without a skip.

// scanGate holds every whole-parent scan (`zfs get ... <parent>`) until
// open closes, counting how many are held at once.
type scanGate struct {
	inner     zfs.Runner
	parent    string
	open      chan struct{}
	cur, peak atomic.Int64
}

func (g *scanGate) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "get" && args[len(args)-1] == g.parent {
		n := g.cur.Add(1)
		defer g.cur.Add(-1)
		for p := g.peak.Load(); n > p && !g.peak.CompareAndSwap(p, n); p = g.peak.Load() {
		}
		select {
		case <-g.open:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return g.inner.Run(ctx, name, args...)
}

// H-06: a whole-parent scan holds zfs's output and its parsed copy, both
// O(volumes). Locks are per volume and zfs.Limit bounds processes, not
// buffers, so N calls that each scan the parent held N copies at once. At
// most maxWholeScans run together; the rest wait their turn.
//
// In a synctest bubble, Wait returns once every caller is durably blocked:
// either inside a gated scan or waiting for its turn, with no sleep.
func TestWholeParentScansAreBounded(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		sim := zfstest.New("tank")
		if err := zfs.New(sim).CreateFilesystem(ctx, "tank/v", nil, false); err != nil {
			t.Fatal(err)
		}
		g := &scanGate{inner: sim, parent: "tank/v", open: make(chan struct{})}
		ep, err := New(zfs.New(zfs.Limit(g, 8)), Config{Parent: "tank/v"})
		if err != nil {
			t.Fatal(err)
		}
		const n = 32
		var wg sync.WaitGroup
		var failed atomic.Int64
		for range n {
			wg.Go(func() {
				if _, _, err := ep.ListVolumes(ctx, 0, ""); err != nil {
					failed.Add(1)
				}
			})
		}
		synctest.Wait()
		if c := g.cur.Load(); c != maxWholeScans {
			t.Errorf("%d whole-parent scans in flight with %d callers (zfs limit 8), want %d", c, n, maxWholeScans)
		}
		close(g.open)
		wg.Wait()
		if f := failed.Load(); f != 0 {
			t.Errorf("%d of %d ListVolumes failed waiting for a scan", f, n)
		}
		if p := g.peak.Load(); p > maxWholeScans {
			t.Errorf("peak %d whole-parent scans, want <= %d", p, maxWholeScans)
		}
	})
}
