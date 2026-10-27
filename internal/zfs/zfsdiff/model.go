package zfsdiff

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/wommy/empty/internal/zfs"
)

// Model is a reference state machine of one OpenZFS 2.4.4 pool as seen
// through the zfs(8) CLI. It is pure (no I/O, no clock) and safe for
// concurrent use. Run implements zfs.Runner.
//
// Modelled: filesystems, volumes, snapshots; native properties in
// propDefs and user properties, with local/received/inherited/default
// sources and explicit-inherit markers; origin/clones; user holds;
// defer_destroy; volsize/volblocksize; quota, refquota, reservation and
// refreservation (including refreservation=auto and the synthetic
// reservation that tracks volsize); mount state. Not modelled: space
// accounting beyond the reservation/quota cross checks (referenced is 0),
// bookmarks, encryption, zoned, snapshot ranges (fs@a%b), -n/-v output.
type Model struct {
	mu   sync.Mutex
	ds   map[string]*dset
	txg  uint64
	pool string
	// MaxBlockSize is the pool's maxblocksize (16M with large_blocks,
	// enabled by default on a new pool).
	MaxBlockSize uint64
	// PoolFree is the space available for reservations.
	PoolFree uint64
}

type dset struct {
	name     string
	kind     kind
	txg      uint64
	local    map[string]string // the dataset's own zap: name -> stored value
	recvd    map[string]string // name$recvd
	inh      map[string]bool   // name$inherit (explicit inherit over a received value)
	hasRecvd bool              // $hasrecvd: set by zfs recv
	origin   *dset             // heads: the origin snapshot of a clone
	volsize  uint64            // volume heads, and snapshots of volumes (at snapshot time)
	vbs      uint64            // volblocksize (volumes and their snapshots)
	holds    map[string]bool
	deferred bool
	mounted  bool
}

// NewModel returns a model with one pool. rootProps are set locally on
// the pool's root dataset, as `zpool create -O k=v` does.
func NewModel(pool string, rootProps map[string]string) *Model {
	m := &Model{ds: map[string]*dset{}, pool: pool, MaxBlockSize: 16 << 20, PoolFree: 9 << 30}
	root := m.newDS(pool, kFS)
	for k, v := range rootProps {
		root.local[k] = v
	}
	root.mounted = true
	return m
}

func (m *Model) newDS(name string, k kind) *dset {
	m.txg++
	d := &dset{name: name, kind: k, txg: m.txg, local: map[string]string{}, recvd: map[string]string{}, inh: map[string]bool{}, holds: map[string]bool{}}
	m.ds[name] = d
	return d
}

type exitErr struct {
	code int
	msg  string
}

func fail(format string, a ...any) *exitErr {
	return &exitErr{code: 1, msg: fmt.Sprintf(format, a...)}
}

// usage: usage(B_FALSE) exits 2 (cmd/zfs/zfs_main.c:644).
func usage(format string, a ...any) *exitErr {
	return &exitErr{code: 2, msg: fmt.Sprintf(format, a...)}
}

// Run implements zfs.Runner.
func (m *Model) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []byte
	var e *exitErr
	if len(args) == 0 {
		e = usage("missing command")
	} else {
		switch args[0] {
		case "create":
			e = m.create(args[1:])
		case "snapshot":
			e = m.snapshot(args[1:])
		case "clone":
			e = m.clone(args[1:])
		case "destroy":
			e = m.destroy(args[1:])
		case "set":
			e = m.set(args[1:])
		case "inherit":
			e = m.inherit(args[1:])
		case "hold":
			e = m.holdRelease(args[1:], true)
		case "release":
			e = m.holdRelease(args[1:], false)
		case "rename":
			e = m.rename(args[1:])
		case "promote":
			e = m.promote(args[1:])
		case "mount":
			e = m.mount(args[1:])
		case "get":
			out, e = m.get(args[1:])
		default:
			e = usage("unrecognized command '%s'", args[0])
		}
	}
	if e != nil {
		return out, &zfs.ExitError{Args: append([]string{name}, args...), Code: e.code, Stderr: e.msg + "\n"}
	}
	return out, nil
}

// ---- option parsing (getopt) ------------------------------------------------

type opts struct {
	flags map[byte]bool
	vals  map[byte][]string
	rest  []string
}

// getopt parses argv like getopt(3) with optstring spec ("o:" takes an
// argument). An unknown flag is a usage error.
func getopt(args []string, spec string) (opts, *exitErr) {
	o := opts{flags: map[byte]bool{}, vals: map[byte][]string{}}
	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			i++
			break
		}
		if len(a) < 2 || a[0] != '-' {
			break
		}
		for j := 1; j < len(a); j++ {
			c := a[j]
			p := strings.IndexByte(spec, c)
			if p < 0 {
				return o, usage("invalid option '%c'", c)
			}
			if p+1 < len(spec) && spec[p+1] == ':' {
				v := a[j+1:]
				if v == "" {
					if i+1 >= len(args) {
						return o, usage("missing argument for '%c'", c)
					}
					i++
					v = args[i]
				}
				o.vals[c] = append(o.vals[c], v)
				break
			}
			o.flags[c] = true
		}
	}
	o.rest = args[i:]
	return o, nil
}

// parseprop splits "k=v" into an ordered list (cmd/zfs/zfs_main.c:652-671).
type kv struct{ k, v string }

func parseprops(list []string) ([]kv, *exitErr) {
	var out []kv
	seen := map[string]bool{}
	for _, s := range list {
		k, v, ok := strings.Cut(s, "=")
		if !ok {
			return nil, fail("missing '=' for property=value argument")
		}
		if seen[k] {
			return nil, fail("property '%s' specified multiple times", k) // zfs_main.c:663-666
		}
		seen[k] = true
		out = append(out, kv{k, v})
	}
	return out, nil
}

// ---- names ------------------------------------------------------------------

// validName is the subset of zfs_validate_name (lib/libzfs/libzfs_dataset.c:
// 109-221) the generator can reach: delimiter rules and the character set
// of entity_namecheck (module/zcommon/zfs_namecheck.c:49-56, 173-280).
func validName(path string, types kind) (string, bool) {
	at := strings.IndexByte(path, '@')
	if types&kSnap == 0 && at >= 0 {
		return "snapshot delimiter '@' is not expected here", false // :113-118
	}
	if types == kSnap && at < 0 {
		return "missing '@' delimiter in snapshot name", false // :120-125
	}
	if strings.IndexByte(path, '#') >= 0 {
		return "bookmark delimiter '#' is not expected here", false // :127-132
	}
	if len(path) >= 256 {
		return "name is too long", false
	}
	if strings.HasPrefix(path, "/") {
		return "leading slash in name", false
	}
	if strings.HasSuffix(path, "/") {
		return "trailing slash in name", false
	}
	if strings.Count(path, "@") > 1 {
		return "multiple '@' and/or '#' delimiters in name", false
	}
	for _, c := range []byte(path) {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("-_.: /@", c) >= 0) {
			return fmt.Sprintf("invalid character '%c' in name", c), false
		}
	}
	ds, snap, _ := strings.Cut(path, "@")
	for _, comp := range strings.Split(ds, "/") {
		if comp == "" {
			return "empty component or misplaced '@' or '#' delimiter in name", false
		}
	}
	if at >= 0 && (snap == "" || strings.Contains(snap, "/")) {
		return "empty component or misplaced '@' or '#' delimiter in name", false
	}
	return "", true
}

func parentName(name string) string {
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		return name[:i]
	}
	return ""
}

func poolOf(name string) string {
	return name[:strings.IndexAny(name+"/@", "/@")]
}

// open is zfs_open (lib/libzfs/libzfs_dataset.c:707-791): name check, then
// existence (ENOENT -> "dataset does not exist", libzfs_util.c:454-458),
// then the type mask (EZFS_BADTYPE, :784-789).
func (m *Model) open(name string, types kind) (*dset, *exitErr) {
	if why, ok := validName(name, types); !ok {
		return nil, fail("cannot open '%s': %s", name, why)
	}
	d := m.ds[name]
	if d == nil {
		return nil, fail("cannot open '%s': dataset does not exist", name)
	}
	if d.kind&types == 0 {
		return nil, fail("cannot open '%s': operation not applicable to datasets of this type", name)
	}
	return d, nil
}

// checkParents is check_parents (lib/libzfs/libzfs_dataset.c:3464-3542).
// With acceptAncestor it returns the closest existing ancestor.
func (m *Model) checkParents(path string, acceptAncestor bool) (string, *exitErr) {
	parent := parentName(path)
	if parent == "" {
		return "", fail("cannot create '%s': missing dataset name", path) // :3478-3481
	}
	pool := poolOf(path)
	if m.ds[pool] == nil {
		return "", fail("cannot create '%s': no such pool '%s'", path, pool) // :3488-3493
	}
	for m.ds[parent] == nil {
		if !acceptAncestor {
			return "", fail("cannot create '%s': parent does not exist", path) // :3506-3509
		}
		parent = parentName(parent)
		if parent == "" {
			return "", fail("cannot create '%s': no such pool '%s'", path, pool)
		}
	}
	if m.ds[parent].kind != kFS {
		return "", fail("cannot create '%s': parent is not a filesystem", path) // :3525-3532
	}
	return parent, nil
}

// ---- tree helpers -------------------------------------------------------------

func (m *Model) headOf(snap *dset) *dset {
	h, _, _ := strings.Cut(snap.name, "@")
	return m.ds[h]
}

func (m *Model) snapsOf(head string) []*dset {
	var out []*dset
	for k, d := range m.ds {
		if strings.HasPrefix(k, head+"@") {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].txg < out[j].txg })
	return out
}

// childrenOf: direct child filesystems and volumes, by name.
func (m *Model) childrenOf(name string) []*dset {
	var out []*dset
	for k, d := range m.ds {
		if d.kind != kSnap && parentName(k) == name {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func (m *Model) clonesOf(snap *dset) []*dset {
	var out []*dset
	for _, d := range m.ds {
		if d.origin == snap {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func under(name, root string) bool {
	return name == root || strings.HasPrefix(name, root+"/") || strings.HasPrefix(name, root+"@")
}

// dependents is zfs_iter_dependents (lib/libzfs/libzfs_iter.c:528-584):
// post-order; for a filesystem its child filesystems, then its snapshots
// by txg, each snapshot after its clones' subtrees. The target itself is
// not included. Cycles (a clone inside the tree) are visited once.
func (m *Model) dependents(d *dset) []*dset {
	var out []*dset
	seen := map[*dset]bool{d: true}
	var walk func(x *dset)
	walk = func(x *dset) {
		if x.kind == kSnap {
			for _, c := range m.clonesOf(x) {
				if !seen[c] {
					seen[c] = true
					walk(c)
					out = append(out, c)
				}
			}
			return
		}
		for _, c := range m.childrenOf(x.name) {
			if !seen[c] {
				seen[c] = true
				walk(c)
				out = append(out, c)
			}
		}
		for _, s := range m.snapsOf(x.name) {
			if !seen[s] {
				seen[s] = true
				walk(s)
				out = append(out, s)
			}
		}
	}
	walk(d)
	return out
}

// ---- property resolution -----------------------------------------------------

// lookup walks the dsl_prop_get_all_ds order (module/zfs/dsl_prop.c:
// 1208-1252, :1065-1170) for one property: the snapshot's own zap
// (setpoint = snapshot), then the dataset's dir, then its ancestors with
// DSL_PROP_GET_INHERITING (non-inheritable props skipped). Received values
// are skipped where a local value or an explicit-inherit marker exists.
// It returns the stored value and its setpoint ("" for none, "$recvd" for
// received on the dataset itself).
func (m *Model) lookup(d *dset, prop string, inheritable bool) (string, string, bool) {
	if d.kind == kSnap {
		if v, ok := d.local[prop]; ok {
			return v, d.name, true
		}
		if v, ok := d.recvd[prop]; ok && !d.inh[prop] {
			return v, d.name + "$recvd", true
		}
		if !inheritable {
			return "", "", false
		}
		d = m.headOf(d)
	} else {
		if v, ok := d.local[prop]; ok {
			return v, d.name, true
		}
		if v, ok := d.recvd[prop]; ok && !d.inh[prop] {
			return v, "$recvd", true
		}
		if !inheritable {
			return "", "", false
		}
		d = m.ds[parentName(d.name)]
	}
	for ; d != nil; d = m.ds[parentName(d.name)] {
		if v, ok := d.local[prop]; ok {
			return v, d.name, true
		}
		// a received value on an ancestor is inherited with the
		// ancestor as setpoint (dsl_prop.c:1123-1124)
		if v, ok := d.recvd[prop]; ok && !d.inh[prop] {
			return v, d.name, true
		}
	}
	return "", "", false
}

// sourceText is get_source (lib/libzfs/libzfs_dataset.c:2357-2381) printed
// by zprop_print_one_property (libzfs_util.c:1583-1606).
func sourceText(d *dset, setpoint string, found bool) string {
	switch {
	case !found:
		return "default"
	case setpoint == "$recvd" || strings.HasSuffix(setpoint, "$recvd"):
		return "received"
	case setpoint == d.name:
		return "local"
	}
	return "inherited from " + setpoint
}

// value is what `zfs get -Hp` prints for one (dataset, property).
func (m *Model) value(d *dset, prop string) (string, string) {
	if isUserProp(prop) {
		v, sp, ok := m.lookup(d, prop, true)
		if !ok {
			return "-", "-" // zfs_main.c:2145
		}
		return v, sourceText(d, sp, true)
	}
	def, known := propDefs[prop]
	if !known {
		return "-", "-"
	}
	// zfs_prop_get: type gate (lib/libzfs/libzfs_dataset.c:2648-2649)
	if def.types&d.kind == 0 {
		return "-", "-"
	}
	// get_numeric_property: a snapshot's numeric/index property must be
	// valid for its head type, unless snapshot-only (:2131-2138,
	// zprop_valid_for_type headcheck, zprop_common.c:410-412)
	if d.kind == kSnap && def.typ != tString && def.types != kSnap {
		if def.types&m.headOf(d).kind == 0 {
			return "-", "-"
		}
	}
	switch prop {
	case "name":
		return d.name, "-"
	case "type":
		return d.kind.String(), "-"
	case "origin":
		if d.origin == nil {
			return "-", "-" // :2747-2755 default string "" -> "-"
		}
		return d.origin.name, "-"
	case "mounted":
		if d.mounted {
			return "yes", "-" // :2759-2770 ("yes"/"no")
		}
		return "no", "-"
	case "defer_destroy":
		if d.deferred {
			return "on", "-"
		}
		return "off", "-"
	case "userrefs":
		return strconv.Itoa(len(d.holds)), "-"
	case "used", "available", "creation":
		return "0", "-"
	case "volsize":
		// zvol_get_stats adds volsize with no source; get_numeric_property
		// then reports the dataset itself as the source (:2246-2249), so
		// volumes and their snapshots print "local"
		return strconv.FormatUint(d.volsize, 10), "local"
	case "volblocksize":
		// dsl_prop_nvlist_add_uint64 (module/zfs/dsl_prop.c:1328-1348):
		// source "" (default) only when equal to the default; else none
		if d.vbs == 16384 {
			return "16384", "default"
		}
		return strconv.FormatUint(d.vbs, 10), "-"
	}
	v, sp, ok := m.lookup(d, prop, def.attr == pInherit)
	if prop == "mountpoint" {
		return m.mountpoint(d, v, sp, ok)
	}
	if !ok {
		return def.def, "default"
	}
	return v, sourceText(d, sp, true)
}

// mountpoint renders the inherited path like zfs_prop_get
// (lib/libzfs/libzfs_dataset.c:2676-2744).
func (m *Model) mountpoint(d *dset, v, sp string, ok bool) (string, string) {
	src := "default"
	if !ok {
		v, sp = "/", ""
	} else {
		src = sourceText(d, sp, true)
	}
	if !strings.HasPrefix(v, "/") {
		return v, src // none, legacy
	}
	rel := ""
	if !strings.HasSuffix(sp, "$recvd") {
		rel = strings.TrimPrefix(d.name[len(sp):], "/")
		if sp == "" {
			rel = d.name
		}
	}
	if v == "/" && rel != "" {
		v = ""
	}
	if rel == "" {
		return v, src
	}
	sep := "/"
	if rel[0] == '@' {
		sep = ""
	}
	return v + sep + rel, src
}

func (m *Model) effective(d *dset, prop string) string {
	v, _ := m.value(d, prop)
	return v
}

func (m *Model) mountable(d *dset) bool {
	// zfs_is_mountable (lib/libzfs/libzfs_mount.c:282-312)
	if d.kind != kFS {
		return false
	}
	mp := m.effective(d, "mountpoint")
	return mp != "none" && mp != "legacy" && m.effective(d, "canmount") != "off"
}

func u64(n uint64) string { return strconv.FormatUint(n, 10) }

func num(s string) uint64 {
	n, _ := strconv.ParseUint(s, 10, 64)
	return n
}
