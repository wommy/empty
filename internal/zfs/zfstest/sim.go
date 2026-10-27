// Package zfstest simulates the zfs(8) CLI closely enough to test callers
// without a pool: dataset tree, snapshots, clone/origin graph, user
// properties, and the stderr text real zfs prints for the failures callers
// must classify (see internal/zfs/errors.go for the OpenZFS sources).
//
// It is an oracle for control flow, not for ZFS itself. Behaviors were
// audited against OpenZFS source (docs/research/simfidelity/). Not modelled:
// promote beyond the origin snapshot, holds, busy (open) zvols, -R, -n,
// native property inheritance other than sharenfs/sharesmb, size suffixes;
// space accounting is approximate. CI runs the same contract tests on a
// real pool.
//
// NFS: a filesystem is mounted unless canmount is off/noauto, its
// mountpoint is none/legacy, or Unmount unmounted it. Sharing a mounted
// filesystem rewrites /etc/exports.d/zfs.exports like libzfs and runs
// `exportfs -ra`; both are the reference models in internal/nfsref
// (OpenZFS libshare and nfs-utils, derived from their sources), so etab
// shows what a real host shows: "*" for an empty host, '@' dropped,
// crossmnt for "on", first-wins duplicates, client classes in mountd
// order, a malformed line ending the file, realpath. An invalid sharenfs
// is refused like `zfs set` refuses it.
// A plain `zfs set sharenfs` on an unmounted canmount=on filesystem mounts
// and shares it (changelist_postfix); `zfs set -u` does neither. Failure
// modes, each with zfs still exiting 0:
//   - SetNFSDown: exportfs fails, so the table stops changing;
//   - SetNFSDRunning(false): `systemctl stop nfs-server` (exportfs -au)
//     empties the table and NFSDThreads reports 0, but later sharenfs
//     changes still repopulate it (exportfs -ra writes etab without nfsd);
//     starting again re-exports every shared dataset (exportfs -r);
//   - DropHost: exportfs leaves one client out and applies the rest.
package zfstest

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wommy/empty/internal/nfsref"
	"github.com/wommy/empty/internal/zfs"
)

type node struct {
	typ    zfs.Type
	props  map[string]string // local props
	recv   map[string]string // received props (zfs recv), below local ones
	origin string            // clone origin snapshot
	defer_ bool              // deferred destroy pending
	birth  int64             // creation, unix seconds
	// unmounted by Unmount: canmount=on, but not mounted
	unmounted bool
}

// Sim is an in-memory zfs. Safe for concurrent use.
type Sim struct {
	mu    sync.Mutex
	ds    map[string]*node
	Calls [][]string
	// Fail lets a test inject a failure: return non-nil to fail the call.
	Fail func(args []string) error
	// PoolSize is reported as available space at the pool root.
	PoolSize uint64
	// Clock stamps the creation property of new datasets and snapshots
	// (unix seconds). Nil is the wall clock.
	Clock func() int64

	exports   string            // /etc/exports.d/zfs.exports
	shared    map[string]string // dataset -> mountpoint it has lines under
	etab      string            // /var/lib/nfs/etab
	nfsDown   bool
	nfsdOff   bool            // nfs-server stopped: no nfsd threads
	dropHosts map[string]bool // clients exportfs leaves out of etab
}

func (s *Sim) now() int64 {
	if s.Clock != nil {
		return s.Clock()
	}
	return time.Now().Unix()
}

// New returns a Sim with the given pools.
func New(pools ...string) *Sim {
	s := &Sim{ds: map[string]*node{}, PoolSize: 1 << 40, shared: map[string]string{}}
	for _, p := range pools {
		s.ds[p] = &node{typ: zfs.TypeFilesystem, props: map[string]string{}}
	}
	return s
}

func exit(format string, a ...any) error {
	return &zfs.ExitError{Code: 1, Stderr: fmt.Sprintf(format, a...) + "\n"}
}

// Run implements zfs.Runner.
func (s *Sim) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Calls = append(s.Calls, append([]string{name}, args...))
	if s.Fail != nil {
		if err := s.Fail(args); err != nil {
			return nil, err
		}
	}
	if len(args) == 0 {
		return nil, exit("missing command")
	}
	switch args[0] {
	case "create":
		return nil, s.create(args[1:])
	case "snapshot":
		return nil, s.snapshot(args[1:])
	case "clone":
		return nil, s.clone(args[1:])
	case "destroy":
		return nil, s.destroy(args[1:])
	case "get":
		return s.get(args[1:])
	case "set":
		return nil, s.set(args[1:])
	case "inherit":
		return nil, s.inherit(args[1:])
	case "promote":
		return nil, s.promote(args[1:])
	}
	return nil, exit("unrecognized command '%s'", args[0])
}

// Has reports whether name exists (test helper).
func (s *Sim) Has(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.ds[name]
	return ok
}

// Prop returns a local property (test helper).
func (s *Sim) Prop(name, prop string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n, ok := s.ds[name]; ok {
		return n.props[prop]
	}
	return ""
}

// Receive sets a property with source "received", as `zfs recv` of a
// replication stream does (test helper). A local value still wins.
func (s *Sim) Receive(name, prop, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.ds[name]
	if n.recv == nil {
		n.recv = map[string]string{}
	}
	n.recv[prop] = value
}

func parent(name string) string {
	if i := strings.LastIndex(name, "@"); i >= 0 {
		return name[:i]
	}
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[:i]
	}
	return ""
}

func takeOpts(args []string) (map[string]string, []string, error) {
	props := map[string]string{}
	var rest []string
	for i := 0; i < len(args); i++ {
		if args[i] == "-o" && i+1 < len(args) {
			kv := strings.SplitN(args[i+1], "=", 2)
			if len(kv) != 2 {
				return nil, nil, exit("missing '=' for property argument")
			}
			props[kv[0]] = kv[1]
			i++
			continue
		}
		rest = append(rest, args[i])
	}
	return props, rest, nil
}

func (s *Sim) create(args []string) error {
	props, rest, err := takeOpts(args)
	if err != nil {
		return err
	}
	var parents, sparse bool
	var volsize string
	var name string
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "-p":
			parents = true
		case "-s":
			sparse = true
		case "-V":
			i++
			volsize = rest[i]
		default:
			name = rest[i]
		}
	}
	if sparse && volsize == "" {
		return exit("'-s' and '-b' can only be used when creating a volume")
	}
	if _, ok := s.ds[name]; ok {
		if parents {
			return nil // real zfs: -p on an existing dataset succeeds silently
		}
		return exit("cannot create '%s': dataset already exists", name)
	}
	p := parent(name)
	if _, ok := s.ds[p]; !ok {
		if pool := strings.SplitN(name, "/", 2)[0]; s.ds[pool] == nil {
			return exit("cannot create '%s': no such pool '%s'", name, pool)
		}
		if !parents {
			return exit("cannot create '%s': parent does not exist", name)
		}
		// create intermediates
		var chain []string
		for q := p; q != ""; q = parent(q) {
			if _, ok := s.ds[q]; ok {
				break
			}
			chain = append([]string{q}, chain...)
		}
		if len(chain) > 0 && parent(chain[0]) == "" {
			return exit("cannot create '%s': no such pool '%s'", name, chain[0])
		}
		for _, q := range chain {
			s.ds[q] = &node{typ: zfs.TypeFilesystem, props: map[string]string{}}
		}
	}
	typ := zfs.TypeFilesystem
	if volsize != "" {
		typ = zfs.TypeVolume
	}
	if k, bad := misfit(props, typ); bad {
		return exit("cannot create '%s': '%s' does not apply to datasets of this type", name, k)
	}
	if msg, ok := s.canonProps(props); !ok {
		return exit("cannot create '%s': %s", name, msg)
	}
	if v, ok := props["sharenfs"]; ok && nfsref.ValidateSharenfs(v) != nil {
		return exit("cannot create '%s': 'sharenfs' cannot be set to invalid options", name)
	}
	n := &node{typ: zfs.TypeFilesystem, props: props, birth: s.now()}
	if volsize != "" {
		v, err := strconv.ParseUint(volsize, 10, 64)
		if err != nil || v == 0 {
			return exit("cannot create '%s': volume size cannot be zero", name)
		}
		// zfs_main.c rounds -V up to a multiple of volblocksize
		bs, _ := strconv.ParseUint(s.volblocksize(n), 10, 64)
		if r := v % bs; r != 0 {
			v += bs - r
		}
		n.typ = zfs.TypeVolume
		n.props["volsize"] = strconv.FormatUint(v, 10)
		if r, set := props["refreservation"]; !set && !sparse || r == "auto" {
			n.props["refreservation"] = autoResv(n.props["volsize"])
		}
	} else if props["refreservation"] == "auto" {
		return exit("cannot create '%s': 'refreservation=auto' only supported on volumes", name)
	}
	s.ds[name] = n
	s.reshare(name)
	return nil
}

func (s *Sim) snapshot(args []string) error {
	props, rest, err := takeOpts(args)
	if err != nil {
		return err
	}
	name := rest[len(rest)-1]
	for k := range props {
		if !strings.Contains(k, ":") {
			return exit("cannot create snapshot '%s': property '%s' can not be set on snapshots", name, k)
		}
	}
	if _, ok := s.ds[parent(name)]; !ok {
		return exit("cannot open '%s': dataset does not exist", parent(name))
	}
	if _, ok := s.ds[name]; ok {
		return exit("cannot create snapshot '%s': dataset already exists", name)
	}
	s.ds[name] = &node{typ: zfs.TypeSnapshot, props: props, birth: s.now()}
	return nil
}

func (s *Sim) clone(args []string) error {
	props, rest, err := takeOpts(args)
	if err != nil {
		return err
	}
	snap, name := rest[0], rest[1]
	src, ok := s.ds[snap]
	if !ok || src.typ != zfs.TypeSnapshot {
		return exit("cannot open '%s': dataset does not exist", snap)
	}
	if _, ok := s.ds[name]; ok {
		return exit("cannot create '%s': dataset already exists", name)
	}
	if _, ok := s.ds[parent(name)]; !ok {
		return exit("cannot create '%s': parent does not exist", name)
	}
	typ := s.ds[parent(snap)].typ
	if _, ok := props["volblocksize"]; ok && typ == zfs.TypeVolume {
		// zfs_valid_proplist with the origin's handle: set-once is readonly
		return exit("cannot create '%s': 'volblocksize' is readonly", name)
	}
	if k, bad := misfit(props, typ); bad {
		return exit("cannot create '%s': '%s' does not apply to datasets of this type", name, k)
	}
	if msg, ok := s.canonProps(props); !ok {
		return exit("cannot create '%s': %s", name, msg)
	}
	if v, ok := props["sharenfs"]; ok && nfsref.ValidateSharenfs(v) != nil {
		return exit("cannot create '%s': 'sharenfs' cannot be set to invalid options", name)
	}
	n := &node{typ: typ, props: props, origin: snap, birth: s.now()}
	if typ == zfs.TypeVolume {
		n.props["volsize"] = s.ds[parent(snap)].props["volsize"]
		if bs, ok := s.ds[parent(snap)].props["volblocksize"]; ok {
			n.props["volblocksize"] = bs
		}
	}
	s.ds[name] = n
	s.reshare(name)
	return nil
}

func (s *Sim) children(name string) []string {
	var out []string
	for k := range s.ds {
		if strings.HasPrefix(k, name+"/") || strings.HasPrefix(k, name+"@") {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func (s *Sim) clonesOf(snap string) []string {
	var out []string
	for k, n := range s.ds {
		if n.origin == snap {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func (s *Sim) destroy(args []string) error {
	var recursive, deferred bool
	var name string
	for _, a := range args {
		switch a {
		case "-r":
			recursive = true
		case "-d":
			deferred = true
		default:
			name = a
		}
	}
	if deferred && !strings.Contains(name, "@") {
		return exit("cannot open '%s': missing '@' delimiter in snapshot name", name)
	}
	n, ok := s.ds[name]
	if !ok {
		if strings.Contains(name, "@") {
			return exit("could not find any snapshots to destroy; check snapshot names.")
		}
		return exit("cannot open '%s': dataset does not exist", name)
	}
	if n.typ == zfs.TypeSnapshot {
		if clones := s.clonesOf(name); len(clones) > 0 {
			if deferred {
				n.defer_ = true
				return nil
			}
			return exit("cannot destroy '%s': snapshot has dependent clones\nuse '-R' to destroy the following datasets:\n%s", name, strings.Join(clones, "\n"))
		}
		s.remove(name)
		return nil
	}
	kids := s.children(name)
	if len(kids) > 0 && !recursive {
		return exit("cannot destroy '%s': filesystem has children\nuse '-r' to destroy the following datasets:\n%s", name, strings.Join(kids, "\n"))
	}
	// -r never destroys clones living outside the tree
	for _, k := range append(kids, name) {
		if s.ds[k].typ != zfs.TypeSnapshot {
			continue
		}
		for _, c := range s.clonesOf(k) {
			if c != name && !strings.HasPrefix(c, name+"/") {
				return exit("cannot destroy '%s': filesystem has dependent clones\nuse '-R' to destroy the following datasets:\n%s", name, c)
			}
		}
	}
	for _, k := range kids {
		s.remove(k)
	}
	s.remove(name)
	return nil
}

// remove deletes a dataset and reaps origins whose deferred destroy is due.
func (s *Sim) remove(name string) {
	origin := s.ds[name].origin
	delete(s.ds, name)
	if s.unshare(name) { // destroy unmounts, and unmount unshares
		s.exportfs()
	}
	if origin == "" {
		return
	}
	if o, ok := s.ds[origin]; ok && o.defer_ && len(s.clonesOf(origin)) == 0 {
		delete(s.ds, origin)
	}
}

func (s *Sim) promote(args []string) error {
	name := args[len(args)-1]
	n, ok := s.ds[name]
	if !ok {
		return exit("cannot open '%s': dataset does not exist", name)
	}
	if n.origin == "" {
		return exit("cannot promote '%s': not a cloned filesystem", name)
	}
	// move the origin snapshot (and older snapshots) under the clone
	oldSnap := n.origin
	oldFS := parent(oldSnap)
	newSnap := name + oldSnap[len(oldFS):]
	s.ds[newSnap] = s.ds[oldSnap]
	delete(s.ds, oldSnap)
	n.origin = ""
	s.ds[oldFS].origin = newSnap
	for k, other := range s.ds {
		if other.origin == oldSnap {
			s.ds[k].origin = newSnap
		}
	}
	return nil
}

func (s *Sim) set(args []string) error {
	noMount := len(args) > 0 && args[0] == "-u"
	if noMount {
		args = args[1:]
	}
	if len(args) < 2 {
		return exit("missing arguments")
	}
	name := args[len(args)-1]
	n, ok := s.ds[name]
	if !ok {
		return exit("cannot open '%s': dataset does not exist", name)
	}
	share := false
	for _, kv := range args[:len(args)-1] {
		p := strings.SplitN(kv, "=", 2)
		if len(p) != 2 {
			return exit("missing '=' for property argument")
		}
		if k, bad := misfit(map[string]string{p[0]: p[1]}, n.typ); bad {
			return exit("cannot set property for '%s': '%s' does not apply to datasets of this type", name, k)
		}
		c, msg, ok := s.canonIndex(p[0], p[1])
		if !ok {
			return exit("cannot set property for '%s': %s", name, msg)
		}
		p[1] = c
		if p[0] == "sharenfs" && nfsref.ValidateSharenfs(p[1]) != nil {
			return exit("cannot set property for '%s': 'sharenfs' cannot be set to invalid options", name)
		}
		if p[0] == "volblocksize" {
			return exit("cannot set property for '%s': 'volblocksize' is readonly", name)
		}
		if p[0] == "volsize" {
			v, err := strconv.ParseUint(p[1], 10, 64)
			bs, _ := strconv.ParseUint(s.volblocksize(n), 10, 64)
			if err != nil || v%bs != 0 {
				return exit("cannot set property for '%s': 'volsize' must be a multiple of volume block size (%d)", name, bs)
			}
		}
		if p[0] == "volsize" && n.props["refreservation"] == autoResv(n.props["volsize"]) {
			// like zfs: an auto refreservation tracks volsize
			n.props["refreservation"] = autoResv(p[1])
		}
		if p[0] == "volsize" && p[1] == "0" {
			return exit("cannot set property for '%s': 'volsize' must be nonzero", name)
		}
		if p[0] == "refreservation" && p[1] == "auto" {
			if n.typ != zfs.TypeVolume {
				return exit("cannot set property for '%s': 'refreservation=auto' only supported on volumes", name)
			}
			p[1] = autoResv(n.props["volsize"])
		}
		n.props[p[0]] = p[1]
		share = share || p[0] == "sharenfs"
	}
	if noMount {
		return nil // -u: no changelist postfix, so no mount and no (re)share
	}
	if c := n.props["canmount"]; share && n.unmounted && c != "off" && c != "noauto" {
		n.unmounted = false // changelist_postfix mounts a canmount=on filesystem
	}
	s.reshare(name)
	return nil
}

func (s *Sim) inherit(args []string) error {
	prop, name := args[0], args[1]
	switch prop {
	case "quota", "refquota", "reservation", "refreservation", "volsize", "volblocksize":
		return exit("'%s' property cannot be inherited", prop)
	}
	n, ok := s.ds[name]
	if !ok {
		return exit("cannot open '%s': dataset does not exist", name)
	}
	delete(n.props, prop)
	s.reshare(name)
	return nil
}

// autoResv approximates zvol_auto_resv: volsize plus metadata overhead (the
// exact overhead depends on the pool; any value > volsize exercises callers)
func autoResv(volsize string) string {
	n, err := strconv.ParseUint(volsize, 10, 64)
	if err != nil || n == 0 {
		return "0"
	}
	return strconv.FormatUint(n+n/64+(1<<20), 10)
}

func (s *Sim) volblocksize(n *node) string {
	if v, ok := n.props["volblocksize"]; ok {
		return v
	}
	return defaults["volblocksize"]
}

// defaults are zfs_prop.c's registered defaults (Linux values where the
// platform matters: relatime on, acltype off).
var defaults = map[string]string{
	"compression": "on", "refquota": "0", "quota": "0", "reservation": "0",
	"refreservation": "0", "volblocksize": "16384", "used": "0",
	"sharenfs": "off", "sharesmb": "off", "canmount": "on",
	"recordsize": "131072", "atime": "on", "relatime": "on", "sync": "standard",
	"logbias": "latency", "dedup": "off", "xattr": "sa", "acltype": "off",
	"snapdir": "hidden", "copies": "1",
}

// applies lists which dataset types a native property exists on (zfs_prop.c);
// for other types zfs get prints "-" with source "-".
var applies = map[string][]zfs.Type{
	"volsize":        {zfs.TypeVolume, zfs.TypeSnapshot},
	"volblocksize":   {zfs.TypeVolume},
	"quota":          {zfs.TypeFilesystem},
	"refquota":       {zfs.TypeFilesystem},
	"reservation":    {zfs.TypeFilesystem, zfs.TypeVolume},
	"refreservation": {zfs.TypeFilesystem, zfs.TypeVolume},
	"sharenfs":       {zfs.TypeFilesystem},
	"sharesmb":       {zfs.TypeFilesystem},
	"mountpoint":     {zfs.TypeFilesystem},
	"mounted":        {zfs.TypeFilesystem},
	"canmount":       {zfs.TypeFilesystem},
	"compression":    {zfs.TypeFilesystem, zfs.TypeVolume},
	"recordsize":     {zfs.TypeFilesystem},
	"atime":          {zfs.TypeFilesystem},
	"relatime":       {zfs.TypeFilesystem},
	"sync":           {zfs.TypeFilesystem, zfs.TypeVolume},
	"logbias":        {zfs.TypeFilesystem, zfs.TypeVolume},
	"dedup":          {zfs.TypeFilesystem, zfs.TypeVolume},
	"copies":         {zfs.TypeFilesystem, zfs.TypeVolume},
	"xattr":          {zfs.TypeFilesystem, zfs.TypeSnapshot},
	"acltype":        {zfs.TypeFilesystem, zfs.TypeSnapshot},
	"snapdir":        {zfs.TypeFilesystem},
	"available":      {zfs.TypeFilesystem, zfs.TypeVolume},
	"defer_destroy":  {zfs.TypeSnapshot},
}

// misfit reports a native property that does not exist on typ
// (libzfs zfs_valid_proplist: "does not apply to datasets of this type").
func misfit(props map[string]string, typ zfs.Type) (string, bool) {
	for k := range props {
		if types, ok := applies[k]; ok && !slices.Contains(types, typ) {
			return k, true
		}
	}
	return "", false
}

func known(prop string) bool {
	if strings.Contains(prop, ":") {
		return true
	}
	switch prop {
	case "all", "name", "type", "origin", "creation", "used":
		return true
	}
	_, ok := applies[prop]
	return ok
}

func (s *Sim) value(name, prop string) (string, string, bool) {
	n := s.ds[name]
	if types, ok := applies[prop]; ok {
		if !slices.Contains(types, n.typ) {
			return "-", "-", true
		}
		if prop == "volsize" && n.typ == zfs.TypeSnapshot && s.ds[parent(name)].typ != zfs.TypeVolume {
			return "-", "-", true
		}
	}
	switch prop {
	case "used":
		return "0", "-", true
	case "name":
		return name, "-", true
	case "type":
		return string(n.typ), "-", true
	case "origin":
		if n.origin == "" {
			return "-", "-", true
		}
		return n.origin, "-", true
	case "available":
		return strconv.FormatUint(s.PoolSize, 10), "-", true
	case "mountpoint":
		if n.typ == zfs.TypeFilesystem {
			if v, ok := n.props[prop]; ok {
				return v, "local", true
			}
			return "/" + name, "default", true
		}
		return "-", "-", true
	case "mounted":
		if s.mounted(name) {
			return "yes", "-", true
		}
		return "no", "-", true
	case "defer_destroy":
		if n.defer_ {
			return "on", "-", true
		}
		return "off", "-", true
	case "creation":
		if n.birth == 0 {
			return "1700000000", "-", true // pools and intermediates
		}
		return strconv.FormatInt(n.birth, 10), "-", true
	case "volblocksize":
		// zvol_get_stats reports the object's block size through
		// dsl_prop_nvlist_add_uint64: source "default" when it equals the
		// default, else none ("-"); never "local"
		v := s.volblocksize(n)
		if v == defaults["volblocksize"] {
			return v, "default", true
		}
		return v, "-", true
	}
	if v, ok := n.props[prop]; ok {
		return v, "local", true
	}
	if v, ok := n.recv[prop]; ok {
		return v, "received", true
	}
	// inherited user properties, and the share properties (PROP_INHERIT)
	if strings.Contains(prop, ":") || prop == "sharenfs" || prop == "sharesmb" {
		for p := parent(name); p != ""; p = parent(p) {
			if v, ok := s.ds[p].props[prop]; ok {
				return v, "inherited from " + p, true
			}
			if v, ok := s.ds[p].recv[prop]; ok {
				return v, "inherited from " + p, true
			}
		}
		if v, ok := defaults[prop]; ok {
			return v, "default", true
		}
		return "-", "-", true
	}
	if prop == "volsize" && n.typ == zfs.TypeSnapshot {
		// a zvol snapshot reports the volume's size at snapshot time
		return s.ds[parent(name)].props["volsize"], "-", true
	}
	if v, ok := defaults[prop]; ok {
		return v, "default", true
	}
	return "", "", false
}

func (s *Sim) get(args []string) ([]byte, error) {
	var types []string
	depth := 0
	recursive := false
	var pos []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-Hp":
		case "-o":
			i++ // columns are fixed to name,property,value,source
		case "-t":
			i++
			types = strings.Split(args[i], ",")
		case "-d":
			i++
			depth, _ = strconv.Atoi(args[i])
		case "-r":
			recursive = true
		default:
			pos = append(pos, args[i])
		}
	}
	props, root := strings.Split(pos[0], ","), pos[1]
	for _, p := range props {
		if !known(p) {
			return nil, exit("bad property list: invalid property '%s'", p)
		}
	}
	if _, ok := s.ds[root]; !ok {
		return nil, exit("cannot open '%s': dataset does not exist", root)
	}
	names := []string{root}
	if recursive || depth > 0 {
		for _, k := range s.children(root) {
			// zfs_iter.c: a dataset's snapshots sit one level below it
			ds, _, isSnap := strings.Cut(k, "@")
			lvl := 0
			if ds != root {
				lvl = strings.Count(strings.TrimPrefix(ds, root+"/"), "/") + 1
			}
			if isSnap {
				lvl++
			}
			if recursive || lvl <= depth {
				names = append(names, k)
			}
		}
	}
	var b strings.Builder
	for _, nm := range names {
		if types != nil && nm != root {
			ok := false
			for _, t := range types {
				ok = ok || string(s.ds[nm].typ) == t
			}
			if !ok {
				continue
			}
		}
		list := props
		if len(props) == 1 && props[0] == "all" {
			list = []string{"type", "origin", "used", "available", "volsize", "refquota", "refreservation", "mountpoint"}
			for k := range s.ds[nm].props {
				list = append(list, k)
			}
		}
		for _, p := range list {
			v, src, ok := s.value(nm, p)
			if !ok {
				continue
			}
			fmt.Fprintf(&b, "%s\t%s\t%s\t%s\n", nm, p, v, src)
		}
	}
	return []byte(b.String()), nil
}

// ---- NFS ----------------------------------------------------------------------

func (s *Sim) mounted(name string) bool {
	n := s.ds[name]
	if n == nil || n.typ != zfs.TypeFilesystem {
		return false
	}
	if c := n.props["canmount"]; c == "off" || c == "noauto" || n.unmounted {
		return false
	}
	mp, _, _ := s.value(name, "mountpoint")
	return mp != "none" && mp != "legacy"
}

// reshare shares or unshares name and the filesystems below it that
// inherit sharenfs (libzfs changelist_postfix), then runs exportfs.
func (s *Sim) reshare(name string) {
	if s.nfsDown {
		return
	}
	changed := false
	for _, k := range append([]string{name}, s.children(name)...) {
		if s.ds[k].typ != zfs.TypeFilesystem {
			continue
		}
		v, src, _ := s.value(k, "sharenfs")
		if k != name && src == "local" {
			continue
		}
		mp, _, _ := s.value(k, "mountpoint")
		if s.mounted(k) && v != "off" {
			if f, err := nfsref.ShareFile(s.exports, mp, v); err == nil {
				s.exports, s.shared[k], changed = f, mp, true
			}
		} else {
			changed = s.unshare(k) || changed
		}
	}
	if changed {
		s.exportfs()
	}
}

// unshare removes name's lines from zfs.exports (nfs_disable_share).
func (s *Sim) unshare(name string) bool {
	mp, ok := s.shared[name]
	if !ok || s.nfsDown {
		return false
	}
	s.exports, _ = nfsref.ShareFile(s.exports, mp, "off")
	delete(s.shared, name)
	return true
}

// exportfs is `exportfs -ra` on zfs.exports, leaving out DropHost
// clients.
func (s *Sim) exportfs() {
	etab, _ := nfsref.ExportfsRA(nfsref.ExportsFiles{Dir: map[string][]byte{"zfs.exports": []byte(s.exports)}}, nil, realpath)
	var b strings.Builder
	for _, line := range strings.SplitAfter(etab, "\n") {
		if _, rest, ok := strings.Cut(line, "\t"); ok {
			if host, _, _ := strings.Cut(rest, "("); s.dropHosts[host] {
				continue
			}
		}
		b.WriteString(line)
	}
	s.etab = b.String()
}

// realpath is nfsd_realpath: the path itself when it does not resolve.
func realpath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// SetNFSDown makes later sharenfs changes not reach the NFS server, as when
// exportfs is unavailable (libshare nfs_available is false): libzfs then
// leaves zfs.exports alone and exports nothing, and zfs set still
// succeeds. Turning it off again does not replay missed changes; the next
// share of a dataset does (exportfs -ra reads the whole file). A sharenfs
// re-set to "off" on a dataset zfs.exports has no line for does not run
// exportfs (changelist_postfix unshares only cn_shared datasets).
func (s *Sim) SetNFSDown(down bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nfsDown = down
}

// Unmount unmounts a filesystem the way `zfs unmount` does: unshare, then
// unmount; canmount stays on (test helper).
func (s *Sim) Unmount(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ds[name].unmounted = true
	if s.unshare(name) {
		s.exportfs()
	}
}

// SetNFSDRunning stops or starts the NFS server. Stopping does what
// nfs-server.service's ExecStopPost does (exportfs -au: the table is
// emptied) and leaves no nfsd threads. A sharenfs change while stopped still
// rewrites the table, because exportfs -ra does not check for nfsd.
// Starting re-exports every shared, mounted dataset (exportfs -r).
func (s *Sim) SetNFSDRunning(running bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nfsdOff = !running
	if !running {
		s.etab = "" // exportfs -au
		return
	}
	s.exportfs() // exportfs -r
}

// DropHost makes exportfs leave client host out of the table while it
// applies the rest, as nfs-utils does with a client it cannot use (an
// unresolvable name, a duplicate line).
func (s *Sim) DropHost(host string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dropHosts == nil {
		s.dropHosts = map[string]bool{}
	}
	s.dropHosts[host] = true
}

// NFSDThreads reports the nfsd thread count (/proc/fs/nfsd/threads). It
// implements the endpoint's NFSDReader.
func (s *Sim) NFSDThreads(context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nfsdOff {
		return 0, nil
	}
	return 8, nil
}

// ReadEtab returns /var/lib/nfs/etab as the last successful exportfs
// wrote it. It implements the endpoint's EtabReader.
func (s *Sim) ReadEtab(context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return []byte(s.etab), nil
}

// Exports returns /etc/exports.d/zfs.exports as libzfs last wrote it.
func (s *Sim) Exports() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exports
}
