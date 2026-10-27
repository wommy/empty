import os
os.chdir('/home/user/empty/.claude/worktrees/agent-ad45c3ab7d0b822f7')
p='internal/endpoint/reconcile.go'
s=open(p).read()
i=s.index('// simulateDestroy applies a delete plan')
j=s.index('// ReconcileRequest is what')
ref=s[i:j]
new='''// simulateDestroyAll applies every destroy of a round's plans to rows the
// way zfs does, in one pass: -r takes a dataset and everything below it; a
// deferred snapshot destroy removes the snapshot once no clone remains, else
// marks it; removing a dataset reaps its origin when that was marked and now
// has no clones.
//
// It is O(rows + ops); applying the plans one at a time was O(rows) per op,
// which made PlanReconcile quadratic (31 s at 16k volumes, half of it under
// the locks). Destroys only remove rows, so reaping once at the end equals
// reaping after each op: the candidates are the same (defer targets and
// origins of removed rows), and a candidate with no clones left at the end
// lost its last clone during the round. TestSimulateDestroyAllEquivalent
// checks it against the sequential version.
func simulateDestroyAll(rows []Row, plans []Plan) []Row {
	exact, rec, deferT := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, plan := range plans {
		for _, op := range plan {
			if op.Kind != OpDestroy {
				continue
			}
			if op.Defer {
				deferT[op.Target] = true
			} else {
				exact[op.Target] = true
			}
			if op.Recursive {
				rec[op.Target] = true
			}
		}
	}
	under := func(name string) bool {
		if len(rec) == 0 {
			return false
		}
		for i := 0; i < len(name); i++ {
			if (name[i] == '@' || name[i] == '/') && rec[name[:i]] {
				return true
			}
		}
		return false
	}
	cand := maps.Clone(deferT)
	rows = slices.DeleteFunc(rows, func(r Row) bool {
		gone := exact[r.Name] || under(r.Name)
		if gone && r.Origin != "" {
			cand[r.Origin] = true
		}
		return gone
	})
	clones := map[string]int{}
	for i, r := range rows {
		if deferT[r.Name] {
			rows[i].Deferred = true
		}
		if r.Origin != "" {
			clones[r.Origin]++
		}
	}
	return slices.DeleteFunc(rows, func(r Row) bool { return cand[r.Name] && r.Deferred && clones[r.Name] == 0 })
}

'''
s=s[:i]+new+s[j:]
old='''		for _, plan := range plans {
			rows = simulateDestroy(rows, plan)
		}
'''
assert old in s
s=s.replace(old,'''		rows = simulateDestroyAll(rows, plans)
''')
open(p,'w').write(s)

t='''package endpoint

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
'''+ref.split('\n',4)[4]+'''
// One batched pass equals the sequential per-op simulation.
func TestSimulateDestroyAllEquivalent(t *testing.T) {
	for seed := range uint64(20000) {
		rng := rand.New(rand.NewPCG(seed, 1))
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
				rows = append(rows, Row{Name: sn, ID: sn[7:], Type: zfs.TypeSnapshot, Deferred: rng.IntN(4) == 0})
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
			t.Fatalf("seed %d: plans %+v\\nrows %+v\\nseq %+v\\nbat %+v", seed, plans, rows, seq, bat)
		}
	}
}

var _ = strings.HasPrefix
'''
open('internal/endpoint/simulate_test.go','w').write(t)

p='internal/endpoint/reconcile_test.go'
s=open(p).read()
s=s.replace('''	rows = simulateDestroy(rows, Plan{{Kind: OpDestroy, Target: "tank/v/b@s", Defer: true}})''','''	rows = simulateDestroyAll(rows, []Plan{{{Kind: OpDestroy, Target: "tank/v/b@s", Defer: true}}})''')
s=s.replace('''	rows = simulateDestroy(rows, Plan{{Kind: OpDestroy, Target: "tank/v/c", Recursive: true}})
	rows = simulateDestroy(rows, Plan{{Kind: OpDestroy, Target: "tank/v/d", Recursive: true}})''','''	rows = simulateDestroyAll(rows, []Plan{{{Kind: OpDestroy, Target: "tank/v/c"}}})
	rows = simulateDestroyAll(rows, []Plan{{{Kind: OpDestroy, Target: "tank/v/d"}}})''')
open(p,'w').write(s)
