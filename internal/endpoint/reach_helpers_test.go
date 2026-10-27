package endpoint

// Helpers for the impossible-state reach tests (ported from the wave-5
// review): a runner that answers every `zfs get` with fixed bytes, so
// states the simulator cannot produce reach the parser and the planners.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

const rParent = "tank/v"

var (
	rNames = props{DefaultPropertyPrefix}
	rNow   = time.Unix(2_000_000_000, 0)
)

// rawRunner answers every `zfs get` with raw and records every other call,
// succeeding unless fail says otherwise.
type rawRunner struct {
	mu    sync.Mutex
	raw   []byte
	calls [][]string
	fail  func(args []string) error
}

func (r *rawRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, args)
	if r.fail != nil {
		if err := r.fail(args); err != nil {
			return nil, err
		}
	}
	if len(args) > 0 && args[0] == "get" {
		return r.raw, nil
	}
	return nil, nil
}

func (r *rawRunner) mutations() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out [][]string
	for _, c := range r.calls {
		if c[0] != "get" {
			out = append(out, c)
		}
	}
	return out
}

func rEndpoint(t testing.TB, r zfs.Runner) *Endpoint {
	t.Helper()
	ep, err := New(zfs.New(r), Config{Parent: rParent, Now: func() time.Time { return rNow }, LockWait: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return ep
}

// dsCells is a full, well-formed set of `zfs get` cells for one dataset,
// with overrides: every requested column printed exactly once.
func dsCells(name string, typ zfs.Type, over map[string][2]string) [][4]string {
	def := map[string][2]string{
		"type": {string(typ), "-"}, "volsize": {"-", "-"}, "volblocksize": {"-", "-"},
		"refquota": {"0", "default"}, "refreservation": {"0", "default"}, "origin": {"-", "-"},
		"creation": {"1000000000", "-"}, "defer_destroy": {"off", "-"}, "mountpoint": {"/" + name, "default"},
		"sharenfs": {"off", "default"}, "mounted": {"yes", "-"},
	}
	var out [][4]string
	for _, p := range rNames.columns() {
		v, ok := over[p]
		if !ok {
			v, ok = def[p]
		}
		if !ok {
			v = [2]string{"-", "-"}
		}
		out = append(out, [4]string{name, p, v[0], v[1]})
	}
	return out
}

func owned(id string, committed bool) map[string][2]string {
	m := map[string][2]string{
		rNames.managed():    {"true", "local"},
		rNames.volumeName(): {id, "local"},
	}
	if committed {
		m[rNames.success()] = [2]string{"true", "local"}
	}
	return m
}

func merge(ms ...map[string][2]string) map[string][2]string {
	out := map[string][2]string{}
	for _, m := range ms {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// rawOf renders cells as `zfs get -j -p` prints them (the simulator's
// printer): datasets in order of first appearance, properties in the
// order given, the dataset type from its "type" cell.
func rawOf(groups ...[][4]string) []byte {
	var ds []zfstest.JSONDataset
	at := map[string]int{}
	for _, g := range groups {
		for _, c := range g {
			i, ok := at[c[0]]
			if !ok {
				i = len(ds)
				at[c[0]] = i
				ds = append(ds, zfstest.JSONDataset{Name: c[0], Createtxg: uint64(i + 1)})
			}
			if c[1] == "type" {
				ds[i].Type = c[2]
			}
			ds[i].Props = append(ds[i].Props, zfstest.JSONProp{Name: c[1], Value: c[2], Source: c[3]})
		}
	}
	return zfstest.AppendGetJSON(nil, ds)
}

// tableFromRaw runs raw bytes through the real client parser and scan.
func tableFromRaw(t testing.TB, raw []byte) (Table, *Endpoint) {
	t.Helper()
	ep := rEndpoint(t, &rawRunner{raw: raw})
	tbl, err := ep.scan(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	return tbl, ep
}

// simEndpoint is an endpoint on a fresh simulator, parent rParent.
func simEndpoint(t *testing.T, cfg Config) (*Endpoint, *zfstest.Sim) {
	t.Helper()
	sim := zfstest.New("tank")
	z := zfs.New(sim)
	if err := z.CreateFilesystem(context.Background(), rParent, nil, true); err != nil {
		t.Fatal(err)
	}
	cfg.Parent = rParent
	ep, err := New(z, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return ep, sim
}
