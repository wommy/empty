package zfs_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

// The goldens in testdata/getjson were derived from the OpenZFS 2.4.0
// source (zfs_main.c get_callback and fill_dataset_info, libzfs_util.c
// zprop_nvlist_one_property, libnvpair_json.c nvlist_print_json); the
// real-pool test TestRealGetJSONMatchesSim (internal/endpoint) checks the
// simulator's printer against what zfs actually prints.
var (
	basicProps = []string{"type", "volsize", "origin", "creation", "refreservation", "p:managed", "p:name"}
	xProps     = []string{"type", "p:note", "p:name"}
)

func golden(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "getjson", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func decode(b []byte, props []string) (*zfs.Columns, error) {
	return zfs.DecodeGetJSON(bytes.NewReader(b), props, 0)
}

// columns builds the Columns a decode should produce: per dataset, its
// name, then value and source for each property in order.
func columns(props []string, rows ...[]string) *zfs.Columns {
	c := &zfs.Columns{Props: props}
	for _, r := range rows {
		c.Names = append(c.Names, r[0])
		for i := 1; i < len(r); i += 2 {
			c.Cells = append(c.Cells, zfs.Prop{Value: r[i], Source: r[i+1]})
		}
	}
	return c
}

// sameColumns compares what a caller can see (Cells may carry capacity).
func sameColumns(a, b *zfs.Columns) bool {
	return reflect.DeepEqual(a.Props, b.Props) && reflect.DeepEqual(a.Names, b.Names) &&
		len(a.Cells) == len(b.Cells) && (len(a.Cells) == 0 || reflect.DeepEqual(a.Cells, b.Cells))
}

// Sources come out in the -Hp spelling (zprop_print_one_property), so the
// Table code downstream reads the same strings it always did.
func TestGoldenBasic(t *testing.T) {
	t.Parallel()
	got, err := decode(golden(t, "basic.json"), basicProps)
	if err != nil {
		t.Fatal(err)
	}
	inh := "inherited from tank/csi/pvc-a"
	want := columns(basicProps,
		[]string{"tank/csi", "filesystem", "-", "-", "-", "-", "-", "1729000000", "-", "0", "default", "-", "-", "-", "-"},
		[]string{"tank/csi/pvc-a", "volume", "-", "10737418240", "local", "-", "-", "1729000100", "-", "11811160064", "local", "true", "local", "pvc-a", "local"},
		[]string{"tank/csi/pvc-a@snap1", "snapshot", "-", "10737418240", "-", "-", "-", "1729000200", "-", "-", "-", "true", inh, "pvc-a", inh},
		[]string{"tank/csi/pvc-b", "volume", "-", "10737418240", "local", "tank/csi/pvc-a@snap1", "-", "1729000300", "-", "0", "default", "true", "local", "pvc-b", "local"},
	)
	if !sameColumns(got, want) {
		t.Fatalf("got\n%+v\nwant\n%+v", got, want)
	}
}

func TestGoldenEscapes(t *testing.T) {
	t.Parallel()
	got, err := decode(golden(t, "escapes.json"), xProps)
	if err != nil {
		t.Fatal(err)
	}
	// U+1F600 is missing: nvlist_print_json drops code points above U+FFFF
	note := "line1\nline2\tTAB \"q\" back\\slash /slash é中 \x01 DEL\x7f\r\f\b"
	want := columns(xProps,
		[]string{"tank/x", "filesystem", "-", note, "local", "-", "-"},
		[]string{"tank/x/odd name", "filesystem", "-", note, "inherited from tank/x", "-", "-"},
		[]string{"tank/x/vol", "volume", "-",
			`x"}}},"tank/x/phantom":{"name":"tank/x/phantom","type":"VOLUME"`, "local",
			"x\tlocal\ntank/x/phantom\tp:name\tpwned\tlocal", "local"},
	)
	if !sameColumns(got, want) {
		t.Fatalf("got\n%#v\nwant\n%#v", got, want)
	}
}

func TestGoldenInvalidUTF8FailsClosed(t *testing.T) {
	t.Parallel()
	// zfs exits 0 here: only the decoder stands between this and a table
	if c, err := decode(golden(t, "invalid-utf8.json"), xProps); err == nil {
		t.Fatalf("truncated document accepted: %+v", c)
	}
}

func TestGoldenEmpty(t *testing.T) {
	t.Parallel()
	c, err := decode(golden(t, "empty.json"), xProps)
	if err != nil || len(c.Names) != 0 || len(c.Cells) != 0 {
		t.Fatalf("%+v, %v", c, err)
	}
}

func jsonDS(name, typ string, txg uint64, kv ...string) zfstest.JSONDataset {
	d := zfstest.JSONDataset{Name: name, Type: typ, Createtxg: txg}
	for i := 0; i < len(kv); i += 3 {
		d.Props = append(d.Props, zfstest.JSONProp{Name: kv[i], Value: kv[i+1], Source: kv[i+2]})
	}
	return d
}

// The simulator's printer reproduces the source-derived goldens byte for
// byte, so contract tests through the Sim exercise the decoder on the
// bytes zfs prints.
func TestSimPrinterMatchesGoldens(t *testing.T) {
	t.Parallel()
	note := "line1\nline2\tTAB \"q\" back\\slash /slash é中😀 \x01 DEL\x7f\r\f\b"
	inh := "inherited from tank/csi/pvc-a"
	for _, tc := range []struct {
		file string
		ds   []zfstest.JSONDataset
	}{
		{"basic.json", []zfstest.JSONDataset{
			jsonDS("tank/csi", "filesystem", 50, "type", "filesystem", "-", "volsize", "-", "-", "origin", "-", "-", "creation", "1729000000", "-", "refreservation", "0", "default", "p:managed", "-", "-", "p:name", "-", "-"),
			jsonDS("tank/csi/pvc-a", "volume", 100, "type", "volume", "-", "volsize", "10737418240", "local", "origin", "-", "-", "creation", "1729000100", "-", "refreservation", "11811160064", "local", "p:managed", "true", "local", "p:name", "pvc-a", "local"),
			jsonDS("tank/csi/pvc-a@snap1", "snapshot", 120, "type", "snapshot", "-", "volsize", "10737418240", "-", "origin", "-", "-", "creation", "1729000200", "-", "refreservation", "-", "-", "p:managed", "true", inh, "p:name", "pvc-a", inh),
			jsonDS("tank/csi/pvc-b", "volume", 130, "type", "volume", "-", "volsize", "10737418240", "local", "origin", "tank/csi/pvc-a@snap1", "-", "creation", "1729000300", "-", "refreservation", "0", "default", "p:managed", "true", "local", "p:name", "pvc-b", "local"),
		}},
		{"escapes.json", []zfstest.JSONDataset{
			jsonDS("tank/x", "filesystem", 7, "type", "filesystem", "-", "p:note", note, "local", "p:name", "-", "-"),
			jsonDS("tank/x/odd name", "filesystem", 8, "type", "filesystem", "-", "p:note", note, "inherited from tank/x", "p:name", "-", "-"),
			jsonDS("tank/x/vol", "volume", 9, "type", "volume", "-", "p:note", `x"}}},"tank/x/phantom":{"name":"tank/x/phantom","type":"VOLUME"`, "local", "p:name", "x\tlocal\ntank/x/phantom\tp:name\tpwned\tlocal", "local"),
		}},
		{"invalid-utf8.json", []zfstest.JSONDataset{
			jsonDS("tank/x", "filesystem", 7, "type", "filesystem", "-", "p:note", "ab\xffcd", "local", "p:name", "-", "-"),
		}},
		{"empty.json", nil},
	} {
		if got, want := zfstest.AppendGetJSON(nil, tc.ds), golden(t, tc.file); !bytes.Equal(got, want) {
			t.Errorf("%s:\n got %q\nwant %q", tc.file, got, want)
		}
	}
}

// Each mutation of a valid document must be rejected.
func TestDecodeStrict(t *testing.T) {
	t.Parallel()
	base := string(golden(t, "basic.json"))
	snap := `"tank/csi/pvc-a@snap1":{"name":"tank/csi/pvc-a@snap1","type":"SNAPSHOT","pool":"tank","createtxg":"120","dataset":"tank/csi/pvc-a","snapshot_name":"snap1",`
	if !strings.Contains(base, snap) {
		t.Fatal("fixture drifted")
	}
	pvcA := `"tank/csi/pvc-a":{"name":"tank/csi/pvc-a","type":"VOLUME","pool":"tank","createtxg":"100",`
	firstProp := `"properties":{"type":{"value":"filesystem","source":{"type":"NONE","data":"-"}},`
	rep := func(old, new string) string {
		if !strings.Contains(base, old) {
			t.Fatalf("no %q in fixture", old)
		}
		return strings.Replace(base, old, new, 1)
	}
	// a whole dataset entry, to repeat it
	start := strings.Index(base, `"tank/csi/pvc-b":`)
	entry := base[start : len(base)-len("}}\n")]
	for name, doc := range map[string]string{
		"duplicate dataset key":        strings.TrimSuffix(base, "}}\n") + "," + entry + "}}\n",
		"duplicate property":           rep(firstProp, firstProp+`"type":{"value":"filesystem","source":{"type":"NONE","data":"-"}},`),
		"property not requested":       rep(firstProp, firstProp+`"used":{"value":"0","source":{"type":"NONE","data":"-"}},`),
		"property missing":             rep(`"p:name":{"value":"pvc-b","source":{"type":"LOCAL","data":"-"}}`, `"p:other":{"value":"pvc-b","source":{"type":"LOCAL","data":"-"}}`),
		"unknown top-level member":     rep(`{"output_version"`, `{"x":1,"output_version"`),
		"unknown dataset member":       rep(pvcA, pvcA+`"guid":"1",`),
		"unknown property member":      rep(`{"value":"volume","source"`, `{"value":"volume","received":"-","source"`),
		"unknown source member":        rep(`"source":{"type":"LOCAL","data":"-"}`, `"source":{"type":"LOCAL","data":"-","x":"-"}`),
		"case-folded member name":      rep(pvcA, strings.Replace(pvcA, `"name"`, `"Name"`, 1)),
		"missing createtxg":            rep(pvcA, strings.Replace(pvcA, `"createtxg":"100",`, ``, 1)),
		"numeric createtxg":            rep(pvcA, strings.Replace(pvcA, `"createtxg":"100"`, `"createtxg":100`, 1)),
		"json-int value":               rep(`{"value":"10737418240","source":{"type":"LOCAL"`, `{"value":10737418240,"source":{"type":"LOCAL"`),
		"null value":                   rep(`{"value":"pvc-b"`, `{"value":null`),
		"name does not match key":      rep(pvcA, strings.Replace(pvcA, `"name":"tank/csi/pvc-a"`, `"name":"tank/csi/pvc-z"`, 1)),
		"pool does not match name":     rep(pvcA, strings.Replace(pvcA, `"pool":"tank"`, `"pool":"tonk"`, 1)),
		"type property disagrees":      rep(pvcA, strings.Replace(pvcA, `"VOLUME"`, `"FILESYSTEM"`, 1)),
		"bookmark type":                rep(pvcA, strings.Replace(pvcA, `"VOLUME"`, `"BOOKMARK"`, 1)),
		"snapshot without dataset":     rep(snap, strings.Replace(snap, `"dataset":"tank/csi/pvc-a",`, ``, 1)),
		"snapshot_name mismatch":       rep(snap, strings.Replace(snap, `"snapshot_name":"snap1"`, `"snapshot_name":"snap2"`, 1)),
		"snapshot fields on a volume":  rep(pvcA, pvcA+`"snapshot_name":"x",`),
		"output_version 0.2":           rep(`"vers_minor":1`, `"vers_minor":2`),
		"output_version 0.1 as 1.0e0":  rep(`"vers_minor":1`, `"vers_minor":1.0e0`),
		"other command":                rep(`"zfs get"`, `"zfs list"`),
		"missing datasets":             `{"output_version":{"command":"zfs get","vers_major":0,"vers_minor":1}}` + "\n",
		"trailing garbage":             base + "x",
		"second document":              base + base,
		"trailing object":              base + "{}",
		"raw invalid UTF-8":            rep(`"pvc-b","source"`, "\"pvc-\xffb\",\"source\""),
		"lone surrogate escape":        rep(`"pvc-b","source"`, `"pvc-\ud800b","source"`),
		"inherited without data":       rep(`"type":"INHERITED","data":"tank/csi/pvc-a"`, `"type":"INHERITED","data":"-"`),
		"local with data":              rep(`"type":"LOCAL","data":"-"`, `"type":"LOCAL","data":"tank"`),
		"unknown source type":          rep(`"type":"LOCAL","data":"-"`, `"type":"PROPERTY","data":"-"`),
		"array instead of properties":  rep(firstProp, `"properties":[],"x":{`),
		"empty input (nonzero exit)":   "",
		"whitespace only":              "\n",
		"datasets is an array":         `{"output_version":{"command":"zfs get","vers_major":0,"vers_minor":1},"datasets":[]}` + "\n",
		"repeated vers_minor":          rep(`"vers_minor":1`, `"vers_minor":1,"vers_minor":1`),
		"repeated dataset member":      rep(pvcA, pvcA+`"pool":"tank",`),
		"repeated value":               rep(`{"value":"pvc-b",`, `{"value":"pvc-b","value":"pvc-b",`),
		"repeated source":              rep(`"source":{"type":"LOCAL","data":"-"}}`, `"source":{"type":"LOCAL","data":"-"},"source":{"type":"LOCAL","data":"-"}}`),
		"repeated source type":         rep(`"source":{"type":"LOCAL","data":"-"}`, `"source":{"type":"LOCAL","type":"LOCAL","data":"-"}`),
		"repeated source data":         rep(`"source":{"type":"LOCAL","data":"-"}`, `"source":{"type":"LOCAL","data":"-","data":"-"}`),
		"repeated datasets object":     strings.TrimSuffix(base, "}\n") + `,"datasets":{}}` + "\n",
		"duplicate output_version key": rep(`"datasets":{`, `"output_version":{"command":"zfs get","vers_major":0,"vers_minor":1},"datasets":{`),
		"whitespace in a property":     rep(`{"value":"pvc-b",`, `{"value": "pvc-b",`),
		"source before value":          rep(`{"value":"pvc-b","source":{"type":"LOCAL","data":"-"}}`, `{"source":{"type":"LOCAL","data":"-"},"value":"pvc-b"}`),
		"data before type":             rep(`{"value":"pvc-b","source":{"type":"LOCAL","data":"-"}}`, `{"value":"pvc-b","source":{"data":"-","type":"LOCAL"}}`),
		"escaped source type":          rep(`{"value":"pvc-b","source":{"type":"LOCAL","data":"-"}}`, `{"value":"pvc-b","source":{"type":"LOC\u0041L","data":"-"}}`),
		"property is a string":         rep(`"p:name":{"value":"pvc-b","source":{"type":"LOCAL","data":"-"}}`, `"p:name":"pvc-b"`),
	} {
		if c, err := decode([]byte(doc), basicProps); err == nil {
			t.Errorf("%s: accepted: %v", name, c.Names)
		}
	}
	for _, props := range [][]string{{"type", "type"}, {"type", "all"}, {"type", ""}} {
		if _, err := decode([]byte(base), props); err == nil {
			t.Errorf("property list %q accepted", props)
		}
	}
}

// Every prefix that cuts the document fails; the document with or without
// its trailing newline decodes. zfs truncates its own output this way, with
// exit 0, when a value is not valid in its locale (nvlist_print_json stops
// at the first mbrtowc failure and zcmd_print_json still prints "\n").
func TestTruncationAtEveryOffsetFailsClosed(t *testing.T) {
	t.Parallel()
	sim := zfstest.New("tank")
	ctx := context.Background()
	for _, args := range [][]string{
		{"create", "-p", "-o", "p:name=a\nb", "tank/k/v1"},
		{"create", "-V", "1048576", "tank/k/v2"},
		{"snapshot", "tank/k/v2@s"},
	} {
		if _, err := sim.Run(ctx, "zfs", args...); err != nil {
			t.Fatal(err)
		}
	}
	simOut, err := sim.Run(ctx, "zfs", "get", "-j", "-p", "-t", "filesystem,volume,snapshot", "-r", strings.Join(basicProps, ","), "tank")
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		doc   []byte
		props []string
	}{
		"basic":   {golden(t, "basic.json"), basicProps},
		"escapes": {golden(t, "escapes.json"), xProps},
		"empty":   {golden(t, "empty.json"), xProps},
		"sim":     {simOut, basicProps},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			full := bytes.TrimSuffix(tc.doc, []byte("\n"))
			for i := range len(full) {
				if _, err := decode(full[:i], tc.props); err == nil {
					t.Fatalf("prefix of %d/%d bytes accepted", i, len(full))
				}
				if _, err := decode(append(full[:i:i], '\n'), tc.props); err == nil {
					t.Fatalf("prefix of %d/%d bytes plus newline accepted", i, len(full))
				}
			}
			for _, ok := range [][]byte{full, tc.doc} {
				if _, err := decode(ok, tc.props); err != nil {
					t.Fatalf("whole document: %v", err)
				}
			}
		})
	}
}

// A user property value that `zfs get -Hp` prints as lines for a dataset
// that does not exist: through the JSON scan it is only ever the value of
// the property it was set on. Likewise a value written as JSON syntax.
func TestNewlineForgeryIsOnlyAValue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sim := zfstest.New("tank")
	props := []string{"type", "p:managed", "p:name"}
	forged := "x\tlocal\ntank/k/phantom\ttype\tvolume\t-\ntank/k/phantom\tp:managed\ttrue\tlocal\ntank/k/phantom\tp:name\tpwned"
	jsonForged := `x"}}}},"tank/k/phantom":{"name":"tank/k/phantom","type":"VOLUME","pool":"tank","createtxg":"1","properties":{"type":{"value":"volume","source":{"type":"NONE","data":"-"}}`
	// the Sim is driven directly: Client.Set refuses newlines, a foreign
	// writer need not
	for _, args := range [][]string{
		{"create", "-p", "tank/k/a"},
		{"create", "tank/k/b"},
		{"set", "p:name=" + forged, "tank/k/a"},
		{"set", "p:name=" + jsonForged, "tank/k/b"},
	} {
		if _, err := sim.Run(ctx, "zfs", args...); err != nil {
			t.Fatal(err)
		}
	}
	// the hole the text scan had: -Hp prints the phantom's lines raw
	text, err := sim.Run(ctx, "zfs", "get", "-Hp", "-o", "name,property,value,source", "-d", "1", strings.Join(props, ","), "tank/k")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(text), "\ntank/k/phantom\tp:managed\ttrue\tlocal\n") {
		t.Fatalf("expected -Hp to print the forged lines raw:\n%s", text)
	}
	c, err := zfs.New(sim).ListColumns(ctx, "tank/k", zfs.ListOptions{Depth: 0, Props: props})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"tank/k", "tank/k/a", "tank/k/b"}; !reflect.DeepEqual(c.Names, want) {
		t.Fatalf("names %q, want %q", c.Names, want)
	}
	if v := c.Cell(1, 2); v.Value != forged || v.Source != "local" {
		t.Fatalf("tank/k/a p:name = %+v", v)
	}
	if v := c.Cell(2, 2); v.Value != jsonForged || v.Source != "local" {
		t.Fatalf("tank/k/b p:name = %+v", v)
	}
}

// ListColumns asks for JSON with exact numbers, not the -Hp columns.
func TestListColumnsArgv(t *testing.T) {
	t.Parallel()
	r := &argvRunner{out: golden(t, "empty.json")}
	if _, err := zfs.New(r).ListColumns(context.Background(), "tank/csi", zfs.ListOptions{
		Types: []zfs.Type{zfs.TypeFilesystem, zfs.TypeSnapshot}, Depth: 1, Props: xProps,
	}); err != nil {
		t.Fatal(err)
	}
	want := "get -j -p -t filesystem,snapshot -d 2 type,p:note,p:name tank/csi"
	if got := strings.Join(r.args, " "); got != want {
		t.Fatalf("argv %q, want %q", got, want)
	}
}

type argvRunner struct {
	args []string
	out  []byte
}

func (r *argvRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	r.args = args
	return r.out, nil
}

// A non-zero exit wins over whatever was printed, and a decode failure is
// reported even when zfs exited 0. Driven through a real process.
func TestListColumnsExitStatus(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	doc := filepath.Join(dir, "doc")
	if err := os.WriteFile(doc, golden(t, "basic.json"), 0o644); err != nil {
		t.Fatal(err)
	}
	n := 0
	script := func(body string) *zfs.Client {
		n++
		p := filepath.Join(dir, fmt.Sprintf("zfs%d", n))
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return &zfs.Client{Runner: zfs.LocalRunner{}, Binary: p}
	}
	ctx := context.Background()
	o := zfs.ListOptions{Props: basicProps}

	c, err := script("cat "+doc).ListColumns(ctx, "tank/csi", o)
	if err != nil || len(c.Names) != 4 {
		t.Fatalf("clean run: %v, %v", c, err)
	}
	// a whole document, then a failure exit: never trusted
	_, err = script("cat "+doc+"; echo \"cannot open 'tank/csi': dataset does not exist\" >&2; exit 1").ListColumns(ctx, "tank/csi", o)
	var ee *zfs.ExitError
	if !errors.As(err, &ee) || !zfs.IsNotFound(err) {
		t.Fatalf("exit 1 after a full document: %v", err)
	}
	// garbage with exit 0: a decode error, not an exit error
	_, err = script("echo '{\"x\"'; head -c 100000 /dev/zero").ListColumns(ctx, "tank/csi", o)
	if err == nil || errors.As(err, &ee) {
		t.Fatalf("garbage with exit 0: %v", err)
	}
	// truncated by an invalid UTF-8 value, exit 0, as zfs does it
	inv := filepath.Join(dir, "inv")
	if err := os.WriteFile(inv, golden(t, "invalid-utf8.json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err = script("cat "+inv).ListColumns(ctx, "tank/x", zfs.ListOptions{Props: xProps}); err == nil {
		t.Fatal("truncated document with exit 0 accepted")
	}
	if _, err := zfs.New(failRunner("cannot open 'tank/csi': dataset does not exist\n")).ListColumns(ctx, "tank/csi", o); !zfs.IsNotFound(err) {
		t.Fatalf("classified failure: %v", err)
	}
}

// datasetKeys walks the document with an independent parser (encoding/json
// v1 tokens) and returns the member names of the top-level "datasets" object
// in order. Inside it, depth-2 strings are keys: every value is an object.
func datasetKeys(b []byte) ([]string, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	var keys []string
	depth, inDatasets := 0, false
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return keys, nil
		}
		if err != nil {
			return nil, err
		}
		switch v := tok.(type) {
		case json.Delim:
			if v == '{' || v == '[' {
				depth++
			} else if depth--; depth == 1 {
				inDatasets = false
			}
		case string:
			switch {
			case depth == 1 && v == "datasets":
				inDatasets = true
			case depth == 2 && inDatasets:
				keys = append(keys, v)
			}
		}
	}
}

// zfs output is untrusted input: user property values are arbitrary text.
// The seeds (the goldens, f.Add below and testdata/fuzz/FuzzDecodeGetJSON)
// replay in every `go test`; exploratory fuzzing runs nightly.
//
// Properties checked on every accepted document: Names are exactly the
// "datasets" keys an independent parser finds, in order, so no row can
// exist that zfs did not list (seam S1); names are unique; the document is
// valid JSON to encoding/json too; Cells is Names x Props with no empty
// source.
func FuzzDecodeGetJSON(f *testing.F) {
	for _, n := range []string{"basic.json", "escapes.json", "invalid-utf8.json", "empty.json"} {
		f.Add(golden(f, n))
	}
	f.Add([]byte(`{"output_version":{"command":"zfs get","vers_major":0,"vers_minor":1},"datasets":{"t":{"name":"t","type":"FILESYSTEM","pool":"t","createtxg":"1","properties":{"type":{"value":"filesystem","source":{"type":"NONE","data":"-"}},"p:managed":{"value":"x","source":{"type":"INHERITED","data":"t"}}}}}}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		for _, props := range [][]string{basicProps, xProps, {"type", "p:managed"}} {
			c, err := decode(b, props)
			if err != nil {
				continue
			}
			if len(c.Cells) != len(c.Names)*len(props) {
				t.Fatalf("cells %d != %d x %d", len(c.Cells), len(c.Names), len(props))
			}
			keys, err := datasetKeys(b)
			if err != nil {
				t.Fatalf("accepted a document encoding/json rejects: %v", err)
			}
			if !reflect.DeepEqual(c.Names, keys) && !(len(c.Names) == 0 && len(keys) == 0) {
				t.Fatalf("names %q, keys %q", c.Names, keys)
			}
			seen := map[string]bool{}
			for i, n := range c.Names {
				if seen[n] {
					t.Fatalf("duplicate name %q", n)
				}
				seen[n] = true
				for j := range props {
					if p := c.Cell(i, j); p.Source == "" {
						t.Fatalf("%s %s: empty source", n, props[j])
					}
				}
			}
		}
	})
}

// The Sim lists datasets in zfs_compare order (zfs_iter.c), as zfs does:
// a dataset, then its snapshots by createtxg, then its children. Plain
// string order would put tank/a/b before tank/a@z.
func TestSimGetOrderIsZFSCompare(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sim := zfstest.New("tank")
	for _, args := range [][]string{
		{"create", "tank/a"},
		{"snapshot", "tank/a@z"},
		{"snapshot", "tank/a@b"},
		{"create", "tank/a/b"},
		{"create", "tank/a-b"},
	} {
		if _, err := sim.Run(ctx, "zfs", args...); err != nil {
			t.Fatal(err)
		}
	}
	c, err := zfs.New(sim).ListColumns(ctx, "tank", zfs.ListOptions{
		Types: []zfs.Type{zfs.TypeFilesystem, zfs.TypeSnapshot}, Depth: -1, Props: []string{"type"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"tank", "tank/a", "tank/a@z", "tank/a@b", "tank/a-b", "tank/a/b"}
	if !reflect.DeepEqual(c.Names, want) {
		t.Fatalf("names %q, want %q", c.Names, want)
	}
}
