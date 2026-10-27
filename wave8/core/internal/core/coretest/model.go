// Package coretest is a small, pure model of OpenZFS for the core's
// generated tests and benchmarks: it executes the argv ops render (create,
// snapshot, clone, set, destroy [-d]) with OpenZFS's refusals and defer
// semantics, and prints `zfs get -j -p` documents for a scope, so every test
// step goes through the real parse boundary.
package coretest

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/wommy/empty/internal/core/world"
	"github.com/wommy/empty/internal/zfs"
)

type pv struct{ v, src string } // src: LOCAL or RECEIVED

type ds struct {
	typ      byte // 'f' filesystem, 'v' volume, 's' snapshot
	props    map[string]pv
	creation int64
	guid     uint64
	txg      uint64
	origin   string
	deferred bool
}

// Model is an in-memory pool. Not safe for concurrent use.
type Model struct {
	d    map[string]*ds
	tick uint64
	cidx map[string][]string // origin -> clones, while printing
}

// New returns a model holding the given filesystems (e.g. "tank", "tank/k8s").
func New(fs ...string) *Model {
	m := &Model{d: map[string]*ds{}}
	for _, f := range fs {
		m.add(f, 'f', nil)
	}
	return m
}

// Clone deep-copies the model.
func (m *Model) Clone() *Model {
	c := &Model{d: make(map[string]*ds, len(m.d)), tick: m.tick}
	for k, v := range m.d {
		x := *v
		x.props = maps.Clone(v.props)
		c.d[k] = &x
	}
	return c
}

func (m *Model) add(n string, typ byte, props map[string]pv) *ds {
	m.tick++
	if props == nil {
		props = map[string]pv{}
	}
	d := &ds{typ: typ, props: props, creation: 1729000000 + int64(m.tick), guid: 1000 + m.tick, txg: m.tick}
	m.d[n] = d
	return d
}

// Has reports whether name exists.
func (m *Model) Has(n string) bool { _, ok := m.d[n]; return ok }

// SetLocal sets a property locally, as an administrator would (no checks).
func (m *Model) SetLocal(n, k, v string) { m.d[n].props[k] = pv{v, "LOCAL"} }

// Receive sets a property with source received (zfs recv).
func (m *Model) Receive(n, k, v string) { m.d[n].props[k] = pv{v, "RECEIVED"} }

// Inherit removes a local property (zfs inherit).
func (m *Model) Inherit(n, k string) { delete(m.d[n].props, k) }

// Admin runs a command outside the endpoint (sanoid, an operator).
func (m *Model) Admin(argv ...string) error { return m.Exec(argv) }

func fail(k zfs.Kind, n, f string, a ...any) error {
	return &zfs.Error{Kind: k, Dataset: n, Stderr: fmt.Sprintf(f, a...)}
}

func parentOf(n string) string {
	if i := strings.LastIndexByte(n, '/'); i >= 0 {
		return n[:i]
	}
	return ""
}

// Run implements decide.Runner.
func (m *Model) Run(_ context.Context, argv []string) error { return m.Exec(argv) }

// Guid implements decide.Runner.
func (m *Model) Guid(_ context.Context, n string) (uint64, error) {
	d, ok := m.d[n]
	if !ok {
		return 0, fail(zfs.KindNotFound, n, "dataset does not exist")
	}
	return d.guid, nil
}

// Exec runs one zfs argv.
func (m *Model) Exec(argv []string) error {
	if len(argv) == 0 {
		return fail(zfs.KindUnknown, "", "no command")
	}
	cmd, args := argv[0], argv[1:]
	flags := map[string]string{}
	props := map[string]pv{}
	var pos []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-o" && i+1 < len(args):
			k, v, _ := strings.Cut(args[i+1], "=")
			props[k] = pv{v, "LOCAL"}
			i++
		case a == "-V" && i+1 < len(args):
			flags["V"] = args[i+1]
			i++
		case a == "-s" || a == "-d" || a == "-r" || a == "-p":
			flags[a[1:]] = "1"
		default:
			pos = append(pos, a)
		}
	}
	switch cmd {
	case "create":
		if len(pos) != 1 {
			return fail(zfs.KindUnknown, "", "usage")
		}
		n := pos[0]
		if m.Has(n) {
			return fail(zfs.KindExists, n, "dataset already exists")
		}
		if p, ok := m.d[parentOf(n)]; !ok || p.typ != 'f' {
			return fail(zfs.KindParentNotFound, n, "no such parent")
		}
		if v, ok := flags["V"]; ok {
			size, _ := strconv.ParseUint(v, 10, 64)
			bs := uint64(16384)
			if b, ok := props["volblocksize"]; ok {
				bs, _ = strconv.ParseUint(b.v, 10, 64)
				delete(props, "volblocksize")
			}
			if size == 0 || size%bs != 0 {
				return fail(zfs.KindInvalidProperty, n, "volsize must be a multiple of volume block size")
			}
			props["volsize"] = pv{v, "LOCAL"}
			props["volblocksize"] = pv{u(bs), "NONE"}
			if flags["s"] == "" {
				props["refreservation"] = pv{v, "LOCAL"} // zvol_auto_resv (metadata ignored)
			}
			m.add(n, 'v', props)
			return nil
		}
		m.add(n, 'f', props)
		return nil
	case "snapshot":
		n := pos[0]
		h, _, _ := strings.Cut(n, "@")
		hd, ok := m.d[h]
		if !ok {
			return fail(zfs.KindNotFound, n, "dataset does not exist")
		}
		if m.Has(n) {
			return fail(zfs.KindExists, n, "dataset already exists")
		}
		s := m.add(n, 's', props)
		if hd.typ == 'v' {
			s.props["volsize"] = pv{hd.props["volsize"].v, "NONE"}
		}
		return nil
	case "clone":
		o, n := pos[0], pos[1]
		od, ok := m.d[o]
		if !ok || od.typ != 's' {
			return fail(zfs.KindNotFound, o, "dataset does not exist")
		}
		if m.Has(n) {
			return fail(zfs.KindExists, n, "dataset already exists")
		}
		if p, ok := m.d[parentOf(n)]; !ok || p.typ != 'f' {
			return fail(zfs.KindParentNotFound, n, "no such parent")
		}
		h, _, _ := strings.Cut(o, "@")
		hd := m.d[h]
		if hd.typ == 'v' {
			props["volsize"] = pv{od.props["volsize"].v, "LOCAL"}
			props["volblocksize"] = hd.props["volblocksize"]
		}
		c := m.add(n, hd.typ, props)
		c.origin = o
		return nil
	case "set":
		n := pos[len(pos)-1]
		d, ok := m.d[n]
		if !ok {
			return fail(zfs.KindNotFound, n, "dataset does not exist")
		}
		for _, kv := range pos[:len(pos)-1] {
			k, v, _ := strings.Cut(kv, "=")
			if k == "refreservation" && v == "auto" {
				v = d.props["volsize"].v
			}
			if k == "volsize" {
				if r, ok := d.props["refreservation"]; ok && r.v == d.props["volsize"].v {
					d.props["refreservation"] = pv{v, "LOCAL"} // auto follows volsize
				}
			}
			d.props[k] = pv{v, "LOCAL"}
		}
		return nil
	case "destroy":
		n := pos[0]
		d, ok := m.d[n]
		if !ok {
			return fail(zfs.KindNotFound, n, "dataset does not exist")
		}
		if flags["r"] != "" {
			return fail(zfs.KindUnknown, n, "the core never destroys recursively")
		}
		if d.typ == 's' {
			if len(m.clonesOf(n)) > 0 {
				if flags["d"] != "" {
					d.deferred = true
					return nil
				}
				return fail(zfs.KindHasClones, n, "snapshot has dependent clones")
			}
			delete(m.d, n)
			return nil
		}
		for k := range m.d {
			if strings.HasPrefix(k, n+"/") || strings.HasPrefix(k, n+"@") {
				return fail(zfs.KindHasChildren, n, "filesystem has children")
			}
		}
		delete(m.d, n)
		if od, ok := m.d[d.origin]; ok && od.deferred && len(m.clonesOf(d.origin)) == 0 {
			delete(m.d, d.origin) // the deferred origin goes with its last clone
		}
		return nil
	}
	return fail(zfs.KindUnknown, "", "unknown command %s", cmd)
}

func (m *Model) clonesOf(snap string) []string {
	if m.cidx != nil {
		return m.cidx[snap]
	}
	var c []string
	for k, d := range m.d {
		if d.origin == snap {
			c = append(c, k)
		}
	}
	slices.Sort(c)
	return c
}

func u(n uint64) string { return strconv.FormatUint(n, 10) }

// ---- zfs get -j -p ------------------------------------------------------------

var localDefaults = map[string]string{"compression": "on", "recordsize": "131072", "atime": "on", "relatime": "on",
	"sync": "standard", "logbias": "latency", "dedup": "off", "xattr": "sa", "acltype": "off", "snapdir": "hidden", "copies": "1"}

var volLocal = map[string]bool{"compression": true, "sync": true, "logbias": true, "dedup": true, "copies": true}

// Docs prints the documents a scan of scope would read (Schema.Argv): one
// for All, one per existing scoped volume.
func (m *Model) Docs(sc *world.Schema, s world.Scope) [][]byte {
	props := sc.Props(s)
	parent := sc.Parent().String()
	if s.IsAll() {
		return [][]byte{m.doc(props, parent, 2)}
	}
	var out [][]byte
	for _, id := range s.VolIDs() {
		root := parent + "/" + id.String()
		if m.Has(root) {
			out = append(out, m.doc(props, root, 1))
		}
	}
	return out
}

func depth(root, n string) int {
	if n == root {
		return 0
	}
	rest := strings.TrimPrefix(n, root+"/")
	if strings.HasPrefix(n, root+"@") {
		return 1
	}
	d := strings.Count(rest, "/") + 1
	if strings.Contains(rest, "@") {
		d++
	}
	return d
}

func (m *Model) index() {
	m.cidx = map[string][]string{}
	for k, d := range m.d {
		if d.origin != "" {
			m.cidx[d.origin] = append(m.cidx[d.origin], k)
		}
	}
	for _, c := range m.cidx {
		slices.Sort(c)
	}
}

func (m *Model) names(root string, maxDepth int) []string {
	var names []string
	for n := range m.d {
		if (n == root || strings.HasPrefix(n, root+"/") || strings.HasPrefix(n, root+"@")) && depth(root, n) <= maxDepth {
			names = append(names, n)
		}
	}
	// zfs_compare: by dataset name, a dataset before its snapshots, snapshots by createtxg
	sortZFS(m, names)
	return names
}

func sortZFS(m *Model, names []string) {
	slices.SortFunc(names, func(a, b string) int {
		ah, _, as := strings.Cut(a, "@")
		bh, _, bs := strings.Cut(b, "@")
		if c := strings.Compare(ah, bh); c != 0 {
			return c
		}
		if as != bs {
			if as {
				return 1
			}
			return -1
		}
		return int(m.d[a].txg) - int(m.d[b].txg)
	})
}

func (m *Model) doc(props []string, root string, maxDepth int) []byte {
	m.index()
	defer func() { m.cidx = nil }()
	names := m.names(root, maxDepth)
	var b bytes.Buffer
	b.WriteString(`{"output_version":{"command":"zfs get","vers_major":0,"vers_minor":1},"datasets":{`)
	for i, n := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		m.writeDataset(&b, props, n)
	}
	b.WriteString("}}\n")
	return b.Bytes()
}

func (m *Model) writeDataset(b *bytes.Buffer, props []string, n string) {
	d := m.d[n]
	typ := map[byte]string{'f': "FILESYSTEM", 'v': "VOLUME", 's': "SNAPSHOT"}[d.typ]
	pool, _, _ := strings.Cut(n, "/")
	pool, _, _ = strings.Cut(pool, "@")
	fmt.Fprintf(b, `%q:{"name":%q,"type":%q,"pool":%q,"createtxg":"%d",`, n, n, typ, pool, d.txg)
	if d.typ == 's' {
		h, c, _ := strings.Cut(n, "@")
		fmt.Fprintf(b, `"dataset":%q,"snapshot_name":%q,`, h, c)
	}
	b.WriteString(`"properties":{`)
	for i, p := range props {
		if i > 0 {
			b.WriteByte(',')
		}
		v, src, data := m.value(n, d, p)
		fmt.Fprintf(b, `%q:{"value":%q,"source":{"type":%q,"data":%q}}`, p, v, src, data)
	}
	b.WriteString("}}")
}

// value is what zfs get prints for one property (wave5/zfs-invariants.md §2).
func (m *Model) value(n string, d *ds, p string) (v, src, data string) {
	dash := func() (string, string, string) { return "-", "NONE", "-" }
	head := d
	if d.typ == 's' {
		h, _, _ := strings.Cut(n, "@")
		head = m.d[h]
	}
	switch p {
	case "type":
		return map[byte]string{'f': "filesystem", 'v': "volume", 's': "snapshot"}[d.typ], "NONE", "-"
	case "mountpoint":
		if d.typ != 'f' {
			return dash()
		}
		return "/" + n, "DEFAULT", "-"
	case "mounted":
		if d.typ != 'f' {
			return dash()
		}
		return "yes", "NONE", "-"
	case "sharenfs":
		if d.typ != 'f' {
			return dash()
		}
		if x, ok := d.props[p]; ok {
			return x.v, x.src, "-"
		}
		return "off", "DEFAULT", "-"
	case "creation":
		return strconv.FormatInt(d.creation, 10), "NONE", "-"
	case "guid":
		return u(d.guid), "NONE", "-"
	case "origin":
		if d.typ == 's' || d.origin == "" {
			return dash()
		}
		return d.origin, "NONE", "-"
	case "clones":
		if d.typ != 's' {
			return dash()
		}
		if c := m.clonesOf(n); len(c) > 0 {
			return strings.Join(c, ","), "NONE", "-"
		}
		return dash()
	case "volsize":
		if d.typ == 'f' || head.typ == 'f' {
			return dash()
		}
		x := d.props["volsize"]
		return x.v, map[bool]string{true: "NONE", false: "LOCAL"}[d.typ == 's'], "-"
	case "volblocksize":
		if d.typ != 'v' {
			return dash()
		}
		x := d.props["volblocksize"].v
		if x == "16384" {
			return x, "DEFAULT", "-"
		}
		return x, "NONE", "-"
	case "refquota", "refreservation":
		if d.typ == 's' || (p == "refquota" && d.typ == 'v') {
			return dash()
		}
		if x, ok := d.props[p]; ok && x.v != "0" {
			return x.v, x.src, "-"
		}
		return "0", "DEFAULT", "-"
	case "defer_destroy":
		if d.typ != 's' {
			return dash()
		}
		return map[bool]string{true: "on", false: "off"}[d.deferred], "NONE", "-"
	}
	if strings.Contains(p, ":") { // user property: local, received, or inherited
		if x, ok := d.props[p]; ok {
			return x.v, x.src, "-"
		}
		anc := n
		if d.typ == 's' {
			anc, _, _ = strings.Cut(n, "@")
		} else {
			anc = parentOf(n)
		}
		for ; anc != ""; anc = parentOf(anc) {
			if a, ok := m.d[anc]; ok {
				if x, ok := a.props[p]; ok && x.src == "LOCAL" {
					return x.v, "INHERITED", anc
				}
			}
		}
		return dash()
	}
	// allowlisted native
	if d.typ == 's' || (d.typ == 'v' && !volLocal[p]) {
		return dash()
	}
	if x, ok := d.props[p]; ok {
		return x.v, x.src, "-"
	}
	return localDefaults[p], "DEFAULT", "-"
}

// Dump is a canonical projection of the model for convergence checks:
// everything except creation time and guid, which differ by path.
func (m *Model) Dump() string {
	var lines []string
	for n, d := range m.d {
		keys := slices.Sorted(maps.Keys(d.props))
		var kv []string
		for _, k := range keys {
			kv = append(kv, k+"="+d.props[k].v+"/"+d.props[k].src)
		}
		lines = append(lines, fmt.Sprintf("%s %c origin=%s deferred=%v %s", n, d.typ, d.origin, d.deferred, strings.Join(kv, ",")))
	}
	slices.Sort(lines)
	return strings.Join(lines, "\n")
}

// Text prints `zfs get -Hp -o name,property,value,source -t filesystem,volume,snapshot
// -d <depth> <props> <root>` for the same datasets: HEAD's scan input.
func (m *Model) Text(props []string, root string, maxDepth int) []byte {
	m.index()
	defer func() { m.cidx = nil }()
	var b bytes.Buffer
	for _, n := range m.names(root, maxDepth) {
		d := m.d[n]
		for _, p := range props {
			v, src, data := m.value(n, d, p)
			switch src {
			case "NONE":
				src = "-"
			case "INHERITED":
				src = "inherited from " + data
			default:
				src = strings.ToLower(src)
			}
			b.WriteString(n)
			b.WriteByte('\t')
			b.WriteString(p)
			b.WriteByte('\t')
			b.WriteString(v)
			b.WriteByte('\t')
			b.WriteString(src)
			b.WriteByte('\n')
		}
	}
	return b.Bytes()
}

// Synthetic is a parent with nVols managed, committed volumes (half
// filesystems with a 1 GiB quota and local compression, half thick 1 GiB
// zvols), every 20th a clone of its predecessor's first snapshot, each with
// snaps CSI snapshots. 1000x9 is the "10k" fixture, 10000x10 the "110k"
// (dod3/existing.md's shapes).
func Synthetic(parent, prefix string, nVols, snaps int) *Model {
	m := New("tank", parent)
	m.d[parent].props[prefix+":unrelated"] = pv{"x", "LOCAL"}
	for i := range nVols {
		id := fmt.Sprintf("pvc-%08x-1b9f-4c1e-9d2e-%012x", i, i*7919)
		n := parent + "/" + id
		p := map[string]pv{
			prefix + ":managed_resource":  {"true", "LOCAL"},
			prefix + ":provision_success": {"true", "LOCAL"},
			prefix + ":csi_volume_name":   {id, "LOCAL"},
		}
		var d *ds
		if i%2 == 0 {
			p["refquota"] = pv{"1073741824", "LOCAL"}
			p["compression"] = pv{"lz4", "LOCAL"}
			p["sharenfs"] = pv{"off", "LOCAL"}
			d = m.add(n, 'f', p)
		} else {
			p["volsize"] = pv{"1073741824", "LOCAL"}
			p["volblocksize"] = pv{"16384", "NONE"}
			p["refreservation"] = pv{"1073741824", "LOCAL"}
			d = m.add(n, 'v', p)
		}
		if i%20 == 19 && snaps > 0 {
			prev := fmt.Sprintf("%s/pvc-%08x-1b9f-4c1e-9d2e-%012x", parent, i-2, (i-2)*7919)
			d.origin = prev + "@snap-0"
			p[prefix+":csi_volume_content_source_type"] = pv{"snapshot", "LOCAL"}
			p[prefix+":csi_volume_content_source_id"] = pv{strings.TrimPrefix(prev, parent+"/") + "@snap-0", "LOCAL"}
		}
		for j := range snaps {
			sp := map[string]pv{
				prefix + ":managed_resource":              {"true", "LOCAL"},
				prefix + ":csi_snapshot_name":             {fmt.Sprintf("snapshot-%08x-%d", i, j), "LOCAL"},
				prefix + ":csi_snapshot_source_volume_id": {id, "LOCAL"},
			}
			s := m.add(n+"@snap-"+strconv.Itoa(j), 's', sp)
			if d.typ == 'v' {
				s.props["volsize"] = pv{"1073741824", "NONE"}
			}
		}
	}
	return m
}
