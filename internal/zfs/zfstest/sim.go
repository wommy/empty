// Package zfstest simulates the zfs(8) CLI closely enough to test callers
// without a pool: dataset tree, snapshots, clone/origin graph, user holds,
// user and native properties with local/received/inherited sources, mount
// state, and the stderr text real zfs prints for the failures callers must
// classify (see internal/zfs/errors.go for the OpenZFS sources).
//
// It is an oracle for control flow, not for ZFS itself. Behaviors were
// audited against OpenZFS 2.4.4 source (docs/research/simfidelity/) and
// are differential-tested against an independent reference model
// (internal/zfs/zfsdiff), which CI also replays on a real pool. Not
// modelled: rename, destroy -R, -n, busy (open) zvols, size suffixes,
// bookmarks; space accounting counts reservations only (an empty
// dataset references nothing).
//
// Mounts follow zfs: create, clone and receive mount a canmount=on
// filesystem (create -u does not); after that only the changelist rules
// move it: a mountpoint change remounts, canmount=off unmounts,
// canmount=on/noauto change nothing, and setting or inheriting a share
// property mounts an unmounted canmount=on filesystem whose share ends up
// on (changelist_postfix). `zfs set -u` does neither. Unmount unmounts.
//
// NFS: sharing a mounted filesystem rewrites /etc/exports.d/zfs.exports
// like libzfs and runs `exportfs -ra`; both are the reference models in
// internal/nfsref (OpenZFS libshare and nfs-utils, derived from their
// sources), so etab shows what a real host shows: "*" for an empty host,
// '@' dropped, crossmnt for "on", first-wins duplicates, client classes
// in mountd order, a malformed line ending the file, realpath. An invalid
// sharenfs is refused like `zfs set` refuses it. Failure modes, each with
// zfs still exiting 0:
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
	"maps"
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
	inh    map[string]bool   // prop$inherit: an explicit inherit hides recv
	origin string            // clone origin snapshot
	defer_ bool              // deferred destroy pending
	holds  map[string]bool   // user holds (snapshots): tag -> held
	birth  int64             // creation, unix seconds
	txg    uint64            // snapshots: creation order
	// mounted: filesystems only. zfs create/clone/recv mount a
	// canmount=on filesystem; later only the changelist rules move it
	// (see mount).
	mounted bool
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
	txg       uint64
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
		s.ds[p] = &node{typ: zfs.TypeFilesystem, props: map[string]string{}, mounted: true}
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
	case "hold":
		return nil, s.hold(args[1:], true)
	case "release":
		return nil, s.hold(args[1:], false)
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
// replication stream does (test helper). A local value still wins, and
// the dataset is marked as received ($hasrecvd), so a later plain
// `zfs inherit` hides the received value (use `zfs inherit -S` to
// revert to it).
func (s *Sim) Receive(name, prop, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.ds[name]
	if n.recv == nil {
		n.recv = map[string]string{}
	}
	n.recv[prop] = value
}

// SendRecv models `zfs send -p snap | zfs recv dst` (test helper): dst is
// a new dataset like snap's, with the head's local and received
// properties received, plus dst@<snapname>; a canmount=on filesystem is
// mounted.
func (s *Sim) SendRecv(snap, dst string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	src, ok := s.ds[snap]
	if !ok || src.typ != zfs.TypeSnapshot {
		return exit("cannot open '%s': dataset does not exist", snap)
	}
	if _, ok := s.ds[dst]; ok {
		return exit("cannot receive new filesystem stream: destination '%s' exists\nmust specify -F to overwrite it", dst)
	}
	if _, ok := s.ds[parent(dst)]; !ok {
		return exit("cannot receive new filesystem stream: destination '%s' does not exist", parent(dst))
	}
	head := s.ds[parent(snap)]
	n := &node{typ: head.typ, props: map[string]string{}, recv: map[string]string{}, birth: s.now()}
	for k, v := range head.recv {
		if !head.inh[k] {
			n.recv[k] = v
		}
	}
	for k, v := range head.props {
		switch k {
		case "volsize", "volblocksize":
			n.props[k] = v
		default:
			n.recv[k] = v
		}
	}
	if v, ok := src.props["volsize"]; ok {
		n.props["volsize"] = v
	}
	s.ds[dst] = n
	s.txg++
	sn := &node{typ: zfs.TypeSnapshot, props: map[string]string{}, birth: s.now(), txg: s.txg}
	if v, ok := n.props["volsize"]; ok {
		sn.props["volsize"] = v
	}
	s.ds[dst+snap[strings.IndexByte(snap, '@'):]] = sn
	s.mountNew(dst)
	s.reshare(dst)
	return nil
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
			if _, dup := props[kv[0]]; dup {
				// zfs_main.c parseprop: ret -1, exit 255
				return nil, nil, &zfs.ExitError{Code: 255, Stderr: fmt.Sprintf("property '%s' specified multiple times\n", kv[0])}
			}
			props[kv[0]] = kv[1]
			i++
			continue
		}
		rest = append(rest, args[i])
	}
	return props, rest, nil
}

// spaceProps are the number properties where "none" means 0
// (zprop_parse_value, libzfs_util.c).
var spaceProps = map[string]bool{"quota": true, "refquota": true, "reservation": true, "refreservation": true}

// parseValue applies zprop_parse_value's rules the simulator models:
// "none" is 0 for the space properties, quota/refquota=0 must be spelled
// "none", index values are stored canonical, block sizes are powers of 2.
func (s *Sim) parseValue(prop, value string) (string, string, bool) {
	if spaceProps[prop] {
		if value == "none" {
			return "0", "", true
		}
		if value == "0" && (prop == "quota" || prop == "refquota") {
			return "", "use 'none' to disable quota/refquota", false
		}
	}
	if prop == "volblocksize" || prop == "recordsize" {
		n, err := strconv.ParseUint(value, 10, 64)
		if err != nil || n < 512 || n > 16<<20 || n&(n-1) != 0 {
			return "", fmt.Sprintf("'%s' must be power of 2 from 512B to 16M", prop), false
		}
	}
	c, msg, ok := s.canonIndex(prop, value)
	if !ok {
		return "", msg, false
	}
	if prop == "sharenfs" && nfsref.ValidateSharenfs(c) != nil {
		return "", "'sharenfs' cannot be set to invalid options", false
	}
	return c, "", true
}

// parseProps runs parseValue over props in place.
func (s *Sim) parseProps(props map[string]string) (string, bool) {
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v, msg, ok := s.parseValue(k, props[k])
		if !ok {
			return msg, false
		}
		props[k] = v
	}
	return "", true
}

func (s *Sim) create(args []string) error {
	props, rest, err := takeOpts(args)
	if err != nil {
		return err
	}
	var parents, sparse, noMount bool
	var volsize string
	var name string
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "-u":
			noMount = true // zfs create -u: do not mount
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
		return &zfs.ExitError{Code: 2, Stderr: "'-s' and '-b' can only be used when creating a volume\n"}
	}
	typ := zfs.TypeFilesystem
	if volsize != "" {
		typ = zfs.TypeVolume
		// zfs_main.c zfs_do_create validates a volume's properties before
		// anything else
		if k, bad := misfit(props, typ); bad {
			return exit("cannot create '%s': '%s' does not apply to datasets of this type", name, k)
		}
		if msg, ok := s.parseProps(props); !ok {
			return exit("cannot create '%s': %s", name, msg)
		}
	}
	if old, ok := s.ds[name]; ok {
		if parents && old.typ == typ {
			// real zfs: -p on an existing dataset of the same type
			// succeeds silently (zfs_dataset_exists(name, type))
			return nil
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
		if s.ds[parent(chain[0])].typ != zfs.TypeFilesystem {
			return exit("cannot create '%s': parent is not a filesystem", name)
		}
		for _, q := range chain {
			s.ds[q] = &node{typ: zfs.TypeFilesystem, props: map[string]string{}, birth: s.now()}
			s.mountNew(q)
		}
	}
	if s.ds[p].typ != zfs.TypeFilesystem {
		return exit("cannot create '%s': parent is not a filesystem", name)
	}
	if typ == zfs.TypeFilesystem {
		if k, bad := misfit(props, typ); bad {
			return exit("cannot create '%s': '%s' does not apply to datasets of this type", name, k)
		}
		if props["refreservation"] == "auto" {
			return exit("cannot create '%s': 'refreservation=auto' only allowed on volumes", name)
		}
		if msg, ok := s.parseProps(props); !ok {
			return exit("cannot create '%s': %s", name, msg)
		}
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
		// AMBIGUOUS: 2.4.4 has no zfs_create_fix_auto_resv, so "auto"
		// reaches the kernel as UINT64_MAX (ENOSPC); master resolves it.
		// testdata/zfsdiff/create-vol-resv-auto.txt settles it on a pool.
		if r, set := props["refreservation"]; !set && !sparse || r == "auto" {
			n.props["refreservation"] = autoResv(n.props["volsize"], s.volblocksize(n))
		}
	}
	if msg := s.createSpace(name, n); msg != "" {
		return exit("cannot create '%s': %s", name, msg)
	}
	s.ds[name] = n
	if !noMount {
		s.mountNew(name)
	}
	s.reshare(name)
	return nil
}

func (s *Sim) snapshot(args []string) error {
	props, rest, err := takeOpts(args)
	if err != nil {
		return err
	}
	recursive := len(rest) > 1 && rest[0] == "-r"
	name := rest[len(rest)-1]
	if _, ok := s.ds[parent(name)]; !ok {
		// zfs_do_snapshot: a dataset it cannot open is a usage error
		return &zfs.ExitError{Code: 2, Stderr: fmt.Sprintf("cannot open '%s': dataset does not exist\nusage:\n\tsnapshot [-r] [-o property=value] ... <filesystem|volume>@<snap> ...\n", parent(name))}
	}
	for k := range props {
		if !strings.Contains(k, ":") {
			// zfs_valid_proplist: only user properties on snapshots
			return exit("cannot create snapshots : this property can not be modified for snapshots")
		}
	}
	head, short, _ := strings.Cut(name, "@")
	names := []string{name}
	if recursive {
		for _, k := range s.children(head) {
			if !strings.Contains(k, "@") {
				names = append(names, k+"@"+short)
			}
		}
	}
	// lzc_snapshot is atomic: one existing name fails them all
	var errs []string
	for _, k := range names {
		if _, ok := s.ds[k]; ok {
			errs = append(errs, fmt.Sprintf("cannot create snapshot '%s': dataset already exists", k))
		}
	}
	if len(errs) > 0 {
		if len(names) > 1 {
			errs = append(errs, "no snapshots were created")
		}
		return exit("%s", strings.Join(errs, "\n"))
	}
	for _, k := range names {
		s.txg++
		n := &node{typ: zfs.TypeSnapshot, props: maps.Clone(props), birth: s.now(), txg: s.txg}
		if v, ok := s.ds[parent(k)].props["volsize"]; ok {
			// a zvol snapshot keeps the volume's size at snapshot time
			n.props["volsize"] = v
		}
		s.ds[k] = n
	}
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
	if props["refreservation"] == "auto" && typ != zfs.TypeVolume {
		return exit("cannot create '%s': 'refreservation=auto' only allowed on volumes", name)
	}
	if msg, ok := s.parseProps(props); !ok {
		return exit("cannot create '%s': %s", name, msg)
	}
	if _, ok := s.ds[name]; ok {
		return exit("cannot create '%s': dataset already exists", name)
	}
	n := &node{typ: typ, props: props, origin: snap, birth: s.now()}
	if typ == zfs.TypeVolume {
		n.props["volsize"] = src.props["volsize"]
		if bs, ok := s.ds[parent(snap)].props["volblocksize"]; ok {
			n.props["volblocksize"] = bs
		}
		if props["refreservation"] == "auto" {
			// zfs_clone: zfs_fix_auto_resv with the snapshot's volsize
			n.props["refreservation"] = autoResv(n.props["volsize"], s.volblocksize(n))
		}
	}
	if msg := s.createSpace(name, n); msg != "" {
		return exit("cannot create '%s': %s", name, msg)
	}
	s.ds[name] = n
	s.mountNew(name)
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
		if len(a) > 1 && a[0] == '-' {
			for _, f := range a[1:] {
				switch f {
				case 'r':
					recursive = true
				case 'd':
					deferred = true
				default:
					return &zfs.ExitError{Code: 2, Stderr: fmt.Sprintf("invalid option '%c'\n", f)}
				}
			}
			continue
		}
		name = a
	}
	if deferred && !strings.Contains(name, "@") {
		return exit("cannot open '%s': missing '@' delimiter in snapshot name", name)
	}
	if head, short, isSnap := strings.Cut(name, "@"); isSnap {
		// zfs destroy fs@snap: gather fs@snap (and with -r every
		// descendant's), refuse clones unless deferring, then destroy all
		// in one atomic lzc_destroy_snaps
		if _, ok := s.ds[head]; !ok {
			return exit("cannot open '%s': dataset does not exist", head)
		}
		var snaps []string
		if _, ok := s.ds[name]; ok {
			snaps = append(snaps, name)
		}
		if recursive {
			for _, k := range s.children(head) {
				if parent(k) != head && strings.HasSuffix(k, "@"+short) && !strings.Contains(strings.TrimSuffix(k, "@"+short), "@") {
					snaps = append(snaps, k)
				}
			}
		}
		if len(snaps) == 0 {
			return exit("could not find any snapshots to destroy; check snapshot names.")
		}
		if !deferred {
			var errs []string
			for _, k := range snaps {
				if clones := s.clonesOf(k); len(clones) > 0 {
					errs = append(errs, fmt.Sprintf("cannot destroy '%s': snapshot has dependent clones\nuse '-R' to destroy the following datasets:\n%s", k, strings.Join(clones, "\n")))
				}
			}
			if len(errs) > 0 {
				return exit("%s", strings.Join(errs, "\n"))
			}
			for _, k := range snaps {
				if len(s.ds[k].holds) > 0 {
					errs = append(errs, held(k))
				}
			}
			if len(errs) > 0 {
				return exit("%s", strings.Join(errs, "\n"))
			}
		}
		for _, k := range snaps {
			if deferred && (len(s.ds[k].holds) > 0 || len(s.clonesOf(k)) > 0) {
				s.ds[k].defer_ = true
				continue
			}
			s.remove(k)
		}
		return nil
	}
	n, ok := s.ds[name]
	if !ok {
		return exit("cannot open '%s': dataset does not exist", name)
	}
	kids := s.children(name)
	if len(kids) > 0 && !recursive {
		// destroy_check_dependent lists every dependent below the target
		return exit("cannot destroy '%s': %s has children\nuse '-r' to destroy the following datasets:\n%s", name, n.typ, strings.Join(kids, "\n"))
	}
	// -r never destroys clones living outside the tree
	for _, k := range append(kids, name) {
		if s.ds[k].typ != zfs.TypeSnapshot {
			continue
		}
		for _, c := range s.clonesOf(k) {
			if c != name && !strings.HasPrefix(c, name+"/") {
				return exit("cannot destroy '%s': %s has dependent clones\nuse '-R' to destroy the following datasets:\n%s", name, n.typ, c)
			}
		}
	}
	// zfs destroy -r walks the dependents children first (zfs_iter_dependents)
	// and keeps going after an error: each dataset's snapshots go in one
	// atomic batch (a held one fails the batch), then the dataset, which
	// fails while it still has snapshots (EBUSY) or children (EEXIST).
	heads := []string{name}
	for _, k := range kids {
		if !strings.Contains(k, "@") {
			heads = append(heads, k)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(heads)))
	// zfs_iter_dependents visits a snapshot's clones before the snapshot;
	// repeating the children-first pass until nothing changes reaches the
	// same end state for clones that live inside the tree
	var errs []string
	for progress := true; progress; {
		progress, errs = false, nil
		for _, h := range heads {
			if _, ok := s.ds[h]; !ok {
				continue
			}
			var snaps []string
			for _, k := range s.children(h) {
				if parent(k) == h && strings.Contains(k, "@") {
					snaps = append(snaps, k)
				}
			}
			var batch []string
			for _, k := range snaps {
				switch {
				case len(s.ds[k].holds) > 0:
					batch = append(batch, held(k))
				case len(s.clonesOf(k)) > 0:
					batch = append(batch, fmt.Sprintf("cannot destroy snapshot %s: snapshot is cloned", k))
				}
			}
			if len(batch) > 0 {
				errs = append(errs, batch...)
			} else {
				for _, k := range snaps {
					s.remove(k)
					progress = true
				}
			}
			left := s.children(h)
			switch {
			case slices.ContainsFunc(left, func(k string) bool { return parent(k) == h && strings.Contains(k, "@") }):
				errs = append(errs, fmt.Sprintf("cannot destroy '%s': dataset is busy", h))
			case len(left) > 0:
				errs = append(errs, fmt.Sprintf("cannot destroy '%s': dataset already exists", h))
			default:
				s.remove(h)
				progress = true
			}
		}
	}
	if len(errs) > 0 {
		return exit("%s", strings.Join(errs, "\n"))
	}
	return nil
}

// remove deletes a dataset and reaps origins whose deferred destroy is due.
func (s *Sim) remove(name string) {
	n, ok := s.ds[name]
	if !ok {
		return // already reaped as a deferred origin earlier in a -r walk
	}
	origin := n.origin
	delete(s.ds, name)
	if s.unshare(name) { // destroy unmounts, and unmount unshares
		s.exportfs()
	}
	if origin == "" {
		return
	}
	if o, ok := s.ds[origin]; ok && o.defer_ && len(s.clonesOf(origin)) == 0 && len(o.holds) == 0 {
		delete(s.ds, origin)
	}
}

// spaceCheck is the kernel's check for setting a space property on name
// (dsl_dataset.c dsl_dataset_set_refquota_check and
// dsl_dataset_set_refreservation_check, dsl_dir.c dsl_dir_set_quota_check
// and dsl_dir_set_reservation_check), with referenced space taken as 0.
// It returns zfs_setprop_error's ENOSPC text, or "".
func (s *Sim) spaceCheck(name, prop, value string) string {
	n := s.ds[name]
	nv, _ := strconv.ParseUint(value, 10, 64)
	get := func(p string) uint64 {
		v, _ := strconv.ParseUint(n.props[p], 10, 64)
		return v
	}
	const less, more = "size is less than current used or reserved space", "size is greater than available space"
	switch prop {
	case "refquota":
		if nv != 0 && nv < get("refreservation") {
			return less
		}
	case "quota":
		if nv != 0 && (nv < get("reservation") || nv < s.dirUsed(name)) {
			return less
		}
	case "refreservation", "reservation":
		limit := "refquota"
		if prop == "reservation" {
			limit = "quota"
		}
		if cur := get(prop); nv > cur && (nv-cur > s.available(name) || get(limit) > 0 && nv > get(limit)) {
			return more
		}
	}
	return ""
}

// createSpace checks the reservations a new dataset sets (the kernel sets
// them after creating it and destroys it again on failure: ENOSPC is
// "out of space").
func (s *Sim) createSpace(name string, n *node) string {
	s.ds[name] = n
	defer delete(s.ds, name)
	for _, p := range []string{"refquota", "quota", "refreservation", "reservation"} {
		v, ok := n.props[p]
		if !ok {
			continue
		}
		delete(n.props, p)
		msg := s.spaceCheck(name, p, v)
		n.props[p] = v
		if msg != "" {
			return "out of space"
		}
	}
	return ""
}

func (s *Sim) num(name, prop string) uint64 {
	v, _ := strconv.ParseUint(s.ds[name].props[prop], 10, 64)
	return v
}

// dirUsed approximates a dataset's dd_used_bytes with empty datasets: the
// refreservations of the heads under it and its descendants'
// reservations.
func (s *Sim) dirUsed(name string) uint64 {
	var u uint64
	for _, k := range append([]string{name}, s.children(name)...) {
		if s.ds[k].typ == zfs.TypeSnapshot {
			continue
		}
		u += s.num(k, "refreservation")
		if k != name {
			u += s.num(k, "reservation")
		}
	}
	return u
}

// available approximates dsl_dir_space_available: the pool's free space
// less every reservation, capped by each ancestor's quota.
func (s *Sim) available(name string) uint64 {
	var used uint64
	for k, n := range s.ds {
		if n.typ != zfs.TypeSnapshot {
			used += s.num(k, "refreservation") + s.num(k, "reservation")
		}
	}
	avail := uint64(0)
	if used < s.PoolSize {
		avail = s.PoolSize - used
	}
	for p := name; p != ""; p = parent(p) {
		if q := s.num(p, "quota"); q > 0 {
			u := s.dirUsed(p)
			if q < u {
				return 0
			}
			avail = min(avail, q-u)
		}
	}
	return avail
}

// held is zfs_destroy_snaps_nvl's EBUSY text for a user hold.
func held(snap string) string {
	return fmt.Sprintf("cannot destroy snapshot %s: it's being held. Run 'zfs holds -r %s' to see holders.", snap, snap)
}

// hold runs `zfs hold [-r] tag snap` or `zfs release [-r] tag snap`
// (zfs_main.c zfs_do_hold_rele_impl, libzfs zfs_hold/zfs_release). A
// release of the last hold on a deferred snapshot with no clones destroys
// it (dsl_userhold.c).
func (s *Sim) hold(args []string, holding bool) error {
	recursive := len(args) > 0 && args[0] == "-r"
	if recursive {
		args = args[1:]
	}
	if len(args) != 2 {
		return &zfs.ExitError{Code: 2, Stderr: "usage\n"}
	}
	tag, name := args[0], args[1]
	ds, snap, ok := strings.Cut(name, "@")
	if !ok {
		return exit("'%s' is not a snapshot", name)
	}
	if _, ok := s.ds[ds]; !ok {
		return exit("cannot open '%s': dataset does not exist", ds)
	}
	heads := []string{ds}
	if recursive {
		for _, k := range s.children(ds) {
			if !strings.Contains(k, "@") {
				heads = append(heads, k)
			}
		}
	}
	var snaps []*node
	for _, h := range heads {
		if n, ok := s.ds[h+"@"+snap]; ok {
			snaps = append(snaps, n)
		}
	}
	op := "hold snapshot"
	if !holding {
		op = "release hold from snapshot"
	}
	if len(snaps) == 0 {
		return exit("cannot %s '%s': dataset does not exist", op, name)
	}
	if holding {
		for i, n := range snaps {
			if n.holds[tag] {
				return exit("cannot hold snapshot '%s': tag already exists on this dataset", heads[i]+"@"+snap)
			}
		}
	} else {
		// zfs_release: the snapshots holding tag are released; when there
		// are none, the last problem seen (depth first) is reported
		why, any := "", false
		for _, h := range heads {
			n, ok := s.ds[h+"@"+snap]
			switch {
			case !ok:
				why = "dataset does not exist"
			case !n.holds[tag]:
				why = "no such tag on this dataset"
			default:
				any = true
			}
		}
		if !any {
			return exit("cannot release hold from snapshot '%s': %s", name, why)
		}
	}
	for _, h := range heads {
		k := h + "@" + snap
		n, ok := s.ds[k]
		if !ok {
			continue
		}
		if holding {
			if n.holds == nil {
				n.holds = map[string]bool{}
			}
			n.holds[tag] = true
			continue
		}
		if !n.holds[tag] {
			continue
		}
		delete(n.holds, tag)
		if n.defer_ && len(n.holds) == 0 && len(s.clonesOf(k)) == 0 {
			s.remove(k)
		}
	}
	return nil
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
	// dsl_dataset_promote: the origin's dataset hands every snapshot up
	// to and including the origin to the clone, which takes over the
	// origin's own origin; the old dataset becomes a clone of the moved
	// origin snapshot. A snapshot name the clone already has is EEXIST.
	origin := n.origin
	oldFS := parent(origin)
	var moved []string
	for _, k := range s.children(oldFS) {
		if parent(k) == oldFS && strings.Contains(k, "@") && s.ds[k].txg <= s.ds[origin].txg {
			moved = append(moved, k)
		}
	}
	for _, k := range moved {
		if _, ok := s.ds[name+k[len(oldFS):]]; ok {
			return exit("cannot promote '%s': conflicting snapshot '%s' from parent '%s'", name, k[len(oldFS)+1:], origin)
		}
	}
	for _, k := range moved {
		nk := name + k[len(oldFS):]
		s.ds[nk] = s.ds[k]
		delete(s.ds, k)
		for _, c := range s.ds {
			if c.origin == k {
				c.origin = nk
			}
		}
	}
	n.origin, s.ds[oldFS].origin = s.ds[oldFS].origin, name+origin[len(oldFS):]
	return nil
}

func (s *Sim) set(args []string) error {
	noMount := len(args) > 0 && args[0] == "-u"
	if noMount {
		args = args[1:]
	}
	if len(args) < 2 {
		return &zfs.ExitError{Code: 2, Stderr: "missing arguments\n"}
	}
	name := args[len(args)-1]
	seen := map[string]bool{}
	for _, kv := range args[:len(args)-1] {
		p := strings.SplitN(kv, "=", 2)
		if len(p) != 2 {
			return &zfs.ExitError{Code: 2, Stderr: "missing '=' for property argument\n"}
		}
		if seen[p[0]] {
			return &zfs.ExitError{Code: 255, Stderr: fmt.Sprintf("property '%s' specified multiple times\n", p[0])}
		}
		seen[p[0]] = true
	}
	n, ok := s.ds[name]
	if !ok {
		return exit("cannot open '%s': dataset does not exist", name)
	}
	type kv struct{ k, v string }
	var list []kv
	for _, a := range args[:len(args)-1] {
		p := strings.SplitN(a, "=", 2)
		if n.typ == zfs.TypeSnapshot && !strings.Contains(p[0], ":") {
			return exit("cannot set property for '%s': this property can not be modified for snapshots", name)
		}
		if k, bad := misfit(map[string]string{p[0]: p[1]}, n.typ); bad {
			return exit("cannot set property for '%s': '%s' does not apply to datasets of this type", name, k)
		}
		if p[0] == "volblocksize" {
			return exit("cannot set property for '%s': 'volblocksize' is readonly", name)
		}
		if p[0] == "refreservation" && p[1] == "auto" {
			if n.typ != zfs.TypeVolume {
				return exit("cannot set property for '%s': 'refreservation=auto' only allowed on volumes", name)
			}
			list = append(list, kv{p[0], p[1]})
			continue
		}
		c, msg, ok := s.parseValue(p[0], p[1])
		if !ok {
			return exit("cannot set property for '%s': %s", name, msg)
		}
		if p[0] == "volsize" {
			v, err := strconv.ParseUint(c, 10, 64)
			bs, _ := strconv.ParseUint(s.volblocksize(n), 10, 64)
			if err != nil || v%bs != 0 {
				return exit("cannot set property for '%s': 'volsize' must be a multiple of volume block size (%dK)", name, bs>>10)
			}
			if v == 0 {
				return exit("cannot set property for '%s': 'volsize' cannot be zero", name)
			}
		}
		list = append(list, kv{p[0], c})
	}
	// zfs_add_synthetic_resv: a thick volume's refreservation tracks
	// volsize unless refreservation is in the same set
	resvSet := false
	for _, p := range list {
		resvSet = resvSet || p.k == "refreservation"
	}
	synthetic := false
	for i, p := range list {
		// zfs_prop_get_int: the effective value, whatever its source
		if cur, _, _ := s.value(name, "refreservation"); p.k == "volsize" && !resvSet && cur == autoResv(n.props["volsize"], s.volblocksize(n)) {
			list = append(list, kv{"refreservation", autoResv(p.v, s.volblocksize(n))})
			synthetic = true
		}
		if p.k == "refreservation" && p.v == "auto" {
			vs := n.props["volsize"]
			for _, q := range list {
				if q.k == "volsize" {
					vs = q.v
				}
			}
			list[i].v = autoResv(vs, s.volblocksize(n))
		}
	}
	var cls []string
	for _, p := range list {
		switch {
		case noMount || n.typ != zfs.TypeFilesystem:
		case p.k == "mountpoint" || p.k == "sharenfs" || p.k == "sharesmb":
			cls = append(cls, p.k)
		case p.k == "canmount" && p.v == "off":
			// only canmount=off goes through the changelist, and it
			// unmounts (zfs_prop_set_list_flags)
			n.mounted = false
		}
	}
	was := map[string]map[string]bool{}
	for _, c := range cls {
		was[c] = s.prefix(name, c)
	}
	// zfs_set_prop_nvlist: the space properties are set one by one in
	// order, each checked by its sync task; a failure is reported per
	// property and the rest still apply (no changelist postfix then)
	oldVolsize := n.props["volsize"]
	var errs []string
	for _, p := range list {
		if msg := s.spaceCheck(name, p.k, p.v); msg != "" {
			errs = append(errs, fmt.Sprintf("cannot set property for '%s': %s", name, msg))
			if synthetic && p.k == "refreservation" {
				n.props["volsize"] = oldVolsize // libzfs puts volsize back
			}
			continue
		}
		n.props[p.k] = p.v
		delete(n.inh, p.k)
	}
	if len(errs) > 0 {
		return exit("%s", strings.Join(errs, "\n"))
	}
	for _, c := range cls {
		s.postfix(c, was[c])
	}
	if noMount {
		return nil // -u: no changelist postfix, so no mount and no (re)share
	}
	s.reshare(name)
	return nil
}

func (s *Sim) inherit(args []string) error {
	var recursive, received bool
	for len(args) > 0 && len(args[0]) > 1 && args[0][0] == '-' {
		for _, f := range args[0][1:] {
			switch f {
			case 'r':
				recursive = true
			case 'S':
				received = true
			default:
				return &zfs.ExitError{Code: 2, Stderr: fmt.Sprintf("invalid option '%c'\n", f)}
			}
		}
		args = args[1:]
	}
	if len(args) < 2 {
		return &zfs.ExitError{Code: 2, Stderr: "missing property or dataset argument\n"}
	}
	prop, name := args[0], args[1]
	// zfs_do_inherit checks the property before opening anything
	switch {
	case strings.Contains(prop, ":"):
	case readonlyProps[prop]:
		return exit("%s property is read-only", prop)
	case !inheritable[prop] && prop != "mountpoint" && !received && known(prop):
		msg := fmt.Sprintf("'%s' property cannot be inherited", prop)
		if spaceProps[prop] {
			msg += fmt.Sprintf("\nuse 'zfs set %s=none' to clear\nuse 'zfs inherit -S %s' to revert to received value", prop, prop)
		}
		return exit("%s", msg)
	case received && prop == "volsize":
		return exit("'volsize' property cannot be reverted to a received value")
	case !known(prop):
		return &zfs.ExitError{Code: 2, Stderr: fmt.Sprintf("invalid property '%s'\nusage:\n\tinherit [-rS] <property> <filesystem|volume|snapshot> ...\n", prop)}
	}
	n, ok := s.ds[name]
	if !ok {
		return exit("cannot open '%s': dataset does not exist", name)
	}
	if _, bad := misfit(map[string]string{prop: ""}, n.typ); bad {
		return exit("cannot inherit %s for '%s': property doesn't apply to datasets of this type", prop, name)
	}
	targets := []string{name}
	if recursive {
		targets = append(targets, s.children(name)...)
	}
	for _, t := range targets {
		tn := s.ds[t]
		if _, bad := misfit(map[string]string{prop: ""}, tn.typ); bad {
			continue
		}
		cl := tn.typ == zfs.TypeFilesystem && (prop == "mountpoint" || prop == "sharenfs" || prop == "sharesmb")
		var was map[string]bool
		if cl {
			was = s.prefix(t, prop)
		}
		delete(tn.props, prop)
		// dsl_prop_set_sync_impl: a plain inherit on a received dataset
		// writes prop$inherit, which hides the received value; -S
		// removes it and so reverts to the received value
		if received {
			delete(tn.inh, prop)
		} else if s.hasRecvd(t) {
			if tn.inh == nil {
				tn.inh = map[string]bool{}
			}
			tn.inh[prop] = true
		}
		if cl {
			s.postfix(prop, was)
		}
	}
	s.reshare(name)
	return nil
}

// readonlyProps are the properties zfs_prop_readonly reports (PROP_READONLY
// and the set-once PROP_ONETIME ones) among those the simulator knows.
var readonlyProps = map[string]bool{
	"type": true, "origin": true, "mounted": true, "defer_destroy": true, "userrefs": true,
	"used": true, "available": true, "creation": true, "name": true, "volblocksize": true,
}

// hasRecvd reports $hasrecvd, which zfs recv sets and the kernel looks up
// like an inheritable property (dsl_prop.c dsl_prop_get_int_ds).
func (s *Sim) hasRecvd(name string) bool {
	if i := strings.IndexByte(name, '@'); i >= 0 {
		name = name[:i]
	}
	for p := name; p != ""; p = parent(p) {
		if n := s.ds[p]; n != nil && n.recv != nil {
			return true
		}
	}
	return false
}

// autoResv is zvol_volsize_to_reservation (libzfs_dataset.c) for a pool of
// plain or mirror vdevs, with ncopies 1: volsize plus two copies of every
// indirect block (128K, 1024 pointers each) and 7 dnode blocks.
func autoResv(volsize, volblocksize string) string {
	n, err := strconv.ParseUint(volsize, 10, 64)
	bs, err2 := strconv.ParseUint(volblocksize, 10, 64)
	if err != nil || err2 != nil || n == 0 || bs == 0 {
		return "0"
	}
	nblocks := n / bs
	numdb := uint64(7)
	for nb := nblocks; nb > 1; {
		nb = (nb + 1023) / 1024
		numdb += nb
	}
	return strconv.FormatUint(nblocks*bs+numdb*2*(1<<17), 10)
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

// inheritable are the PROP_INHERIT properties the simulator models
// (zfs_prop.c zprop_register_index/string/number with PROP_INHERIT).
var inheritable = map[string]bool{
	"sharenfs": true, "sharesmb": true, "compression": true, "recordsize": true,
	"atime": true, "relatime": true, "sync": true, "logbias": true, "dedup": true,
	"xattr": true, "acltype": true, "snapdir": true, "copies": true,
}

func known(prop string) bool {
	if strings.Contains(prop, ":") {
		return true
	}
	switch prop {
	case "all", "name", "type", "origin", "creation", "used", "userrefs":
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
		// get_numeric_property: on a snapshot the property must also
		// apply to the snapshot's dataset type (headcheck)
		if n.typ == zfs.TypeSnapshot && len(types) > 1 && !slices.Contains(types, s.ds[parent(name)].typ) {
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
			if v, ok := n.recv[prop]; ok && !n.inh[prop] {
				return v, "received", true
			}
			// inherited: the setpoint's path plus the relative name
			// (zfs_prop_get, libzfs_dataset.c)
			for p := parent(name); p != ""; p = parent(p) {
				v, ok := s.ds[p].props[prop]
				if !ok && !s.ds[p].inh[prop] {
					v, ok = s.ds[p].recv[prop]
				}
				if !ok {
					continue
				}
				if strings.HasPrefix(v, "/") {
					v = strings.TrimSuffix(v, "/") + "/" + name[len(p)+1:]
				}
				return v, "inherited from " + p, true
			}
			return "/" + name, "default", true
		}
		return "-", "-", true
	case "mounted":
		if s.mounted(name) {
			return "yes", "-", true
		}
		return "no", "-", true
	case "userrefs":
		if n.typ != zfs.TypeSnapshot {
			return "-", "-", true
		}
		return strconv.Itoa(len(n.holds)), "-", true
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
	if v, ok := n.recv[prop]; ok && !n.inh[prop] {
		return v, "received", true
	}
	// inherited user properties and PROP_INHERIT natives (zfs_prop.c);
	// a snapshot inherits from its dataset first (dsl_prop_get_all_ds)
	if strings.Contains(prop, ":") || inheritable[prop] {
		for p := parent(name); p != ""; p = parent(p) {
			if v, ok := s.ds[p].props[prop]; ok {
				return v, "inherited from " + p, true
			}
			if v, ok := s.ds[p].recv[prop]; ok && !s.ds[p].inh[prop] {
				return v, "inherited from " + p, true
			}
		}
		if v, ok := defaults[prop]; ok {
			return v, "default", true
		}
		return "-", "-", true
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
	return n != nil && n.typ == zfs.TypeFilesystem && n.mounted
}

// mountable is zfs_is_mountable: a filesystem with canmount other than off
// and a real mountpoint.
func (s *Sim) mountable(name string) bool {
	n := s.ds[name]
	if n == nil || n.typ != zfs.TypeFilesystem || s.canmount(name) == "off" {
		return false
	}
	mp, _, _ := s.value(name, "mountpoint")
	return mp != "none" && mp != "legacy"
}

// canmount is the effective canmount (local, then received, default on).
func (s *Sim) canmount(name string) string {
	v, _, _ := s.value(name, "canmount")
	return v
}

// mountNew is zfs_mount_and_share after create, clone and recv: only a
// canmount=on filesystem is mounted.
func (s *Sim) mountNew(name string) {
	n := s.ds[name]
	if s.canmount(name) == "on" && s.mountable(name) {
		n.mounted = true
	}
}

// changelist is changelist_gather for prop at name: name, then the
// filesystems below it whose prop (or share companion) is default or
// inherited, descending only through those.
func (s *Sim) changelist(name, prop string) []string {
	companion := map[string]string{"sharenfs": "sharesmb", "sharesmb": "sharenfs"}[prop]
	inherits := func(k, p string) bool {
		_, src, _ := s.value(k, p)
		return src == "default" || strings.HasPrefix(src, "inherited")
	}
	out := []string{name}
	var walk func(string)
	walk = func(d string) {
		for _, k := range s.children(d) {
			if parent(k) != d || strings.Contains(k, "@") || s.ds[k].typ != zfs.TypeFilesystem {
				continue
			}
			if inherits(k, prop) || companion != "" && inherits(k, companion) {
				out = append(out, k)
				walk(k)
			}
		}
	}
	walk(name)
	return out
}

// prefix is changelist_prefix: a mountpoint change unmounts every node.
// It returns which nodes were mounted.
func (s *Sim) prefix(name, prop string) map[string]bool {
	was := map[string]bool{}
	for _, k := range s.changelist(name, prop) {
		was[k] = s.ds[k].mounted
		if prop == "mountpoint" {
			s.ds[k].mounted = false
		}
	}
	return was
}

// postfix is changelist_postfix: a node that is not mounted is mounted
// when it was mounted before, or when canmount=on and the property is
// mountpoint or a share property is on.
func (s *Sim) postfix(prop string, was map[string]bool) {
	for k, wasMounted := range was {
		n := s.ds[k]
		if n == nil || n.mounted || !s.mountable(k) {
			continue
		}
		nfs, _, _ := s.value(k, "sharenfs")
		smb, _, _ := s.value(k, "sharesmb")
		if wasMounted || (prop == "mountpoint" || nfs != "off" || smb != "off") && s.canmount(k) == "on" {
			n.mounted = true
		}
	}
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
	s.ds[name].mounted = false
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
