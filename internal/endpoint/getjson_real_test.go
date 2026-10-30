package endpoint_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

// captureRunner keeps the stdout of the last command it ran.
type captureRunner struct {
	r    zfs.Runner
	last []byte
}

func (c *captureRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := c.r.Run(ctx, name, args...)
	c.last = out
	return out, err
}

var createtxgRE = regexp.MustCompile(`"createtxg":"[0-9]+"`)

// The scan's JSON as real OpenZFS 2.4 prints it, against the simulator's
// printer (zfstest.AppendGetJSON, derived from the 2.4.0 source) and the
// strict decoder: the same commands run on the pool and on a Sim whose
// pool has the same name, and `zfs get -j -p` must print the same bytes
// (createtxg aside: the Sim's txgs are its own). Covers every dataset type
// the scan reads, all three source kinds a user property has here (local,
// inherited from a dataset, none), an origin, and a value carrying every
// character the printer escapes. Only ASCII is compared byte for byte; how
// non-ASCII and invalid UTF-8 fare in the runner's locale is checked for
// fail-closed and logged as evidence.
//
// Real pool only (ZFS_TEST_POOL): on the simulator it would compare the
// printer with itself.
func TestRealGetJSONMatchesSim(t *testing.T) {
	if os.Getenv("ZFS_TEST_POOL") == "" {
		t.Skip("real pool only: compares the simulator's JSON with zfs's")
	}
	t.Parallel()
	ctx := context.Background()
	_, h := setup(t)
	p := h.Parent
	pool, _, _ := strings.Cut(p, "/")
	sim := zfstest.New(pool)
	if _, err := sim.Run(ctx, "zfs", "create", "-p", p); err != nil {
		t.Fatal(err)
	}
	// every escape nvlist_print_json writes for ASCII, plus raw '/' and DEL
	esc := "a\nb\tc \"q\" back\\slash /s \x01 del\x7f\r\f\b"
	for _, args := range [][]string{
		{"create", p + "/fs"},
		{"set", "jsontest:note=" + esc, p + "/fs"},
		{"create", p + "/fs/child"},
		{"snapshot", p + "/fs@s"},
		{"clone", p + "/fs@s", p + "/clone"},
		{"create", "-s", "-V", "1048576", p + "/vol"},
		{"set", `jsontest:note={"x":"y"}`, p + "/vol"},
		{"snapshot", p + "/vol@s"},
	} {
		// raw argv: Client.Set refuses control characters (checkProps)
		if _, err := h.Z.Runner.Run(ctx, "zfs", args...); err != nil {
			t.Fatalf("zfs %q on the pool: %v", args, err)
		}
		if _, err := sim.Run(ctx, "zfs", args...); err != nil {
			t.Fatalf("zfs %q on the Sim: %v", args, err)
		}
	}
	props := []string{"type", "volsize", "origin", "jsontest:note", "jsontest:unset"}
	o := zfs.ListOptions{Types: []zfs.Type{zfs.TypeFilesystem, zfs.TypeVolume, zfs.TypeSnapshot}, Depth: -1, Props: props}
	scan := func(r zfs.Runner, root string, o zfs.ListOptions) (*zfs.Columns, []byte, error) {
		c := &captureRunner{r: r}
		cols, err := zfs.New(c).ListColumns(ctx, root, o)
		return cols, c.last, err
	}
	cols, zfsOut, err := scan(h.Z.Runner, p, o)
	if err != nil {
		t.Fatalf("decoding the pool's JSON: %v\n%s", err, zfsOut)
	}
	_, simOut, err := scan(sim, p, o)
	if err != nil {
		t.Fatalf("decoding the Sim's JSON: %v\n%s", err, simOut)
	}
	norm := func(b []byte) []byte { return createtxgRE.ReplaceAll(b, []byte(`"createtxg":"0"`)) }
	same := func(what string, zfsOut, simOut []byte) {
		t.Helper()
		if a, b := norm(zfsOut), norm(simOut); !bytes.Equal(a, b) {
			i := 0
			for i < min(len(a), len(b)) && a[i] == b[i] {
				i++
			}
			from := max(0, i-120)
			t.Fatalf("%s: zfs and the Sim print different JSON from byte %d:\n zfs %q\n sim %q\nfull zfs output:\n%s",
				what, i, a[from:min(len(a), i+120)], b[from:min(len(b), i+120)], zfsOut)
		}
	}
	same("scan", zfsOut, simOut)
	t.Logf("zfs get -j -p output (%d bytes) matches the Sim byte for byte", len(zfsOut))
	// Get and List issue shapes of their own: one dataset or snapshot
	// with neither -t nor -d, and List's default types at depth 1
	for _, name := range []string{p + "/fs", p + "/vol", p + "/fs@s", p + "/clone"} {
		var outs [2][]byte
		for i, r := range []zfs.Runner{h.Z.Runner, sim} {
			c := &captureRunner{r: r}
			d, err := zfs.New(c).Get(ctx, name, props...)
			if err != nil {
				t.Fatalf("Get %s: %v\n%s", name, err, c.last)
			}
			if d.Name != name || len(d.Props) != len(props) {
				t.Fatalf("Get %s = %+v", name, d)
			}
			outs[i] = c.last
		}
		same("get "+name, outs[0], outs[1])
	}
	var lists [2][]byte
	for i, r := range []zfs.Runner{h.Z.Runner, sim} {
		c := &captureRunner{r: r}
		ds, err := zfs.New(c).List(ctx, p, zfs.ListOptions{Props: props})
		if err != nil || len(ds) != 3 {
			t.Fatalf("List %s = %+v, %v\n%s", p, ds, err, c.last)
		}
		lists[i] = c.last
	}
	same("list "+p, lists[0], lists[1])
	want := []string{p, p + "/clone", p + "/fs", p + "/fs/child", p + "/fs@s", p + "/vol", p + "/vol@s"}
	got := map[string]bool{}
	for _, n := range cols.Names {
		got[n] = true
	}
	for _, n := range want {
		if !got[n] {
			t.Errorf("scan lacks %s: %q", n, cols.Names)
		}
	}
	if len(cols.Names) != len(want) {
		t.Errorf("scan names %q, want %q", cols.Names, want)
	}
	for i, n := range cols.Names {
		if n == p+"/fs" {
			if v := cols.Cell(i, 3); v.Value != esc || v.Source != "local" {
				t.Errorf("%s note = %+v, want %q local", n, v, esc)
			}
		}
	}

	// Outside ASCII the printer depends on the locale zfs runs in
	// (mbrtowc after setlocale(LC_ALL, "")): under C.UTF-8 é is printed
	// and code points above U+FFFF are dropped; under C every byte >= 0x80
	// stops the printer mid-document, with exit 0. Either way the scan
	// must return the value as printed or fail; it is scanned on its own,
	// so the main scan above stays locale-independent.
	u := p + "/u"
	if _, err := h.Z.Runner.Run(ctx, "zfs", "create", "-o", "jsontest:note=café 😀", u); err != nil {
		t.Fatal(err)
	}
	one := zfs.ListOptions{Depth: 0, Props: []string{"type", "jsontest:note"}}
	switch c, out, err := scan(h.Z.Runner, u, one); {
	case err != nil:
		var ee *zfs.ExitError
		if errors.As(err, &ee) {
			t.Fatalf("zfs failed on a non-ASCII value: %v", err)
		}
		t.Logf("EVIDENCE non-ASCII: the scan fails closed (truncated JSON, exit 0: C locale): %v; output %q", err, out)
	case c.Cell(0, 1).Value == "café ":
		t.Logf("EVIDENCE non-ASCII: é printed, U+1F600 dropped (UTF-8 locale): %q", out)
	case c.Cell(0, 1).Value == "café 😀":
		t.Logf("EVIDENCE non-ASCII: printed whole, U+1F600 kept: %q", out)
	default:
		t.Fatalf("non-ASCII value read back as %q: %q", c.Cell(0, 1).Value, out)
	}
	// Whether zfs set takes invalid UTF-8 at all was never measured.
	if _, err := h.Z.Runner.Run(ctx, "zfs", "set", "jsontest:note=ab\xffcd", u); err != nil {
		t.Logf("EVIDENCE invalid UTF-8: zfs set refuses it: %v", err)
		return
	}
	if c, out, err := scan(h.Z.Runner, u, one); err == nil {
		t.Fatalf("a value of invalid UTF-8 scanned as %q: %q", c.Cell(0, 1).Value, out)
	} else {
		t.Logf("EVIDENCE invalid UTF-8: zfs set takes it, and the scan fails closed: %v", err)
	}
}
