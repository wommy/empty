package zfsdiff

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// unmodelled are real OpenZFS 2.4 dataset properties the model does not
// carry. Using one makes the model refuse with exit 3 ("unmodelled"), so a
// comparison flags the step instead of trusting a guess.
var unmodelled = []string{
	"checksum", "exec", "devices", "setuid", "zoned", "vscan", "nbmand", "overlay",
	"aclmode", "aclinherit", "primarycache", "secondarycache", "prefetch", "dnodesize",
	"volmode", "direct", "snapdev", "redundant_metadata", "special_small_blocks",
	"filesystem_limit", "snapshot_limit", "mlslabel", "context", "fscontext",
	"defcontext", "rootcontext", "keylocation", "keyformat", "encryption",
	"pbkdf2iters", "version", "normalization", "utf8only", "casesensitivity",
	"volthreading", "longname", "logicalused", "referenced", "written",
}

type pval struct {
	k, v   string // canonical stored value
	auto   bool   // refreservation=auto sentinel (UINT64_MAX)
	isNone bool
}

// validProps is zfs_valid_proplist (lib/libzfs/libzfs_dataset.c:1031-1600)
// with zprop_parse_value (lib/libzfs/libzfs_util.c:1759-1937). zhp is nil
// for create and non-nil for set and clone (clone passes the origin).
func (m *Model) validProps(list []kv, typ kind, zhp *dset, errbuf string) ([]pval, *exitErr) {
	var out []pval
	for _, p := range list {
		if isUserProp(p.k) {
			if len(p.k) >= 256 { // :1072-1078
				return nil, fail("%s: property name '%s' is too long", errbuf, p.k)
			}
			if len(p.v) >= 8192 { // kernel E2BIG, zfs_ioctl.c zfs_check_userprops
				return nil, fail("%s: property value too long", errbuf)
			}
			out = append(out, pval{k: p.k, v: p.v})
			continue
		}
		// only user properties on snapshots (:1087-1096), checked before
		// the name is even resolved
		if typ == kSnap {
			return nil, fail("%s: this property can not be modified for snapshots", errbuf)
		}
		def, known := propDefs[p.k]
		if !known {
			if slices.Contains(unmodelled, p.k) {
				return nil, &exitErr{code: 3, msg: "zfsdiff model: unmodelled property " + p.k}
			}
			return nil, fail("%s: invalid property '%s'", errbuf, p.k) // :1183-1188
		}
		if def.types&typ == 0 { // :1190-1196
			return nil, fail("%s: '%s' does not apply to datasets of this type", errbuf, p.k)
		}
		// readonly, except a set-once property at create (zhp == NULL)
		// (:1198-1206; zfs_prop_readonly zfs_prop.c:931-938)
		if def.attr == pReadonly || def.attr == pOnetime && zhp != nil {
			return nil, fail("%s: '%s' is readonly", errbuf, p.k)
		}
		pv := pval{k: p.k}
		switch def.typ {
		case tNumber:
			switch p.v {
			case "none":
				pv.isNone, pv.v = true, "0"
			case "auto":
				// libzfs_util.c:1870-1895
				if p.k != "refreservation" {
					return nil, fail("%s: 'auto' is invalid value for '%s'", errbuf, p.k)
				}
				if typ&kVol == 0 {
					return nil, fail("%s: '%s=auto' only allowed on volumes", errbuf, p.k)
				}
				pv.auto = true
			default:
				n, why, ok := niceStrToNum(p.v)
				if !ok {
					return nil, fail("%s: %s", errbuf, why)
				}
				pv.v = u64(n)
			}
			// libzfs_util.c:1826-1834: quota/refquota=0 must be spelled none
			if !pv.isNone && pv.v == "0" && (p.k == "quota" || p.k == "refquota") {
				return nil, fail("%s: use 'none' to disable quota/refquota", errbuf)
			}
		case tIndex:
			c, ok := canonIndex(def, p.v)
			if !ok { // libzfs_util.c:1903-1913
				names := make([]string, 0, len(def.table))
				for _, e := range def.table {
					names = append(names, e.name)
				}
				return nil, fail("%s: '%s' must be one of '%s'", errbuf, p.k, strings.Join(names, " | "))
			}
			pv.v = c
		case tString:
			if len(p.v) >= 4096 {
				return nil, fail("%s: '%s' is too long", errbuf, p.k)
			}
			pv.v = p.v
		}
		switch p.k {
		case "volblocksize", "recordsize": // :1232-1255
			n := num(pv.v)
			if n < 512 || n > m.MaxBlockSize || n&(n-1) != 0 {
				return nil, fail("%s: '%s' must be power of 2 from 512B to %s", errbuf, p.k, niceBytes(m.MaxBlockSize))
			}
		case "mountpoint": // :1338-1368
			if pv.v != "none" && pv.v != "legacy" && !strings.HasPrefix(pv.v, "/") {
				return nil, fail("%s: '%s' must be an absolute path, 'none', or 'legacy'", errbuf, p.k)
			}
		}
		// existing volumes (:1494-1530): volsize must be a nonzero
		// multiple of the volume's block size
		if typ == kVol && zhp != nil && p.k == "volsize" {
			n := num(pv.v)
			if n%zhp.vbs != 0 {
				return nil, fail("%s: 'volsize' must be a multiple of volume block size (%s)", errbuf, niceBytes(zhp.vbs))
			}
			if n == 0 {
				return nil, fail("%s: 'volsize' cannot be zero", errbuf)
			}
		}
		// NV_UNIQUE_NAME: a later add replaces an earlier one
		out = slices.DeleteFunc(out, func(x pval) bool { return x.k == pv.k })
		out = append(out, pv)
	}
	return out, nil
}

// ---- kernel property set --------------------------------------------------------

const enospc = "ENOSPC"

// dirUsed is dd_used_bytes with referenced and unique 0 (module/zfs/
// dsl_dir.c dsl_dir_diduse_transfer_space): d's refreservation plus, per
// child, the larger of its used and its reservation.
func (m *Model) dirUsed(d *dset) uint64 {
	n := num(m.effective(d, "refreservation"))
	for _, c := range m.childrenOf(d.name) {
		n += max(m.dirUsed(c), num(m.effective(c, "reservation")))
	}
	return n
}

// dirAvail is dsl_dir_space_available(dd, NULL, 0, TRUE) (dsl_dir.c:
// 1198-1250): the parent's space plus d's unused reservation, capped by
// d's quota; at the pool root the quota is PoolFree.
func (m *Model) dirAvail(d *dset) uint64 {
	parentspace := uint64(math.MaxUint64)
	p := m.ds[parentName(d.name)]
	if p != nil {
		parentspace = m.dirAvail(p)
	}
	quota := uint64(math.MaxUint64)
	if q := num(m.effective(d, "quota")); q > 0 {
		quota = q
	}
	if p == nil {
		quota = min(quota, m.PoolFree)
	}
	used := m.dirUsed(d)
	if r := num(m.effective(d, "reservation")); r > used && parentspace != math.MaxUint64 {
		parentspace += r - used
	}
	if used > quota {
		return 0
	}
	return min(parentspace, quota-used)
}

// setSpecial is zfs_prop_set_special (module/zfs/zfs_ioctl.c:2625-2720)
// for the space properties, with their sync-task checks. It returns
// "" or an errno name.
func (m *Model) setSpecial(d *dset, p pval) (string, bool) {
	newval := num(p.v)
	switch p.k {
	case "refquota":
		// dsl_dataset_set_refquota_check (module/zfs/dsl_dataset.c:4455-4497)
		if newval != 0 && newval < num(m.effective(d, "refreservation")) {
			return enospc, true
		}
	case "refreservation":
		// dsl_dataset_set_refreservation_check (dsl_dataset.c:4538-4596):
		// only growth is checked, against available and refquota
		cur := num(m.effective(d, "refreservation"))
		if p.auto { // UINT64_MAX reaches the kernel only from create
			return enospc, true
		}
		if newval > cur {
			rq := uint64(0)
			if d.kind == kFS {
				rq = num(m.effective(d, "refquota"))
			}
			if newval-cur > m.dirAvail(d) || rq > 0 && newval > rq {
				return enospc, true
			}
		}
	case "quota":
		// dsl_dir_set_quota_check (module/zfs/dsl_dir.c:1720-1758)
		if newval != 0 && (newval < num(m.effective(d, "reservation")) || newval < m.dirUsed(d)) {
			return enospc, true
		}
	case "reservation":
		// dsl_dir_set_reservation_check (dsl_dir.c:1759-1810): delta =
		// MAX(used, new) - MAX(used, old) against the parent's space
		// (PoolFree less used at the root)
		used := m.dirUsed(d)
		cur := max(used, num(m.effective(d, "reservation")))
		if nv := max(used, newval); nv > cur {
			avail := uint64(0)
			if p := m.ds[parentName(d.name)]; p != nil {
				avail = m.dirAvail(p)
			} else if used < m.PoolFree {
				avail = m.PoolFree - used
			}
			q := uint64(0)
			if d.kind == kFS {
				q = num(m.effective(d, "quota"))
			}
			if nv-cur > avail || q > 0 && newval > q {
				return enospc, true
			}
		}
	case "volsize":
		d.volsize = newval // zvol_set_volsize
		return "", true
	default:
		return "", false
	}
	d.local[p.k] = p.v
	delete(d.inh, p.k)
	return "", true
}

// applyProps is zfs_set_prop_nvlist (module/zfs/zfs_ioctl.c:2840-2990):
// special properties are set one at a time in order, a failure is
// recorded and the rest continue; the others are set together at the
// end. It returns the failed properties.
func (m *Model) applyProps(d *dset, list []pval) []string {
	var failed []string
	for _, p := range list {
		if errno, special := m.setSpecial(d, p); special {
			if errno != "" {
				failed = append(failed, p.k)
			}
			continue
		}
		d.local[p.k] = p.v
		delete(d.inh, p.k) // ZPROP_SRC_LOCAL removes $inherit (dsl_prop.c:771-784)
	}
	return failed
}

// setpropError is zfs_setprop_error for ENOSPC (lib/libzfs/libzfs_util.c:
// 534-556).
func setpropError(errbuf, prop string) string {
	switch prop {
	case "quota", "refquota":
		return errbuf + ": size is less than current used or reserved space"
	case "reservation", "refreservation":
		return errbuf + ": size is greater than available space"
	}
	return errbuf + ": out of space"
}

// ---- mount side effects -------------------------------------------------------

// changelist is the node set changelist_gather (lib/libzfs/
// libzfs_changelist.c:654-830, change_one :508-600) collects for prop at
// d: d itself, then filesystems below it whose prop (or, for a share
// property, its companion) is default or inherited, descending only
// through added nodes.
func (m *Model) changelist(d *dset, prop string) []*dset {
	companion := map[string]string{"sharenfs": "sharesmb", "sharesmb": "sharenfs"}[prop]
	inherits := func(x *dset, p string) bool {
		_, src := m.value(x, p)
		return src == "default" || strings.HasPrefix(src, "inherited")
	}
	nodes := []*dset{d}
	var walk func(x *dset)
	walk = func(x *dset) {
		for _, c := range m.childrenOf(x.name) {
			if c.kind != kFS {
				continue
			}
			if inherits(c, prop) || companion != "" && inherits(c, companion) {
				nodes = append(nodes, c)
				walk(c)
			}
		}
	}
	walk(d)
	return nodes
}

// prefix is changelist_prefix (libzfs_changelist.c:96-170): for a
// mountpoint change every mounted node is unmounted, deepest mountpoint
// first (compare_mountpoints). It returns the nodes with their
// cn_mounted state. A mounted node with a mounted dataset beneath its
// mountpoint that is not in the list, and so is not unmounted ahead of
// it, fails zfs_unmount with EBUSY: prefix returns that node's dir and
// only the nodes already unmounted, which the caller postfixes.
func (m *Model) prefix(d *dset, prop string) (map[*dset]bool, string) {
	nodes := m.changelist(d, prop)
	if prop == "mountpoint" {
		slices.SortStableFunc(nodes, func(a, b *dset) int {
			return strings.Compare(m.effective(b, "mountpoint"), m.effective(a, "mountpoint"))
		})
	}
	was := map[*dset]bool{}
	for _, n := range nodes {
		if prop == "mountpoint" && n.mounted {
			dir := m.effective(n, "mountpoint")
			for _, o := range m.ds {
				if o != n && o.mounted && strings.HasPrefix(m.effective(o, "mountpoint"), dir+"/") {
					return was, dir // zfs_unmount fails EBUSY in changelist_prefix
				}
			}
		}
		was[n] = n.mounted
		if prop == "mountpoint" {
			n.mounted = false
		}
	}
	return was, ""
}

// postfix is changelist_postfix (libzfs_changelist.c:180-300): a node
// that is not mounted is mounted if it was mounted before, or if
// canmount=on and either the property is mountpoint or a share property
// is on (:258-268); zfs_mount then refuses an unmountable one.
func (m *Model) postfix(prop string, was map[*dset]bool) {
	for n, wasMounted := range was {
		if m.ds[n.name] != n || n.mounted || !m.mountable(n) {
			continue
		}
		share := m.effective(n, "sharenfs") != "off" || m.effective(n, "sharesmb") != "off"
		if wasMounted || (prop == "mountpoint" || share) && m.effective(n, "canmount") == "on" {
			n.mounted = true
		}
	}
}

// ---- create ---------------------------------------------------------------------

func (m *Model) create(args []string) *exitErr {
	o, e := getopt(args, "PV:b:nso:puv")
	if e != nil {
		return e
	}
	typ := kFS
	var props []kv
	if v, ok := o.vals['V']; ok { // zfs_main.c:1108-1121
		typ = kVol
		n, why, ok := niceStrToNum(v[len(v)-1])
		if !ok {
			return fail("bad volume size '%s': %s", v[len(v)-1], why)
		}
		props = append(props, kv{"volsize", u64(n)})
	}
	if v, ok := o.vals['b']; ok {
		n, why, ok := niceStrToNum(v[len(v)-1])
		if !ok {
			return fail("bad volume block size '%s': %s", v[len(v)-1], why)
		}
		props = append(props, kv{"volblocksize", u64(n)})
	}
	op, e := parseprops(o.vals['o'])
	if e != nil {
		return e
	}
	for _, p := range op {
		props = slices.DeleteFunc(props, func(x kv) bool { return x.k == p.k })
		props = append(props, p)
	}
	if (o.vals['b'] != nil || o.flags['s']) && typ != kVol { // :1172-1176
		return usage("'-s' and '-b' can only be used when creating a volume")
	}
	if o.flags['u'] && typ != kFS {
		return usage("'-u' can only be used when creating a filesystem")
	}
	if len(o.rest) == 0 {
		return usage("missing %s argument", typ)
	}
	if len(o.rest) > 1 {
		return usage("too many arguments")
	}
	name := o.rest[0]
	errbuf := fmt.Sprintf("cannot create '%s'", name)
	pool := poolOf(name)
	var vol []pval
	if typ == kVol { // :1200-1220: volumes validate before anything else
		if m.ds[pool] == nil {
			return fail("cannot open '%s': no such pool", pool)
		}
		var e *exitErr
		if vol, e = m.validProps(props, typ, nil, errbuf); e != nil {
			return e
		}
		vbs := uint64(16384)
		for _, p := range vol {
			if p.k == "volblocksize" {
				vbs = num(p.v)
			}
		}
		// round volsize up to volblocksize (:1236-1247)
		for i, p := range props {
			if p.k == "volsize" {
				n := num(p.v)
				if r := n % vbs; r != 0 {
					props[i].v = u64(n + vbs - r)
				}
			}
		}
		// thick by default: refreservation unless given (:1250-1272)
		if !o.flags['s'] && !slices.ContainsFunc(props, func(x kv) bool { return x.k == "refreservation" }) {
			var vs uint64
			for _, p := range props {
				if p.k == "volsize" {
					vs = num(p.v)
				}
			}
			props = append(props, kv{"refreservation", u64(zvolResv(vs, vbs))})
		}
	}
	if o.flags['p'] {
		if _, ok := validName(name, typ); ok {
			// :1280-1290: an existing dataset of the requested type is
			// success; any other type falls through to "already exists"
			if d := m.ds[name]; d != nil && d.kind == typ {
				return nil
			}
			if e := m.createAncestors(name); e != nil {
				return e
			}
		}
	}
	d, e := m.zfsCreate(name, typ, props)
	if e != nil {
		return e
	}
	if !o.flags['u'] {
		m.mountAndShare(d)
	}
	return nil
}

// mountAndShare is zfs_mount_and_share (cmd/zfs/zfs_main.c:772-819):
// only canmount=on filesystems, and only if mountable.
func (m *Model) mountAndShare(d *dset) {
	if d.kind == kFS && m.effective(d, "canmount") == "on" && m.mountable(d) {
		d.mounted = true
	}
}

// createAncestors is zfs_create_ancestors + create_parents
// (lib/libzfs/libzfs_dataset.c:3571-3680): each missing ancestor is
// created with no properties, mounted and shared.
func (m *Model) createAncestors(path string) *exitErr {
	anc, e := m.checkParents(path, true)
	if e != nil {
		return e
	}
	rest := strings.Split(strings.TrimPrefix(path, anc+"/"), "/")
	cur := anc
	for _, c := range rest[:len(rest)-1] {
		cur += "/" + c
		if m.ds[cur] != nil {
			continue
		}
		d, e := m.zfsCreate(cur, kFS, nil)
		if e != nil {
			return fail("cannot create '%s': failed to create ancestor '%s'", path, cur)
		}
		if m.mountable(d) {
			d.mounted = true // zfs_mount, not gated on canmount=on (:3612-3620)
		}
	}
	return nil
}

// zfsCreate is zfs_create (lib/libzfs/libzfs_dataset.c:3682-3851) plus the
// kernel's zfs_ioc_create (module/zfs/zfs_ioctl.c:3704-3822).
func (m *Model) zfsCreate(name string, typ kind, props []kv) (*dset, *exitErr) {
	errbuf := fmt.Sprintf("cannot create '%s'", name)
	if why, ok := validName(name, typ); !ok {
		return nil, fail("%s: %s", errbuf, why)
	}
	if strings.Count(name, "/") >= 50 {
		return nil, fail("%s: maximum name nesting depth exceeded", errbuf)
	}
	if _, e := m.checkParents(name, false); e != nil {
		return nil, e
	}
	if m.ds[name] != nil { // :3730-3734
		return nil, fail("%s: dataset already exists", errbuf)
	}
	list, e := m.validProps(props, typ, nil, errbuf)
	if e != nil {
		return nil, e
	}
	vbs, vs := uint64(16384), uint64(0)
	for _, p := range list {
		switch p.k {
		case "volblocksize":
			vbs = num(p.v)
		case "volsize":
			vs = num(p.v)
		}
	}
	if typ == kVol {
		if vs == 0 { // :3784-3789
			return nil, fail("%s: volume size cannot be zero", errbuf)
		}
		if vs%vbs != 0 { // :3791-3797
			return nil, fail("%s: volume size must be a multiple of volume block size", errbuf)
		}
	}
	d := m.newDS(name, typ)
	if typ == kVol {
		d.volsize, d.vbs = vs, vbs
	}
	var rest []pval
	for _, p := range list {
		if p.k == "volsize" || p.k == "volblocksize" {
			continue // objset attributes, not dsl props
		}
		rest = append(rest, p)
	}
	// zfs_ioc_create sets the props after creating, and destroys the new
	// dataset if any fails (zfs_ioctl.c:3798-3820); libzfs then reports
	// the errno (:3836-3848): ENOSPC is "out of space".
	if failed := m.applyProps(d, rest); len(failed) > 0 {
		delete(m.ds, name)
		return nil, fail("%s: out of space", errbuf)
	}
	return d, nil
}

// ---- snapshot ---------------------------------------------------------------------

func (m *Model) snapshot(args []string) *exitErr {
	o, e := getopt(args, "ro:")
	if e != nil {
		return e
	}
	props, e := parseprops(o.vals['o'])
	if e != nil {
		return e
	}
	if len(o.rest) == 0 {
		return usage("missing snapshot argument")
	}
	var snaps []string
	for _, a := range o.rest {
		fs, sn, ok := strings.Cut(a, "@")
		if !ok {
			return usage("") // zfs_main.c:4693-4695
		}
		// a missing dataset goes to usage, exit 2 (:4697-4700)
		d, e := m.open(fs, kFS|kVol)
		if e != nil {
			e.code = 2
			return e
		}
		snaps = append(snaps, d.name+"@"+sn)
		if o.flags['r'] {
			for k, x := range m.ds {
				if x.kind != kSnap && strings.HasPrefix(k, fs+"/") {
					snaps = append(snaps, k+"@"+sn)
				}
			}
		}
	}
	sort.Strings(snaps[1:])
	// zfs_snapshot_nvl (lib/libzfs/libzfs_dataset.c:4186-4262)
	for _, s := range snaps {
		if why, ok := validName(s, kSnap); !ok {
			return fail("cannot create snapshot '%s': %s", s, why)
		}
	}
	list, e := m.validProps(props, kSnap, nil, "cannot create snapshots ")
	if e != nil {
		return e
	}
	// lzc_snapshot is atomic; EEXIST per snapshot (:4231-4243)
	var msgs []string
	for _, s := range snaps {
		if m.ds[s] != nil {
			msgs = append(msgs, fmt.Sprintf("cannot create snapshot '%s': dataset already exists", s))
		}
	}
	if len(msgs) > 0 {
		if len(snaps) > 1 {
			msgs = append(msgs, "no snapshots were created")
		}
		return fail("%s", strings.Join(msgs, "\n"))
	}
	for _, s := range snaps {
		head := m.ds[s[:strings.IndexByte(s, '@')]]
		d := m.newDS(s, kSnap)
		d.volsize, d.vbs = head.volsize, head.vbs
		for _, p := range list {
			d.local[p.k] = p.v
		}
	}
	return nil
}

// ---- clone ----------------------------------------------------------------------

func (m *Model) clone(args []string) *exitErr {
	o, e := getopt(args, "o:pu")
	if e != nil {
		return e
	}
	props, e := parseprops(o.vals['o'])
	if e != nil {
		return e
	}
	switch {
	case len(o.rest) < 1:
		return usage("missing source dataset argument")
	case len(o.rest) < 2:
		return usage("missing target dataset argument")
	case len(o.rest) > 2:
		return usage("too many arguments")
	}
	src, e := m.open(o.rest[0], kSnap) // zfs_main.c:889
	if e != nil {
		return e
	}
	target := o.rest[1]
	if o.flags['p'] {
		if _, ok := validName(target, kFS|kVol); ok {
			if d := m.ds[target]; d != nil && d.kind != kSnap { // :899-905
				return nil
			}
			if e := m.createAncestors(target); e != nil {
				return e
			}
		}
	}
	// zfs_clone (lib/libzfs/libzfs_dataset.c:4019-4100)
	errbuf := fmt.Sprintf("cannot create '%s'", target)
	if why, ok := validName(target, kFS); !ok {
		return fail("%s: %s", errbuf, why)
	}
	if _, e := m.checkParents(target, false); e != nil {
		return e
	}
	head := m.headOf(src)
	typ := head.kind
	list, e := m.validProps(props, typ, src, errbuf)
	if e != nil {
		return e
	}
	// zfs_fix_auto_resv with the snapshot's handle (:4052-4055)
	for i, p := range list {
		if p.auto && typ == kVol {
			list[i].auto, list[i].v = false, u64(zvolResv(src.volsize, src.vbs))
		}
	}
	if m.ds[target] != nil { // lzc_clone EEXIST -> zfs_standard_error
		return fail("%s: dataset already exists", errbuf)
	}
	d := m.newDS(target, typ)
	d.origin = src
	d.volsize, d.vbs = src.volsize, src.vbs
	if failed := m.applyProps(d, list); len(failed) > 0 {
		delete(m.ds, target)
		return fail("%s: out of space", errbuf)
	}
	if !o.flags['u'] {
		m.mountAndShare(d)
	}
	return nil
}

// ---- destroy --------------------------------------------------------------------

func (m *Model) destroy(args []string) *exitErr {
	o, e := getopt(args, "vpndfrR")
	if e != nil {
		return e
	}
	recurse, doclones, deferd := o.flags['r'] || o.flags['R'], o.flags['R'], o.flags['d']
	if o.flags['n'] || o.flags['v'] || o.flags['p'] {
		return &exitErr{code: 3, msg: "zfsdiff model: -n/-v/-p not modelled"}
	}
	if len(o.rest) == 0 {
		return usage("missing dataset argument")
	}
	if len(o.rest) > 1 {
		return usage("too many arguments")
	}
	name := o.rest[0]
	if fs, spec, ok := strings.Cut(name, "@"); ok {
		return m.destroySnapshots(fs, spec, recurse, doclones, deferd)
	}
	types := kFS | kVol | kSnap
	if deferd {
		types = kSnap // zfs_main.c:1705-1708
	}
	d, e := m.open(name, types)
	if e != nil {
		return e
	}
	if !recurse && !strings.Contains(name, "/") && d.kind == kFS { // :1861-1874
		return fail("cannot destroy '%s': operation does not apply to pools\nuse 'zfs destroy -r %s' to destroy all datasets in the pool\nuse 'zpool destroy %s' to destroy the pool itself", name, name, name)
	}
	deps := m.dependents(d)
	if !doclones { // destroy_check_dependent (:1401-1455)
		var lines []string
		for _, x := range deps {
			if under(x.name, d.name) {
				if recurse {
					continue
				}
				if lines == nil {
					lines = []string{fmt.Sprintf("cannot destroy '%s': %s has children", name, d.kind), "use '-r' to destroy the following datasets:"}
				}
			} else {
				if !recurse {
					continue
				}
				if lines == nil {
					lines = []string{fmt.Sprintf("cannot destroy '%s': %s has dependent clones", name, d.kind), "use '-R' to destroy the following datasets:"}
				}
			}
			lines = append(lines, x.name)
		}
		if lines != nil {
			return fail("%s", strings.Join(lines, "\n"))
		}
	}
	return m.destroyWalk(append(deps, d), recurse)
}

// destroyWalk is destroy_callback over the dependents and then the target
// (cmd/zfs/zfs_main.c:1460-1530, :1885-1903): snapshots are batched and
// destroyed atomically before the next filesystem; with -r an error is
// recorded and the walk goes on.
func (m *Model) destroyWalk(list []*dset, recurse bool) *exitErr {
	var batch []*dset
	var msgs []string
	failed := false
	flush := func() bool {
		if len(batch) == 0 {
			return true
		}
		b := batch
		batch = nil
		if msg := m.destroySnaps(b, false); msg != "" {
			msgs = append(msgs, msg)
			return false
		}
		return true
	}
	for _, x := range list {
		if m.ds[x.name] != x {
			continue // already gone (a clone reached twice)
		}
		if !strings.Contains(x.name, "/") && x.kind == kFS {
			continue // the pool itself (:1480-1484)
		}
		if x.kind == kSnap {
			batch = append(batch, x)
			continue
		}
		ok := flush()
		if ok {
			if msg := m.destroyHead(x); msg != "" {
				msgs = append(msgs, msg)
				ok = false
			}
		}
		if !ok {
			failed = true
			if !recurse {
				return fail("%s", strings.Join(msgs, "\n"))
			}
		}
	}
	if !flush() {
		failed = true
	}
	if failed {
		return fail("%s", strings.Join(msgs, "\n"))
	}
	return nil
}

// destroyHead is lzc_destroy with dsl_destroy_head_check_impl
// (module/zfs/dsl_destroy.c:766-810) and the origin reaping of
// dsl_destroy_head_sync_impl (:1027-1031, :1081-1085).
func (m *Model) destroyHead(d *dset) string {
	if len(m.snapsOf(d.name)) > 0 {
		return fmt.Sprintf("cannot destroy '%s': dataset is busy", d.name) // EBUSY :787-790
	}
	if len(m.childrenOf(d.name)) > 0 {
		return fmt.Sprintf("cannot destroy '%s': dataset already exists", d.name) // EEXIST :797-800
	}
	o := d.origin
	delete(m.ds, d.name)
	if o != nil && m.ds[o.name] == o && o.deferred && len(m.clonesOf(o)) == 0 && len(o.holds) == 0 {
		delete(m.ds, o.name)
	}
	return ""
}

// destroySnaps is lzc_destroy_snaps: atomic (the channel program in
// dsl_destroy_snapshots_nvl, module/zfs/dsl_destroy.c:603-690, checks all
// before destroying any). With defer, a held or cloned snapshot is marked
// (:320-330); without, holds give EBUSY (:79-80) and clones EEXIST
// (:85-86). Messages: zfs_destroy_snaps_nvl (libzfs_dataset.c:3949-4015).
func (m *Model) destroySnaps(list []*dset, deferd bool) string {
	var msgs []string
	if !deferd {
		for _, s := range list {
			switch {
			case len(s.holds) > 0:
				msgs = append(msgs, fmt.Sprintf("cannot destroy snapshot %s: it's being held. Run 'zfs holds -r %s' to see holders.", s.name, s.name))
			case len(m.clonesOf(s)) > 0:
				msgs = append(msgs, fmt.Sprintf("cannot destroy snapshot %s: snapshot is cloned", s.name))
			}
		}
	}
	if len(msgs) > 0 {
		return strings.Join(msgs, "\n")
	}
	for _, s := range list {
		if deferd && (len(s.holds) > 0 || len(m.clonesOf(s)) > 0) {
			s.deferred = true
			continue
		}
		delete(m.ds, s.name)
	}
	return ""
}

func (m *Model) destroySnapshots(fs, spec string, recurse, doclones, deferd bool) *exitErr {
	head, e := m.open(fs, kFS|kVol) // zfs_main.c:1745-1752
	if e != nil {
		return e
	}
	// gather_snapshots (:1638-1665): fs@spec, and with -r every
	// descendant's
	var snaps []*dset
	var lines []string
	heads := []*dset{head}
	if recurse {
		for k, x := range m.ds {
			if x.kind != kSnap && strings.HasPrefix(k, fs+"/") {
				heads = append(heads, x)
			}
		}
		sort.Slice(heads[1:], func(i, j int) bool { return heads[1+i].name < heads[1+j].name })
	}
	for _, h := range heads {
		s := m.ds[h.name+"@"+spec]
		if s == nil {
			continue
		}
		if !doclones && !deferd {
			// snapshot_to_nvl_cb checks for clones (:1618-1635)
			if deps := m.dependents(s); len(deps) > 0 {
				lines = append(lines, fmt.Sprintf("cannot destroy '%s': snapshot has dependent clones", s.name), "use '-R' to destroy the following datasets:")
				for _, x := range deps {
					lines = append(lines, x.name)
				}
			}
		}
		snaps = append(snaps, s)
	}
	if lines != nil {
		return fail("%s", strings.Join(lines, "\n"))
	}
	if len(snaps) == 0 { // :1763-1767
		return fail("could not find any snapshots to destroy; check snapshot names.")
	}
	if doclones { // destroy_clones (:1667-1690)
		var all []*dset
		for _, s := range snaps {
			all = append(all, m.dependents(s)...)
		}
		if e := m.destroyWalk(all, true); e != nil {
			return e
		}
	}
	if msg := m.destroySnaps(snaps, deferd); msg != "" {
		return fail("%s", msg)
	}
	return nil
}

// ---- set / inherit ------------------------------------------------------------------

func (m *Model) set(args []string) *exitErr {
	o, e := getopt(args, "u")
	if e != nil {
		return e
	}
	a := o.rest
	if len(a) < 1 {
		return usage("missing arguments")
	}
	if len(a) < 2 {
		if strings.Contains(a[0], "=") {
			return usage("missing dataset name(s)")
		}
		return usage("missing property=value argument(s)")
	}
	start := -1
	for i, s := range a { // zfs_main.c:4575-4590
		if strings.Contains(s, "=") {
			if start > 0 {
				return usage("invalid argument order")
			}
		} else if start < 0 {
			start = i
		}
	}
	if start < 0 {
		return usage("missing dataset name(s)")
	}
	props, e := parseprops(a[:start])
	if e != nil {
		return &exitErr{code: 255, msg: e.msg} // ret = -1 (:4595-4598)
	}
	var errs []string
	for _, name := range a[start:] {
		d, e := m.open(name, kFS|kVol|kSnap)
		if e != nil {
			errs = append(errs, e.msg)
			continue
		}
		if msg := m.setOne(d, props, o.flags['u']); msg != "" {
			errs = append(errs, msg)
		}
	}
	if len(errs) > 0 {
		return fail("%s", strings.Join(errs, "\n"))
	}
	return nil
}

// setOne is zfs_prop_set_list_flags (lib/libzfs/libzfs_dataset.c:1749-1925).
func (m *Model) setOne(d *dset, props []kv, nomount bool) string {
	errbuf := fmt.Sprintf("cannot set property for '%s'", d.name)
	list, e := m.validProps(props, d.kind, d, errbuf)
	if e != nil {
		return e.msg
	}
	// zfs_add_synthetic_resv (:1603-1647): an existing volume whose
	// refreservation equals the auto value for its size keeps tracking
	// volsize, unless refreservation is in the same set
	added := false
	if d.kind == kVol {
		var nv *pval
		hasResv := false
		for i := range list {
			switch list[i].k {
			case "volsize":
				nv = &list[i]
			case "refreservation":
				hasResv = true
			}
		}
		if nv != nil && !hasResv && zvolResv(d.volsize, d.vbs) == num(m.effective(d, "refreservation")) {
			list = append(list, pval{k: "refreservation", v: u64(zvolResv(num(nv.v), d.vbs))})
			added = true
		}
		// zfs_fix_auto_resv (:1654-1700), uses the new volsize if set
		if !added {
			vs := d.volsize
			for _, p := range list {
				if p.k == "volsize" {
					vs = num(p.v)
				}
			}
			for i := range list {
				if list[i].auto {
					list[i].auto, list[i].v = false, u64(zvolResv(vs, d.vbs))
				}
			}
		}
	}
	// changelist prefix: a mountpoint change, or canmount=off on a mounted
	// filesystem, unmounts (:1797-1826); -u gathers with
	// CL_GATHER_DONT_UNMOUNT and does neither prefix nor postfix
	// (libzfs_changelist.c:193-194)
	type cl struct {
		prop string
		was  map[*dset]bool
	}
	var cls []cl
	if !nomount && d.kind == kFS {
		for _, p := range list {
			var c cl
			unmount := "" // the prefix's property: canmount gathers as mountpoint
			switch {
			case p.k == "mountpoint" || p.k == "sharenfs" || p.k == "sharesmb":
				c, unmount = cl{p.k, nil}, p.k
			case p.k == "canmount" && p.v == "off" && d.mounted:
				// gathered as a mountpoint changelist whose realprop is
				// canmount (libzfs_changelist.c:710-711; dataset.c:1836-
				// 1844): children with an inherited mountpoint unmount in
				// the prefix too (change_one :538-550), and the postfix
				// runs only if the set succeeds (dataset.c:1908), so a
				// failed set leaves them unmounted. The postfix is keyed
				// on canmount, so the mountpoint remount rule stays off
				// (changelist.c:260-261).
				c, unmount = cl{"canmount", nil}, "mountpoint"
			default:
				continue
			}
			// the prefix runs before the set (lib/libzfs/libzfs_dataset.c
			// zfs_prop_set_list_flags: changelist_prefix ~:1856, the
			// property ioctl ~:1871, tag zfs-2.4.0). A busy prefix ends
			// the set with nothing applied, so a refreservation in the
			// same list never reports nospace, and the earlier
			// changelists are not postfixed (the caller returns at once).
			was, dir := m.prefix(d, unmount)
			if dir != "" {
				m.postfix(c.prop, was)
				return fmt.Sprintf("cannot unmount '%s': pool or dataset is busy", dir)
			}
			c.was = was
			cls = append(cls, c)
		}
	}
	oldVolsize := d.volsize
	failed := m.applyProps(d, list)
	if len(failed) > 0 {
		// a synthetic reservation that failed ENOSPC: libzfs sets the old
		// volsize back (libzfs_dataset.c:1871-1886)
		if added && slices.Contains(failed, "refreservation") {
			d.volsize = oldVolsize
		}
		var msgs []string
		for _, f := range failed {
			msgs = append(msgs, setpropError(errbuf, f))
		}
		return strings.Join(msgs, "\n")
	}
	for _, c := range cls {
		m.postfix(c.prop, c.was)
	}
	return ""
}

func (m *Model) inherit(args []string) *exitErr {
	o, e := getopt(args, "rS")
	if e != nil {
		return e
	}
	if len(o.rest) < 1 {
		return usage("missing property argument")
	}
	if len(o.rest) < 2 {
		return usage("missing dataset argument")
	}
	prop, recvd := o.rest[0], o.flags['S']
	// zfs_do_inherit (cmd/zfs/zfs_main.c:2518-2552)
	if def, ok := propDefs[prop]; ok {
		if def.attr == pReadonly || def.attr == pOnetime {
			return fail("%s property is read-only", prop)
		}
		if def.attr != pInherit && !recvd {
			msg := fmt.Sprintf("'%s' property cannot be inherited", prop)
			switch prop {
			case "quota", "reservation", "refquota", "refreservation":
				msg += fmt.Sprintf("\nuse 'zfs set %s=none' to clear\nuse 'zfs inherit -S %s' to revert to received value", prop, prop)
			}
			return fail("%s", msg)
		}
		if recvd && prop == "volsize" {
			return fail("'%s' property cannot be reverted to a received value", prop)
		}
	} else if slices.Contains(unmodelled, prop) {
		return &exitErr{code: 3, msg: "zfsdiff model: unmodelled property " + prop}
	} else if !isUserProp(prop) {
		return usage("invalid property '%s'", prop)
	}
	var errs []string
	for _, name := range o.rest[1:] {
		d, e := m.open(name, kFS|kVol|kSnap)
		if e != nil {
			errs = append(errs, e.msg)
			continue
		}
		targets := []*dset{d}
		if o.flags['r'] {
			for k, x := range m.ds {
				if strings.HasPrefix(k, name+"/") || strings.HasPrefix(k, name+"@") {
					targets = append(targets, x)
				}
			}
			sort.Slice(targets[1:], func(i, j int) bool { return targets[1+i].name < targets[1+j].name })
		}
		for i, t := range targets {
			def, native := propDefs[prop]
			if native && def.types&t.kind == 0 {
				if i > 0 {
					continue // inherit_recurse_cb skips (:2457-2464)
				}
				errs = append(errs, fmt.Sprintf("cannot inherit %s for '%s': property doesn't apply to datasets of this type", prop, t.name))
				continue
			}
			if msg := m.inheritOne(t, prop, recvd); msg != "" {
				return fail("%s", msg) // the inherit stops at the busy target
			}
		}
	}
	if len(errs) > 0 {
		return fail("%s", strings.Join(errs, "\n"))
	}
	return nil
}

// inheritOne is zfs_prop_inherit (lib/libzfs/libzfs_dataset.c:1932-2040)
// and the kernel's dsl_prop_set_sync_impl (module/zfs/dsl_prop.c:723-845):
// plain inherit removes the local value and, on a dataset with received
// properties, writes an explicit-inherit marker that hides the received
// value; -S removes both and so reverts to the received value.
func (m *Model) inheritOne(d *dset, prop string, recvd bool) string {
	var was map[*dset]bool
	cl := d.kind == kFS && (prop == "mountpoint" || prop == "sharenfs" || prop == "sharesmb")
	if cl {
		var dir string
		was, dir = m.prefix(d, prop)
		if dir != "" { // the busy prefix ends the inherit before any local value is removed
			m.postfix(prop, was)
			return fmt.Sprintf("cannot unmount '%s': pool or dataset is busy", dir)
		}
	}
	delete(d.local, prop)
	if recvd {
		delete(d.inh, prop)
	} else if m.hasRecvd(d) {
		d.inh[prop] = true
	}
	if cl {
		m.postfix(prop, was)
	}
	return ""
}

// hasRecvd is dsl_prop_get_int_ds(ds, "$hasrecvd") (module/zfs/
// dsl_prop.c:805-806): an unknown name is looked up as inheritable, so a
// $hasrecvd on any ancestor counts.
func (m *Model) hasRecvd(d *dset) bool {
	if d.kind == kSnap {
		d = m.headOf(d)
	}
	for ; d != nil; d = m.ds[parentName(d.name)] {
		if d.hasRecvd {
			return true
		}
	}
	return false
}

// ---- holds ------------------------------------------------------------------------

func (m *Model) holdRelease(args []string, holding bool) *exitErr {
	spec := "r"
	if holding {
		spec = "rt"
	}
	o, e := getopt(args, spec)
	if e != nil {
		return e
	}
	if len(o.rest) < 2 {
		return usage("")
	}
	tag := o.rest[0]
	if holding && strings.HasPrefix(tag, ".") { // zfs_main.c:6782-6786
		return usage("tag may not start with '.'")
	}
	var errs []string
	for _, path := range o.rest[1:] {
		fs, sn, ok := strings.Cut(path, "@")
		if !ok {
			errs = append(errs, fmt.Sprintf("'%s' is not a snapshot", path))
			continue
		}
		head, e := m.open(fs, kFS|kVol)
		if e != nil {
			errs = append(errs, e.msg)
			continue
		}
		heads := []*dset{head}
		if o.flags['r'] {
			// zfs_hold_one/zfs_release_one recurse depth first
			// (zfs_iter_filesystems_v2); name order is one such order
			for k, x := range m.ds {
				if x.kind != kSnap && strings.HasPrefix(k, fs+"/") {
					heads = append(heads, x)
				}
			}
			sort.Slice(heads[1:], func(i, j int) bool { return heads[1+i].name < heads[1+j].name })
		}
		if holding {
			if msg := m.hold(heads, sn, tag); msg != "" {
				errs = append(errs, msg)
			}
		} else if msg := m.release(heads, sn, tag); msg != "" {
			errs = append(errs, msg)
		}
	}
	if len(errs) > 0 {
		return fail("%s", strings.Join(errs, "\n"))
	}
	return nil
}

// hold is zfs_hold + zfs_hold_nvl (lib/libzfs/libzfs_dataset.c:4964-5080)
// and dsl_dataset_user_hold (module/zfs/dsl_userhold.c): atomic.
func (m *Model) hold(heads []*dset, sn, tag string) string {
	var snaps []*dset
	for _, h := range heads {
		if s := m.ds[h.name+"@"+sn]; s != nil {
			snaps = append(snaps, s)
		}
	}
	if len(snaps) == 0 {
		return fmt.Sprintf("cannot hold snapshot '%s@%s': dataset does not exist", heads[0].name, sn)
	}
	var msgs []string
	for _, s := range snaps {
		if s.holds[tag] { // EEXIST -> EZFS_REFTAG_HOLD (libzfs_util.c:234-236)
			msgs = append(msgs, fmt.Sprintf("cannot hold snapshot '%s': tag already exists on this dataset", s.name))
		}
	}
	if len(msgs) > 0 {
		return strings.Join(msgs, "\n")
	}
	for _, s := range snaps {
		s.holds[tag] = true
	}
	return ""
}

// release is zfs_release (lib/libzfs/libzfs_dataset.c:5084-5160); the last
// hold on a deferred, unclone snapshot destroys it
// (module/zfs/dsl_userhold.c:424-426, :536-545).
func (m *Model) release(heads []*dset, sn, tag string) string {
	var snaps []*dset
	why := ""
	for _, h := range heads {
		s := m.ds[h.name+"@"+sn]
		switch {
		case s == nil:
			why = "dataset does not exist"
		case !s.holds[tag]:
			why = "no such tag on this dataset"
		default:
			snaps = append(snaps, s)
		}
	}
	if len(snaps) == 0 {
		return fmt.Sprintf("cannot release hold from snapshot '%s@%s': %s", heads[0].name, sn, why)
	}
	for _, s := range snaps {
		delete(s.holds, tag)
		if s.deferred && len(s.holds) == 0 && len(m.clonesOf(s)) == 0 {
			delete(m.ds, s.name)
		}
	}
	return ""
}

// ---- rename / promote -------------------------------------------------------------

func (m *Model) rename(args []string) *exitErr {
	o, e := getopt(args, "pruf")
	if e != nil {
		return e
	}
	switch {
	case len(o.rest) < 1:
		return usage("missing source dataset argument")
	case len(o.rest) < 2:
		return usage("missing target dataset argument")
	case len(o.rest) > 2:
		return usage("too many arguments")
	case o.flags['r'] && o.flags['p']:
		return usage("-p and -r options are mutually exclusive")
	case o.flags['u'] && o.flags['p']:
		return usage("-u and -p options are mutually exclusive")
	case o.flags['r'] && !strings.Contains(o.rest[0], "@"):
		return usage("source dataset for recursive rename must be a snapshot")
	}
	types := kFS | kVol | kSnap
	if o.flags['u'] {
		types = kFS
	} else if o.flags['p'] {
		types = kFS | kVol
	}
	d, e := m.open(o.rest[0], types)
	if e != nil {
		return e
	}
	target := o.rest[1]
	if o.flags['p'] {
		if _, ok := validName(target, d.kind); ok {
			if e := m.createAncestors(target); e != nil {
				return e
			}
		}
	}
	// zfs_rename (lib/libzfs/libzfs_dataset.c:4469-4660)
	if d.name == target {
		return nil
	}
	errbuf := fmt.Sprintf("cannot rename to '%s'", target)
	if d.kind == kSnap {
		head := d.name[:strings.IndexByte(d.name, '@')]
		if !strings.Contains(target, "@") {
			target = head + "@" + target
		} else if strings.HasPrefix(target, "@") {
			target = head + target
		} else if target[:strings.IndexByte(target, '@')] != head {
			return fail("%s: snapshots must be part of same dataset", errbuf)
		}
		if why, ok := validName(target, kSnap); !ok {
			return fail("%s: %s", errbuf, why)
		}
		short := target[strings.IndexByte(target, '@'):]
		var moves [][2]string
		heads := []string{head}
		if o.flags['r'] {
			for k, x := range m.ds {
				if x.kind != kSnap && strings.HasPrefix(k, head+"/") {
					heads = append(heads, k)
				}
			}
			sort.Strings(heads[1:])
		}
		oldShort := d.name[strings.IndexByte(d.name, '@'):]
		for _, h := range heads {
			if m.ds[h+oldShort] == nil {
				continue
			}
			if m.ds[h+short] != nil {
				if o.flags['r'] {
					return fail("cannot rename '%s': a child dataset already has a snapshot with the new name", h+oldShort)
				}
				return fail("cannot rename '%s': dataset already exists", d.name)
			}
			moves = append(moves, [2]string{h + oldShort, h + short})
		}
		for _, mv := range moves {
			m.rekey(mv[0], mv[1])
		}
		return nil
	}
	if why, ok := validName(target, d.kind); !ok {
		return fail("%s: %s", errbuf, why)
	}
	if _, e := m.checkParents(target, false); e != nil {
		return e
	}
	if poolOf(target) != poolOf(d.name) {
		return fail("%s: datasets must be within same pool", errbuf)
	}
	if strings.HasPrefix(target, d.name+"/") {
		return fail("%s: New dataset name cannot be a descendant of current dataset name", errbuf)
	}
	if m.ds[target] != nil {
		return fail("cannot rename '%s': dataset already exists", d.name)
	}
	from := d.name
	var keys []string
	for k := range m.ds {
		if under(k, from) {
			keys = append(keys, k)
		}
	}
	for _, k := range keys {
		m.rekey(k, target+k[len(from):])
	}
	return nil
}

func (m *Model) rekey(from, to string) {
	x := m.ds[from]
	delete(m.ds, from)
	x.name = to
	m.ds[to] = x
}

func (m *Model) promote(args []string) *exitErr {
	if len(args) > 0 && strings.HasPrefix(args[0], "-") {
		return usage("invalid option '%c'", args[0][1])
	}
	if len(args) < 1 {
		return usage("missing clone filesystem argument")
	}
	if len(args) > 1 {
		return usage("too many arguments")
	}
	d, e := m.open(args[0], kFS|kVol)
	if e != nil {
		return e
	}
	errbuf := fmt.Sprintf("cannot promote '%s'", d.name)
	if d.origin == nil { // lib/libzfs/libzfs_dataset.c:4116-4120
		return fail("%s: not a cloned filesystem", errbuf)
	}
	origin := d.origin
	old := m.headOf(origin)
	// shared_snaps: the origin head's snapshots up to the origin
	// (dsl_dataset_promote_check, module/zfs/dsl_dataset.c:3436-3650)
	var moved []*dset
	for _, s := range m.snapsOf(old.name) {
		if s.txg <= origin.txg {
			moved = append(moved, s)
		}
	}
	for _, s := range moved {
		short := s.name[strings.IndexByte(s.name, '@'):]
		if m.ds[d.name+short] != nil { // EEXIST (:3536-3542), libzfs :4135-4141
			return fail("%s: conflicting snapshot '%s' from parent '%s'", errbuf, short[1:], origin.name)
		}
	}
	for _, s := range moved {
		m.rekey(s.name, d.name+s.name[strings.IndexByte(s.name, '@'):])
	}
	d.origin, old.origin = old.origin, origin
	return nil
}

// ---- mount ------------------------------------------------------------------------

// mount is `zfs mount FS`: share_mount (cmd/zfs/zfs_main.c:7486) opens the
// one filesystem (zfs_open with ZFS_TYPE_FILESYSTEM, :7702-7703) and runs
// share_mount_one(OP_MOUNT, explicit) (:7170). It mounts and does not
// share (zfs_mount, not zfs_mount_and_share). Options are not modelled.
func (m *Model) mount(args []string) *exitErr {
	if len(args) > 0 && strings.HasPrefix(args[0], "-") {
		return &exitErr{code: 3, msg: "zfsdiff model: mount options not modelled"}
	}
	if len(args) == 0 {
		return &exitErr{code: 3, msg: "zfsdiff model: mount listing not modelled"}
	}
	if len(args) > 1 {
		return usage("too many arguments") // :7697-7701
	}
	d, e := m.open(args[0], kFS)
	if e != nil {
		return e
	}
	errbuf := fmt.Sprintf("cannot mount '%s'", d.name)
	switch m.effective(d, "mountpoint") {
	case "legacy": // :7241-7247
		return fail("%s: legacy mountpoint\nuse mount(8) to mount this filesystem", errbuf)
	case "none": // :7251-7257
		return fail("%s: no mountpoint set", errbuf)
	}
	if m.effective(d, "canmount") == "off" { // :7270-7277; noauto passes when explicit
		return fail("%s: 'canmount' property is set to 'off'", errbuf)
	}
	if d.mounted { // :7380-7388
		return fail("%s: filesystem already mounted", errbuf)
	}
	d.mounted = true
	return nil
}

// ---- get --------------------------------------------------------------------------

// allProps is the property list `zfs get all` prints in the model.
var allProps = func() []string {
	var out []string
	for k := range propDefs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}()

func (m *Model) get(args []string) ([]byte, *exitErr) {
	o, e := getopt(args, "d:o:s:jrt:Hp")
	if e != nil {
		return nil, e
	}
	if o.flags['j'] {
		return nil, &exitErr{code: 3, msg: "zfsdiff model: get -j not modelled"}
	}
	if len(o.rest) < 1 {
		return nil, usage("missing property argument")
	}
	fields := strings.Split(o.rest[0], ",")
	var props []string
	for _, f := range fields {
		switch {
		case f == "all":
			props = append(props, allProps...)
		case isUserProp(f):
			props = append(props, f)
		case propDefs[f].types != 0:
			props = append(props, f)
		case slices.Contains(unmodelled, f):
			return nil, &exitErr{code: 3, msg: "zfsdiff model: unmodelled property " + f}
		default:
			// zprop_get_list (lib/libzfs/libzfs_util.c:1958-1965), then usage
			return nil, usage("bad property list: invalid property '%s'", f)
		}
	}
	types := kFS | kVol | kSnap
	if t, ok := o.vals['t']; ok {
		types = 0
		for _, s := range strings.Split(t[len(t)-1], ",") {
			switch s {
			case "filesystem", "fs":
				types |= kFS
			case "volume", "vol":
				types |= kVol
			case "snapshot", "snap":
				types |= kSnap
			case "all":
				types |= kFS | kVol | kSnap
			}
		}
	}
	depth := -1
	if d, ok := o.vals['d']; ok {
		depth, _ = strconv.Atoi(d[len(d)-1])
	} else if !o.flags['r'] {
		depth = 0
	}
	var b strings.Builder
	var errs []string
	for _, root := range o.rest[1:] {
		r := m.ds[root]
		if r == nil {
			if why, ok := validName(root, kFS|kVol|kSnap); !ok {
				errs = append(errs, fmt.Sprintf("cannot open '%s': %s", root, why))
			} else {
				errs = append(errs, fmt.Sprintf("cannot open '%s': dataset does not exist", root))
			}
			continue
		}
		for _, d := range m.walk(r, depth) {
			if d.kind&types == 0 {
				continue
			}
			for _, p := range props {
				v, src := m.value(d, p)
				fmt.Fprintf(&b, "%s\t%s\t%s\t%s\n", d.name, p, v, src)
			}
		}
	}
	if len(errs) > 0 {
		return []byte(b.String()), fail("%s", strings.Join(errs, "\n"))
	}
	return []byte(b.String()), nil
}

// walk lists root and its descendants to depth (-1 unlimited) the way
// zfs_for_each does (lib/libzfs/libzfs_iter.c, zfs_callback): a dataset
// at level L lists its snapshots only if L < depth.
func (m *Model) walk(r *dset, depth int) []*dset {
	out := []*dset{r}
	if r.kind == kSnap {
		return out
	}
	var rec func(d *dset, lvl int)
	rec = func(d *dset, lvl int) {
		if depth >= 0 && lvl >= depth {
			return
		}
		for _, c := range m.childrenOf(d.name) {
			out = append(out, c)
			rec(c, lvl+1)
		}
		out = append(out, m.snapsOf(d.name)...)
	}
	rec(r, 0)
	return out
}

// ---- send | recv ----------------------------------------------------------------

// SendRecv models `zfs send -p snap | zfs recv dst`: dst is created as a
// copy of snap's head with every property set locally (or received) on
// that head arriving as received (ZPROP_SRC_RECEIVED, dsl_prop.c:786-793),
// plus the snapshot dst@<name> with snap's own properties received;
// zfs recv then mounts dst.
func (m *Model) SendRecv(snap, dst string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.ds[snap]
	if s == nil || s.kind != kSnap {
		return &exitErrWrap{fail("cannot open '%s': dataset does not exist", snap)}
	}
	if m.ds[dst] != nil {
		return &exitErrWrap{fail("cannot receive new filesystem stream: destination '%s' exists\nmust specify -F to overwrite it", dst)}
	}
	if m.ds[parentName(dst)] == nil {
		return &exitErrWrap{fail("cannot receive new filesystem stream: destination '%s' does not exist", parentName(dst))}
	}
	head := m.headOf(s)
	d := m.newDS(dst, head.kind)
	d.volsize, d.vbs = s.volsize, s.vbs
	d.hasRecvd = true
	for k, v := range head.recvd {
		if !head.inh[k] {
			d.recvd[k] = v
		}
	}
	for k, v := range head.local {
		d.recvd[k] = v
	}
	sn := m.newDS(dst+snap[strings.IndexByte(snap, '@'):], kSnap)
	sn.volsize, sn.vbs = d.volsize, d.vbs
	// the stream's snapprops: zfs_receive_one sets the snapshot's own
	// properties on the received snapshot as received (libzfs_sendrecv.c,
	// ZFS_IOC_SET_PROP with zc_cookie = B_TRUE)
	for k, v := range s.recvd {
		if !s.inh[k] {
			sn.recvd[k] = v
		}
	}
	for k, v := range s.local {
		sn.recvd[k] = v
	}
	m.mountAndShare(d)
	return nil
}

type exitErrWrap struct{ e *exitErr }

func (w *exitErrWrap) Error() string { return w.e.msg }
