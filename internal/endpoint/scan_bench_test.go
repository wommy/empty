package endpoint

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/metrics"
	"testing"
	"time"

	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

// recorder keeps the stdout of the last command it ran.
type recorder struct {
	r    zfs.Runner
	last []byte
}

func (c *recorder) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := c.r.Run(ctx, name, args...)
	c.last = out
	return out, err
}

// replay answers every command with the same stdout.
type replay []byte

func (r replay) Run(context.Context, string, ...string) ([]byte, error) { return r, nil }

// BenchmarkScan is the Go-side cost of one whole-parent scan of n rows
// (half owned, committed filesystems, half a CSI snapshot of each): parse
// the `zfs get -j -p` output the simulator printed for it, decode the rows
// and build the Table. The zfs exec itself is not measured.
func BenchmarkScan(b *testing.B) {
	for _, n := range []int{1000, 10000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			ctx := context.Background()
			ep, rec, out := scanFixture(b, n)
			rec.r = replay(out)
			b.SetBytes(int64(len(out)))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := ep.scan(ctx, ""); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkScanPipe is BenchmarkScan through LocalRunner: a stand-in zfs
// prints the same document to a real pipe, so the cost of reading stdout
// (buffered whole, or streamed into the decoder) is in the numbers.
func BenchmarkScanPipe(b *testing.B) {
	for _, n := range []int{1000, 10000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			ctx := context.Background()
			ep, _, out := scanFixture(b, n)
			dir := b.TempDir()
			doc := filepath.Join(dir, "doc.json")
			if err := os.WriteFile(doc, out, 0o644); err != nil {
				b.Fatal(err)
			}
			bin := filepath.Join(dir, "zfs")
			if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec cat "+doc+"\n"), 0o755); err != nil {
				b.Fatal(err)
			}
			ep.zfs = &zfs.Client{Runner: zfs.LocalRunner{}, Binary: bin}
			b.SetBytes(int64(len(out)))
			b.ReportAllocs()
			runtime.GC()
			base := heapObjects()
			peak := samplePeak()
			b.ResetTimer()
			for range b.N {
				if _, err := ep.scan(ctx, ""); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(peak()-base)/(1<<20), "peak-heap-MB")
		})
	}
}

// scanFixture builds an endpoint over a simulated parent of n rows and
// returns it with the scan's recorded stdout.
func scanFixture(b *testing.B, n int) (*Endpoint, *recorder, []byte) {
	b.Helper()
	ctx := context.Background()
	sim := zfstest.New("tank")
	rec := &recorder{r: sim}
	z := zfs.New(rec)
	if err := z.CreateFilesystem(ctx, "tank/v", nil, false); err != nil {
		b.Fatal(err)
	}
	names := props{DefaultPropertyPrefix}
	for i := range n / 2 {
		id := fmt.Sprintf("pvc-%06d", i)
		own := map[string]string{names.managed(): "true", names.volumeName(): id, names.success(): "true"}
		if err := z.CreateFilesystem(ctx, "tank/v/"+id, own, false); err != nil {
			b.Fatal(err)
		}
		if err := z.Snapshot(ctx, "tank/v/"+id+"@s", map[string]string{
			names.managed(): "true", names.snapshotName(): "s-" + id, names.snapshotSource(): id,
		}); err != nil {
			b.Fatal(err)
		}
	}
	ep, err := New(z, Config{Parent: "tank/v", Now: time.Now})
	if err != nil {
		b.Fatal(err)
	}
	t, err := ep.scan(ctx, "")
	if err != nil {
		b.Fatal(err)
	}
	if got := t.Len(); got != n {
		b.Fatalf("scan read %d rows, want %d", got, n)
	}
	return ep, rec, rec.last
}

func heapObjects() uint64 {
	return readHeapObjects([]metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}})
}

func readHeapObjects(s []metrics.Sample) uint64 {
	metrics.Read(s)
	return s[0].Value.Uint64()
}

// samplePeak samples the heap's object bytes (live and not yet swept)
// every 100µs until the returned function is called, which returns the
// highest sample: a rough peak, comparable between runs.
func samplePeak() func() uint64 {
	stop, done := make(chan struct{}), make(chan uint64)
	go func() {
		var peak uint64
		sample := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
		t := time.NewTicker(100 * time.Microsecond)
		defer t.Stop()
		for {
			peak = max(peak, readHeapObjects(sample))
			select {
			case <-stop:
				done <- peak
				return
			case <-t.C:
			}
		}
	}()
	return func() uint64 { close(stop); return <-done }
}
