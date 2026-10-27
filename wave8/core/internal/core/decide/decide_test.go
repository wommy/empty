package decide_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/wommy/empty/internal/core/code"
	"github.com/wommy/empty/internal/core/coretest"
	"github.com/wommy/empty/internal/core/decide"
	"github.com/wommy/empty/internal/core/name"
	"github.com/wommy/empty/internal/core/op"
	"github.com/wommy/empty/internal/core/req"
	"github.com/wommy/empty/internal/core/world"
)

const (
	parent = "tank/k8s"
	prefix = "zfs-endpoint"
	GiB    = 1 << 30
)

func schema(t testing.TB) *world.Schema {
	p, _ := name.ParseDataset(parent)
	sc, err := world.NewSchema(p, prefix)
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

func mk(p string) string { return prefix + p }

// scan is the shell's read: one zfs get per scope document, then Parse.
func scan(t testing.TB, m *coretest.Model, sc *world.Schema, s world.Scope) *world.World {
	t.Helper()
	var rs []io.Reader
	for _, d := range m.Docs(sc, s) {
		rs = append(rs, bytes.NewReader(d))
	}
	w, err := sc.Parse(s, rs...) // read-only: binds nothing
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return w
}

// scanUnder is the scan an RPC takes after its locks are held.
func scanUnder(t testing.TB, m *coretest.Model, sc *world.Schema, l world.Lease, s world.Scope) *world.World {
	t.Helper()
	var rs []io.Reader
	for _, d := range m.Docs(sc, s) {
		rs = append(rs, bytes.NewReader(d))
	}
	w, err := sc.ParseUnder(l, s, rs...)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return w
}

func decideCreate(t testing.TB, m *coretest.Model, sc *world.Schema, in req.CreateVolumeIn) (op.Plan, *code.Refusal, world.State) {
	t.Helper()
	r, ref := req.ParseCreateVolume(in, req.BlockSize{})
	if ref != nil {
		return nil, ref, 0
	}
	l := world.NewLease()
	w := scanUnder(t, m, sc, l, r.Scope())
	b, err := world.Bind(w, l, r)
	if err != nil {
		t.Fatal(err)
	}
	p, ref := decide.CreateVolume(b)
	return p, ref, w.State(r.ID)
}

func decideDelete(t testing.TB, m *coretest.Model, sc *world.Schema, id string, pol decide.Policy) (op.Plan, *code.Refusal, world.State) {
	t.Helper()
	r, parsed, ref := req.ParseDeleteVolume(id)
	if parsed != req.Proceed {
		return nil, ref, world.Absent
	}
	l := world.NewLease()
	w := scanUnder(t, m, sc, l, r.Scope())
	if r2, more := decide.DeleteScope(w, r); more {
		r = r2
		w = scanUnder(t, m, sc, l, r.Scope())
	}
	b, _ := world.Bind(w, l, r)
	p, ref := decide.DeleteVolume(b, pol)
	return p, ref, w.State(r.ID)
}

func apply(m *coretest.Model, p op.Plan) *code.Refusal {
	return decide.Apply(context.Background(), m, p)
}

func state(t testing.TB, m *coretest.Model, sc *world.Schema, id string) world.State {
	v, _ := name.ParseVolID(id)
	return scan(t, m, sc, world.Vols(false, v)).State(v)
}

// world0: two committed sources (fs "sf", zvol "sb", 1 GiB, thin) with a
// CSI snapshot each, made by the core itself.
func world0(t testing.TB, sc *world.Schema) *coretest.Model {
	m := coretest.New("tank", parent)
	for _, in := range []req.CreateVolumeIn{{Name: "sf", Capacity: GiB}, {Name: "sb", Block: true, Capacity: GiB, Sparse: true}} {
		p, ref, _ := decideCreate(t, m, sc, in)
		if ref != nil || apply(m, p) != nil {
			t.Fatalf("source %s: %v", in.Name, ref)
		}
		if err := m.Admin("snapshot", "-o", mk(":managed_resource=true"), "-o", mk(":csi_snapshot_name=s1"),
			"-o", mk(":csi_snapshot_source_volume_id="+in.Name), parent+"/"+in.Name+"@s"); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

type variant struct {
	name string
	in   req.CreateVolumeIn
}

// variants: every request shape the parser accepts, for target "v".
func variants() []variant {
	var vs []variant
	for _, block := range []bool{false, true} {
		for _, thick := range []bool{false, true} {
			for _, capa := range []uint64{0, 2 * GiB} {
				for _, src := range []string{"", "snap", "vol"} {
					in := req.CreateVolumeIn{Name: "v", Block: block, Capacity: capa}
					s := "sf"
					if block {
						s = "sb"
						in.Sparse = !thick
					} else {
						in.Reserve = thick
					}
					switch src {
					case "snap":
						in.SourceSnapshotID = s + "@s"
					case "vol":
						in.SourceVolumeID = s
					}
					if _, ref := req.ParseCreateVolume(in, req.BlockSize{}); ref != nil {
						continue // unrepresentable request: refused by the parser
					}
					vs = append(vs, variant{fmt.Sprintf("block=%v/thick=%v/cap=%d/src=%s", block, thick, capa/GiB, src), in})
				}
			}
		}
	}
	return vs
}

// fixture puts target "v" into state st, using variant f to make it.
func fixture(t testing.TB, sc *world.Schema, st world.State, f variant) *coretest.Model {
	m := world0(t, sc)
	v := parent + "/v"
	switch st {
	case world.Foreign:
		_ = m.Admin("create", v)
	case world.Invalid:
		_ = m.Admin("create", v)
		m.SetLocal(v, mk(":managed_resource"), "true") // I2: managed without a name marker
	case world.Uncommitted, world.Committed:
		p, ref, _ := decideCreate(t, m, sc, f.in)
		if ref != nil {
			t.Fatalf("fixture %v %s: %v", st, f.name, ref)
		}
		if st == world.Uncommitted {
			p = p[:len(p)-1] // crash before the commit
		}
		if ref := apply(m, p); ref != nil {
			t.Fatal(ref)
		}
	}
	if got := state(t, m, sc, "v"); got != st {
		t.Fatalf("fixture: want %v, got %v", st, got)
	}
	return m
}

func TestTableIsTotal(t *testing.T) {
	for s := range world.NStates {
		for rk := range decide.NReq {
			if !decide.Table[s][rk].Defined(world.State(s), rk) {
				t.Errorf("cell %v×%v undefined", world.State(s), rk)
			}
		}
	}
}

// TestCreateCells is generated from the table: every (state, request) pair,
// with the target made by every request variant.
func TestCreateCells(t *testing.T) {
	sc := schema(t)
	vs := variants()
	t.Logf("%d request variants", len(vs))
	n := 0
	for st := range world.NStates {
		for _, f := range vs {
			base := fixture(t, sc, world.State(st), f)
			for _, r := range vs {
				n++
				m := base.Clone()
				in, _ := req.ParseCreateVolume(r.in, req.BlockSize{})
				cell := decide.Table[st][decide.KindOf(in)]
				p, ref, from := decideCreate(t, m, sc, r.in)
				if from != world.State(st) {
					t.Fatalf("state %v, want %v", from, world.State(st))
				}
				if cell.Refuse != code.OK {
					if ref == nil || ref.Code != cell.Refuse {
						t.Errorf("%v×%s: want %v, got %v %v", world.State(st), r.name, cell.Refuse, p, ref)
					}
					continue
				}
				if ref != nil {
					if !slices.Contains(cell.Errs, ref.Code) {
						t.Errorf("%v×%s (made by %s): %v outside cell", world.State(st), r.name, f.name, ref)
					}
					if world.State(st) == world.Committed && f.name == r.name {
						t.Errorf("identical retry on a committed volume refused: %s: %v", r.name, ref)
					}
					continue
				}
				if !cell.Conforms(p.Kinds()) {
					t.Errorf("plan %v outside cell", p)
				}
				if ref := apply(m, p); ref != nil {
					t.Errorf("%v×%s apply: %v", world.State(st), r.name, ref)
					continue
				}
				if got := state(t, m, sc, "v"); got != cell.To {
					t.Errorf("%v×%s: ends %v, want %v", world.State(st), r.name, got, cell.To)
				}
				// idempotency: the identical request now plans nothing
				if p2, ref2, _ := decideCreate(t, m, sc, r.in); len(p2) != 0 || ref2 != nil {
					t.Errorf("%v×%s: repeat plans %v %v", world.State(st), r.name, p2, ref2)
				}
			}
		}
	}
	t.Logf("%d decisions", n)
}

// TestCrashConvergence is the table's crash property: for every plan, a
// process death after any op, or a kill in the middle of a multi-property
// set, leaves a state the cell lists in Mid, and the retry reaches the
// uninterrupted end state exactly.
func TestCrashConvergence(t *testing.T) {
	sc := schema(t)
	runs := 0
	for _, f := range variants() {
		for _, st := range []world.State{world.Absent, world.Uncommitted} {
			m0 := fixture(t, sc, st, f)
			p, ref, _ := decideCreate(t, m0, sc, f.in)
			if ref != nil {
				t.Fatalf("%s: %v", f.name, ref)
			}
			want := m0.Clone()
			if apply(want, p) != nil {
				t.Fatal("uncrashed apply failed")
			}
			cell := decide.Table[st][decide.KindOf(mustReq(f.in))]
			for k := 0; k <= len(p); k++ {
				for _, cut := range cuts(p, k) {
					runs++
					m := m0.Clone()
					if apply(m, p[:k]) != nil {
						t.Fatal("prefix failed")
					}
					if cut != nil {
						_ = m.Exec(cut)
					}
					if mid := state(t, m, sc, "v"); k < len(p) && !slices.Contains(cell.Mid, mid) {
						t.Errorf("%s from %v: crash after %d leaves %v, not in Mid %v", f.name, st, k, mid, cell.Mid)
					}
					p2, ref, _ := decideCreate(t, m, sc, f.in)
					if ref != nil || apply(m, p2) != nil {
						t.Errorf("%s from %v crash %d: retry %v", f.name, st, k, ref)
						continue
					}
					if m.Dump() != want.Dump() {
						t.Errorf("%s from %v crash %d cut %v: diverged\n got %s\nwant %s", f.name, st, k, cut, m.Dump(), want.Dump())
					}
				}
			}
		}
	}
	t.Logf("%d crash/retry runs", runs)
}

// cuts: nil (a clean crash), plus, when op k is a set of several
// properties, each single property of it (a kill mid `zfs set a=,b=`).
func cuts(p op.Plan, k int) [][]string {
	out := [][]string{nil}
	if k < len(p) {
		if s, ok := p[k].(op.Set); ok {
			a := s.Argv()
			if props := a[1 : len(a)-1]; len(props) > 1 {
				for _, kv := range props {
					out = append(out, []string{"set", kv, a[len(a)-1]})
				}
			}
		}
	}
	return out
}

func mustReq(in req.CreateVolumeIn) req.CreateVolume {
	r, _ := req.ParseCreateVolume(in, req.BlockSize{})
	return r
}

// TestDeleteCells: Delete for every state and every way of making the
// volume, plus the cross-row situations around it, with crash convergence.
func TestDeleteCells(t *testing.T) {
	sc := schema(t)
	type extra struct {
		name   string
		setup  func(m *coretest.Model) error
		code   code.Code // refusal expected (OK: deletes)
		policy decide.Policy
	}
	v := parent + "/v"
	extras := []extra{
		{"plain", func(*coretest.Model) error { return nil }, code.OK, decide.Policy{}},
		{"csi-snapshot", func(m *coretest.Model) error {
			return m.Admin("snapshot", "-o", mk(":managed_resource=true"), "-o", mk(":csi_snapshot_name=x"), "-o", mk(":csi_snapshot_source_volume_id=v"), v+"@x")
		}, code.FailedPrecondition, decide.Policy{}},
		{"sanoid-snapshot", func(m *coretest.Model) error { return m.Admin("snapshot", v+"@autosnap") }, code.FailedPrecondition, decide.Policy{}},
		{"sanoid-snapshot-opted-in", func(m *coretest.Model) error { return m.Admin("snapshot", v+"@autosnap") }, code.OK, decide.Policy{DestroyForeignSnapshots: true}},
		{"child-dataset", func(m *coretest.Model) error { return m.Admin("create", v+"/user-data") }, code.FailedPrecondition, decide.Policy{DestroyForeignSnapshots: true}},
		{"clone-outside-parent", func(m *coretest.Model) error {
			_ = m.Admin("snapshot", v+"@manual")
			_ = m.Admin("create", "tank/backup")
			return m.Admin("clone", v+"@manual", "tank/backup/x")
		}, code.FailedPrecondition, decide.Policy{DestroyForeignSnapshots: true}},
		{"orphan-clone-source", func(m *coretest.Model) error {
			return m.Admin("snapshot", "-o", mk(":managed_resource=true"), "-o", mk(":clone_source_for=gone"), v+"@"+world.CloneSourcePrefix+"gone")
		}, code.OK, decide.Policy{}},
		// OWN-13: the reserved name without our stamp is someone's snapshot
		{"unstamped-clone-source-name", func(m *coretest.Model) error { return m.Admin("snapshot", v+"@"+world.CloneSourcePrefix+"gone") }, code.FailedPrecondition, decide.Policy{}},
		{"clone-source-stamped-for-another", func(m *coretest.Model) error {
			return m.Admin("snapshot", "-o", mk(":managed_resource=true"), "-o", mk(":clone_source_for=other"), v+"@"+world.CloneSourcePrefix+"gone")
		}, code.FailedPrecondition, decide.Policy{DestroyForeignSnapshots: true}},
		{"inconsistent-snapshot", func(m *coretest.Model) error {
			return m.Admin("snapshot", "-o", mk(":managed_resource=true"), v+"@half")
		}, code.FailedPrecondition, decide.Policy{DestroyForeignSnapshots: true}},
	}
	runs := 0
	for st := range world.NStates {
		for _, f := range variants() {
			for _, x := range extras {
				m0 := fixture(t, sc, world.State(st), f)
				if m0.Has(v) && x.setup(m0) != nil {
					continue // e.g. a child under a zvol: zfs refuses, nothing to test
				}
				cell := decide.Table[st][decide.Delete]
				p, ref, _ := decideDelete(t, m0, sc, "v", x.policy)
				switch {
				case cell.Refuse != code.OK:
					if ref == nil || ref.Code != cell.Refuse {
						t.Errorf("%v/%s: want %v, got %v %v", world.State(st), x.name, cell.Refuse, p, ref)
					}
					continue
				case world.State(st) == world.Absent:
					if ref != nil || len(p) != 0 {
						t.Errorf("absent: %v %v", p, ref)
					}
					continue
				case x.code != code.OK:
					if ref == nil || ref.Code != x.code {
						t.Errorf("%v/%s/%s: want %v, got %v %v", world.State(st), f.name, x.name, x.code, p, ref)
					}
					continue
				case ref != nil:
					t.Errorf("%v/%s/%s: %v", world.State(st), f.name, x.name, ref)
					continue
				}
				want := m0.Clone()
				if r := apply(want, p); r != nil {
					t.Fatalf("%s/%s: %v (%v)", f.name, x.name, r, p)
				}
				for k := 0; k <= len(p); k++ {
					runs++
					m := m0.Clone()
					_ = apply(m, p[:k])
					if mid := state(t, m, sc, "v"); k < len(p) && !slices.Contains(cell.Mid, mid) {
						t.Errorf("crash after %d leaves %v", k, mid)
					}
					p2, ref, _ := decideDelete(t, m, sc, "v", x.policy)
					if ref != nil || apply(m, p2) != nil {
						t.Errorf("%s/%s crash %d: retry %v", f.name, x.name, k, ref)
						continue
					}
					if m.Dump() != want.Dump() {
						t.Errorf("%s/%s crash %d: diverged (lifecycle CX1)\n got %s\nwant %s", f.name, x.name, k, m.Dump(), want.Dump())
					}
					if state(t, m, sc, "v") != world.Absent {
						t.Errorf("not deleted")
					}
					// nothing of the sources was touched
					if !m.Has(parent+"/sf@s") || !m.Has(parent+"/sb@s") {
						t.Errorf("a source snapshot went away")
					}
					if strings.Contains(m.Dump(), world.CloneSourcePrefix+"v ") {
						t.Errorf("%s/%s crash %d: clone source leaked", f.name, x.name, k)
					}
				}
			}
		}
	}
	t.Logf("%d delete crash/retry runs", runs)
}

// TestErrTableIsTotal: every (class, zfs kind, mutated) has an outcome, and
// the CSI MUSTs hold.
func TestErrTableIsTotal(t *testing.T) {
	for c := range decide.NClass {
		for k := range decide.NZKind {
			for m := range 2 {
				o := decide.ErrTable[c][k][m]
				if !o.Done && o.Code == code.OK {
					t.Errorf("class %d kind %d mutated %d: no outcome", c, k, m)
				}
			}
		}
	}
}
