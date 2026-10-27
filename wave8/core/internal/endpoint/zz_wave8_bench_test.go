package endpoint

// wave8: the current pipeline on synthetic input, for comparison with
// internal/core (same generator, coretest.Synthetic).

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/wommy/empty/internal/core/coretest"
	"github.com/wommy/empty/internal/zfs"
)

type cannedRunner struct{ out []byte }

func (c cannedRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "get" {
		return c.out, nil
	}
	return nil, nil
}

var wave8Fix sync.Map

func wave8Text(b testing.TB, size string) []byte {
	if v, ok := wave8Fix.Load(size); ok {
		return v.([]byte)
	}
	vols, snaps := 1000, 9
	if size == "110k" {
		vols, snaps = 10000, 10
	}
	m := coretest.Synthetic("tank/k8s", DefaultPropertyPrefix, vols, snaps)
	cols := props{DefaultPropertyPrefix}.columns()
	out := m.Text(cols, "tank/k8s", 2)
	wave8Fix.Store(size, out)
	return out
}

func wave8Endpoint(b testing.TB, out []byte) *Endpoint {
	e, err := New(zfs.New(cannedRunner{out}), Config{Parent: "tank/k8s"})
	if err != nil {
		b.Fatal(err)
	}
	return e
}

// BenchmarkWave8HeadScan: one full-parent scan (what DeleteVolume,
// CreateSnapshot, List* and Reconcile do today), bytes already produced.
func BenchmarkWave8HeadScan(b *testing.B) {
	for _, size := range []string{"10k", "110k"} {
		b.Run(size, func(b *testing.B) {
			out := wave8Text(b, size)
			e := wave8Endpoint(b, out)
			b.SetBytes(int64(len(out)))
			b.ReportAllocs()
			for b.Loop() {
				t, err := e.scan(context.Background(), "")
				if err != nil || t.Len() == 0 {
					b.Fatal(err, t.Len())
				}
			}
		})
	}
}

// BenchmarkWave8HeadDeleteVolume: the whole RPC up to apply (lock, full
// scan, plan): every synthetic volume has CSI snapshots, so it refuses.
func BenchmarkWave8HeadDeleteVolume(b *testing.B) {
	for _, size := range []string{"10k", "110k"} {
		b.Run(size, func(b *testing.B) {
			e := wave8Endpoint(b, wave8Text(b, size))
			id := "pvc-00000002-1b9f-4c1e-9d2e-" + strings.Repeat("0", 8) + "3dde"
			b.ReportAllocs()
			for b.Loop() {
				if err := e.DeleteVolume(context.Background(), id); CodeOf(err) != FailedPrecondition {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestWave8HeadRetained(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	for _, size := range []string{"10k", "110k"} {
		e := wave8Endpoint(t, wave8Text(t, size))
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		tab, err := e.scan(context.Background(), "")
		if err != nil {
			t.Fatal(err)
		}
		runtime.GC()
		runtime.ReadMemStats(&after)
		t.Logf("%s: HEAD Table retains %.1f MiB for %d rows (text %d MiB)", size, float64(after.HeapAlloc-before.HeapAlloc)/(1<<20), tab.Len(), len(wave8Text(t, size))>>20)
		runtime.KeepAlive(tab)
	}
}
