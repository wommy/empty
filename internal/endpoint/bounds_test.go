package endpoint

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"unicode/utf8"

	"github.com/wommy/empty/internal/zfs"
)

// Regression tests for the wave 11 hardening findings that bound memory
// and message sizes. They run on the simulator built here (never on the
// harness), so the real-ZFS leg runs them too, without a skip.

// H-09: an error message echoes caller input with %q, and is returned and
// logged. One buggy client sending a 64 KiB name must not get (or make the
// daemon log) a 64 KiB error: messages are clamped, still valid UTF-8.
func TestErrorsDoNotEchoUnboundedInput(t *testing.T) {
	t.Parallel()
	big := strings.Repeat("a", 64<<10)
	wide := strings.Repeat("é", 32<<10) // 2-byte runes: a clamp must not split one
	ep, _ := simEndpoint(t, Config{})
	ctx := context.Background()
	for name, err := range map[string]error{
		"volumeID":     func() error { _, e := volumeID(big); return e }(),
		"issuedID":     func() error { _, e := issuedVolumeID(big + "/"); return e }(),
		"snapshotID":   func() error { _, _, e := parseSnapshotID(big + "@@"); return e }(),
		"validateProp": func() error { _, e := ValidateProperties(Filesystem, map[string]string{big: "x"}); return e }(),
		"propValue":    func() error { _, e := ValidateProperties(Filesystem, map[string]string{"compression": big}); return e }(),
		"wideValue":    func() error { _, e := ValidateProperties(Filesystem, map[string]string{"compression": wide}); return e }(),
		"planSnapshot": func() error {
			_, _, e := pl.PlanCreateSnapshot(NewTable([]Row{vol("v", nil)}), big+"\n", "v")
			return e
		}(),
		"CreateVolume":   func() error { _, e := ep.CreateVolume(ctx, CreateVolumeRequest{Name: big, CapacityBytes: 1}); return e }(),
		"GetVolume":      func() error { _, e := ep.GetVolume(ctx, big); return e }(),
		"ExpandVolume":   func() error { _, e := ep.ExpandVolume(ctx, big, 1, 0); return e }(),
		"CreateSnapshot": func() error { _, e := ep.CreateSnapshot(ctx, big, "x"); return e }(),
		"SnapshotSource": func() error { _, e := ep.CreateSnapshot(ctx, "s", wide); return e }(),
		"ListToken":      func() error { _, _, e := ep.ListVolumes(ctx, 0, big); return e }(),
	} {
		if err == nil {
			t.Errorf("%s: no error", name)
			continue
		}
		msg := err.Error()
		if len(msg) > 1024 {
			t.Errorf("%s: error message is %d bytes for a %d-byte input", name, len(msg), len(big))
		}
		if !utf8.ValidString(msg) {
			t.Errorf("%s: clamped message is not valid UTF-8", name)
		}
	}
	// a short message is left alone
	if e := errorf(InvalidArgument, "invalid volume name %q", "x"); e.Msg != `invalid volume name "x"` {
		t.Errorf("short message changed: %q", e.Msg)
	}
}

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
		_, sim := simEndpoint(t, Config{})
		g := &scanGate{inner: sim, parent: rParent, open: make(chan struct{})}
		ep, err := New(zfs.New(zfs.Limit(g, 8)), Config{Parent: rParent})
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
