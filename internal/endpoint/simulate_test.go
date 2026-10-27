package endpoint

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/wommy/empty/internal/zfs"
)

// simulateDestroy is the sequential reference for simulateDestroyAll: one
// plan's destroys, one op at a time, each a pass over every row.
func simulateDestroy(rows []Row, plan Plan) []Row {
	clones := func(snap string) bool {
		return slices.ContainsFunc(rows, func(r Row) bool { return r.Origin == snap })
	}
	reap := func(origin string) {
		if origin == "" || clones(origin) {
			return
		}
		rows = slices.DeleteFunc(rows, func(r Row) bool { return r.Name == origin && r.Deferred && r.Holds == 0 })
	}
	for _, op := range plan {
		if op.Kind != OpDestroy {
			continue
		}
		var origins []string
		rows = slices.DeleteFunc(rows, func(r Row) bool {
			gone := r.Name == op.Target && !op.Defer ||
				op.Recursive && (strings.HasPrefix(r.Name, op.Target+"@") || strings.HasPrefix(r.Name, op.Target+"/"))
			if gone {
				origins = append(origins, r.Origin)
			}
			return gone
		})
		if op.Defer {
			if i := slices.IndexFunc(rows, func(r Row) bool { return r.Name == op.Target }); i >= 0 {
				rows[i].Deferred = true
				reap(op.Target)
			}
		}
		for _, o := range origins {
			reap(o)
		}
	}
	return rows
}

// BenchmarkPlanReconcile: n committed old volumes, half desired. With a
// pass over every row per planned volume this was quadratic (16k: 31 s).
func BenchmarkPlanReconcile(b *testing.B) {
	for _, n := range []int{1000, 16000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			var rows []Row
			var want []string
			for i := range n {
				id := fmt.Sprintf("pvc-%06d", i)
				rows = append(rows, aged(vol(id, nil)))
				if i%2 == 0 {
					want = append(want, id)
				}
			}
			tab := NewTable(rows)
			b.ResetTimer()
			for range b.N {
				if _, err := pl.PlanReconcile(tab, desired(want...)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// One batched pass equals the sequential per-op simulation.
//
// The PR tier runs seeds 0..999: on the 45 mutgen mutants of
// simulateDestroyAll the latest first failing seed is 5, and the ones that
// survive survive all 20000 seeds this test used to run (wave10/tests.md),
// so those cost 2.5 s (10 s under -race) for nothing. Longer exploration is
// FuzzSimulateDestroyAllEquivalent's job (nightly).
func TestSimulateDestroyAllEquivalent(t *testing.T) {
	t.Parallel()
	for seed := range uint64(1000) {
		simulateDestroyAllEquivalent(t, seed)
	}
}

func FuzzSimulateDestroyAllEquivalent(f *testing.F) {
	for _, seed := range []uint64{0, 1, 2, 5} {
		f.Add(seed)
	}
	f.Fuzz(simulateDestroyAllEquivalent)
}

func simulateDestroyAllEquivalent(t *testing.T, seed uint64) {
	{
		rng := rand.New(rand.NewPCG(seed, 1))
		holds := rand.New(rand.NewPCG(seed, 2)) // its own stream: rng's draws stay as they were
		var rows []Row
		var snaps []string
		for v := range 1 + rng.IntN(6) {
			name := fmt.Sprintf("tank/v/%c", 'a'+v)
			r := Row{Name: name, ID: name[7:]}
			if len(snaps) > 0 && rng.IntN(2) == 0 {
				r.Origin = snaps[rng.IntN(len(snaps))]
			}
			rows = append(rows, r)
			for s := range rng.IntN(3) {
				sn := fmt.Sprintf("%s@%d", name, s)
				rows = append(rows, Row{Name: sn, ID: sn[7:], Type: zfs.TypeSnapshot, Deferred: rng.IntN(4) == 0, Holds: uint64(holds.IntN(5) / 4)})
				snaps = append(snaps, sn)
			}
		}
		var plans []Plan
		for range rng.IntN(4) {
			var p Plan
			for range 1 + rng.IntN(3) {
				switch rng.IntN(3) {
				case 0:
					p = append(p, Op{Kind: OpDestroy, Target: rows[rng.IntN(len(rows))].Name, Recursive: true})
				case 1:
					p = append(p, Op{Kind: OpDestroy, Target: rows[rng.IntN(len(rows))].Name})
				default:
					if len(snaps) > 0 {
						p = append(p, Op{Kind: OpDestroy, Target: snaps[rng.IntN(len(snaps))], Defer: true})
					}
				}
			}
			plans = append(plans, p)
		}
		seq := slices.Clone(rows)
		for _, p := range plans {
			seq = simulateDestroy(seq, p)
		}
		bat := simulateDestroyAll(slices.Clone(rows), plans)
		if !reflect.DeepEqual(seq, bat) && !(len(seq) == 0 && len(bat) == 0) {
			t.Fatalf("seed %d: plans %+v\nrows %+v\nseq %+v\nbat %+v", seed, plans, rows, seq, bat)
		}
	}
}
