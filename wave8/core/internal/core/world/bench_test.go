package world_test

import (
	"bytes"
	"context"
	"io"
	"runtime"
	"sync"
	"testing"

	"github.com/wommy/empty/internal/core/coretest"
	"github.com/wommy/empty/internal/core/decide"
	"github.com/wommy/empty/internal/core/name"
	"github.com/wommy/empty/internal/core/req"
	"github.com/wommy/empty/internal/core/world"
)

type fix struct {
	m          *coretest.Model
	full, cold []byte // All(true) (HEAD's column set and more), All(false) (the narrow scan)
}

var fixtures sync.Map

func fixture(b testing.TB, size string) (*fix, *world.Schema) {
	p, _ := name.ParseDataset(parent)
	sc, _ := world.NewSchema(p, "zfs-endpoint")
	if f, ok := fixtures.Load(size); ok {
		return f.(*fix), sc
	}
	vols, snaps := 1000, 9
	if size == "110k" {
		vols, snaps = 10000, 10
	}
	m := coretest.Synthetic(parent, "zfs-endpoint", vols, snaps)
	f := &fix{m: m, full: m.Docs(sc, world.All(true))[0], cold: m.Docs(sc, world.All(false))[0]}
	fixtures.Store(size, f)
	return f, sc
}

var sizes = []string{"10k", "110k"}

// BenchmarkCoreScan: one full-parent parse into a World (the bytes zfs
// printed are in memory, as in BenchmarkWave8HeadScan).
func BenchmarkCoreScan(b *testing.B) {
	for _, size := range sizes {
		f, sc := fixture(b, size)
		for _, c := range []struct {
			name  string
			doc   []byte
			scope world.Scope
		}{{"27cols", f.full, world.All(true)}, {"16cols", f.cold, world.All(false)}} {
			b.Run(size+"/"+c.name, func(b *testing.B) {
				b.SetBytes(int64(len(c.doc)))
				b.ReportAllocs()
				for b.Loop() {
					w, err := sc.Parse(c.scope, bytes.NewReader(c.doc))
					if err != nil {
						b.Fatal(err)
					}
					if n, _ := w.Len(); n == 0 {
						b.Fatal("empty")
					}
				}
			})
		}
	}
}

// BenchmarkCoreDeleteVolume: the RPC up to apply, as the core does it:
// parse the volume's own subtree (clones anywhere are in its snapshots'
// `clones` property), bind, decide. Same refusal as HEAD.
func BenchmarkCoreDeleteVolume(b *testing.B) {
	for _, size := range sizes {
		f, sc := fixture(b, size)
		r, _, _ := req.ParseDeleteVolume("pvc-00000002-1b9f-4c1e-9d2e-000000003dde")
		docs := f.m.Docs(sc, r.Scope())
		b.Run(size, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				var rs []io.Reader
				for _, d := range docs {
					rs = append(rs, bytes.NewReader(d))
				}
				l := world.NewLease()
				w, err := sc.ParseUnder(l, r.Scope(), rs...)
				if err != nil {
					b.Fatal(err)
				}
				bd, _ := world.Bind(w, l, r)
				if _, ref := decide.DeleteVolume(bd, decide.Policy{}); ref == nil {
					b.Fatal("deleted a volume with snapshots")
				}
			}
		})
	}
}

// BenchmarkCoreDeleteVolumeFullScan: the same decision on a whole-parent
// world, to separate the scan narrowing from the parse itself.
func BenchmarkCoreDeleteVolumeFullScan(b *testing.B) {
	for _, size := range sizes {
		f, sc := fixture(b, size)
		r, _, _ := req.ParseDeleteVolume("pvc-00000002-1b9f-4c1e-9d2e-000000003dde")
		b.Run(size, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				l := world.NewLease()
				w, err := sc.ParseUnder(l, world.All(false), bytes.NewReader(f.cold))
				if err != nil {
					b.Fatal(err)
				}
				bd, _ := world.Bind(w, l, r)
				if _, ref := decide.DeleteVolume(bd, decide.Policy{}); ref == nil {
					b.Fatal("deleted")
				}
			}
		})
	}
}

// BenchmarkLayout: split per-kind slices (World) vs one kind-tagged fat
// row per dataset, from the same decoded records.
func BenchmarkLayout(b *testing.B) {
	for _, size := range sizes {
		f, sc := fixture(b, size)
		raw, err := sc.ParseRaw(world.All(true), bytes.NewReader(f.full))
		if err != nil {
			b.Fatal(err)
		}
		w, _ := raw.BuildSplit()
		tg := raw.BuildTagged()
		if w.JudgeAll() != tg.JudgeAll() || w.ListCommitted() != tg.ListCommitted() || w.CountSnaps() != tg.CountSnaps() {
			b.Fatal("layouts disagree")
		}
		b.Run(size+"/build/split", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_, _ = raw.BuildSplit()
			}
		})
		b.Run(size+"/build/tagged", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = raw.BuildTagged()
			}
		})
		for _, q := range []struct {
			name string
			s, t func() int
		}{{"judge-every-volume", w.JudgeAll, tg.JudgeAll}, {"list-volumes", w.ListCommitted, tg.ListCommitted}, {"list-snapshots", w.CountSnaps, tg.CountSnaps}} {
			b.Run(size+"/"+q.name+"/split", func(b *testing.B) {
				for b.Loop() {
					_ = q.s()
				}
			})
			b.Run(size+"/"+q.name+"/tagged", func(b *testing.B) {
				for b.Loop() {
					_ = q.t()
				}
			})
		}
	}
}

func TestCoreRetained(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	vs, ss, ts := world.Sizes()
	t.Logf("sizeof Vol=%d Snap=%d tagRow=%d", vs, ss, ts)
	for _, size := range sizes {
		f, sc := fixture(t, size)
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		w, err := sc.Parse(world.All(true), bytes.NewReader(f.full))
		if err != nil {
			t.Fatal(err)
		}
		runtime.GC()
		runtime.ReadMemStats(&after)
		v, s := w.Len()
		t.Logf("%s: World retains %.1f MiB for %d vols + %d snaps (json %d MiB, 16-col json %d MiB)", size,
			float64(after.HeapAlloc-before.HeapAlloc)/(1<<20), v, s, len(f.full)>>20, len(f.cold)>>20)
		runtime.KeepAlive(w)
	}
	_ = context.Background
}
