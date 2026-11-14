//go:build nightly

package endpoint

// Exhaustive lifecycle explorer (wave5 FSM study), nightly tier (ADR 0005):
// TestExploreLifecycle, about 40 s on 4 cores at depth 4. The classifier and
// its table test are in fsm_classify_test.go and run on every PR.
//
// Breadth-first over world states reached from an empty parent by RPCs and
// out-of-band actions, up to FSM_DEPTH steps, over 2 volumes (a, b) and one
// CSI snapshot name (s). For every (state, RPC) edge it runs:
//   - the uncrashed RPC
//   - a crash at every mutation boundary (0..n), and a partial command at
//     each multi-property `zfs set` (every proper subset) and each
//     `destroy -r` (snapshots gone, dataset left), each followed by one retry
//   - an out-of-band race injected before every mutation (locks do not cover
//     other actors)
// and from every reachable state (crash intermediates included) reconciles
// with desired = {}, = every committed volume, and = the first of them
// (so a committed volume is undesired). Out-of-band actions include a user
// hold and a dataset below a volume, read raw into each world's identity.
//
// Asserts: every row maps to a named state; crash+retry reaches the
// uncrashed final state and response; no mutation targets a Foreign row;
// reconcile never destroys a CSI snapshot or a desired committed volume and
// a second reconcile collects nothing, fails nothing and changes nothing.
// Any finding fails the test.
//
// Env: FSM_DEPTH (default 4), FSM_KIND (fs|block; default both),
// FSM_PREFIX (property prefix; default zfs-endpoint and democratic-csi),
// FSM_OUT (report path prefix; one file per kind and prefix).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

var errCrash = errors.New("fsm: process died")

type fsmCfg struct {
	prefix string
	kind   VolumeKind
	depth  int
}

// ---- world: a list of mutation commands replayed onto a fresh sim ----------

type world struct {
	cmds [][]string // zfs argv, or pseudo "#recv name prop val", "#prov name kind"
	path []string   // human trace
}

func newSim() *zfstest.Sim {
	s := zfstest.New("tank")
	var clk int64 = 1000
	var mu sync.Mutex
	s.Clock = func() int64 { mu.Lock(); defer mu.Unlock(); clk++; return clk }
	if _, err := s.Run(context.Background(), "zfs", "create", "-p", fsmParent); err != nil {
		panic(err)
	}
	return s
}

func (w world) build() (*zfstest.Sim, map[string]string) {
	s := newSim()
	prov := map[string]string{}
	for _, c := range w.cmds {
		switch c[0] {
		case "#recv":
			s.Receive(c[1], c[2], c[3])
		case "#prov":
			prov[c[1]] = c[2]
		default:
			if _, err := s.Run(context.Background(), "zfs", c...); err != nil {
				panic(fmt.Sprintf("replay %v: %v", c, err))
			}
			if c[0] == "destroy" {
				t := c[len(c)-1]
				rec := slices.Contains(c, "-r")
				for n := range prov {
					if n == t || rec && (strings.HasPrefix(n, t+"@") || strings.HasPrefix(n, t+"/")) {
						delete(prov, n)
					}
				}
			}
		}
	}
	return s, prov
}

// ---- crash/race runner ------------------------------------------------------

type fsmRunner struct {
	sim      *zfstest.Sim
	budget   int // mutations allowed; -1 = no crash
	kill     int // 0: crash kills the process; 1: deadline kills zfs before the command, 2: after it ran
	cancel   func()
	partial  int // at mutation==budget: 0 none, >0 set-subset mask+1... see below
	partialK string
	injectAt int
	inject   func(*fsmRunner)
	muts     int
	dead     bool
	log      [][]string // successful mutations
	attempts [][]string
	onMut    func(args []string)
}

func isMut(args []string) bool {
	switch args[0] {
	case "create", "snapshot", "clone", "set", "destroy", "inherit", "promote", "rename", "hold", "release":
		return true
	}
	return false
}

func (r *fsmRunner) exec(args []string) ([]byte, error) {
	if r.onMut != nil {
		r.onMut(args)
	}
	out, err := r.sim.Run(context.Background(), "zfs", args...)
	if err == nil {
		r.log = append(r.log, slices.Clone(args))
	}
	return out, err
}

func (r *fsmRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if r.dead {
		return nil, errCrash
	}
	if len(args) > 0 && isMut(args) {
		if r.inject != nil && r.muts == r.injectAt {
			f := r.inject
			r.inject = nil
			f(r)
		}
		if r.budget >= 0 && r.muts == r.budget {
			if r.partialK != "" {
				for _, p := range partialCmds(r.sim, args, r.partialK, r.partial) {
					r.exec(p)
				}
			}
			if r.kill > 0 {
				// the CSI deadline killed `zfs`: the endpoint lives on and
				// sees a killed command; the ioctl may or may not have run
				if r.kill == 2 {
					r.attempts = append(r.attempts, slices.Clone(args))
					r.exec(args)
				}
				r.budget, r.muts = -1, r.muts+1
				r.cancel()
				return nil, &zfs.ExitError{Args: append([]string{name}, args...), Code: -1, Underlay: errors.New("signal: killed")}
			}
			r.dead = true
			return nil, errCrash
		}
		r.attempts = append(r.attempts, slices.Clone(args))
		r.muts++
		return r.exec(args)
	}
	return r.sim.Run(ctx, name, args...)
}

// partialCmds: "set" applies the props selected by mask; "destroyr" destroys
// the target's snapshots but not the target (zfs destroy -r order).
func partialCmds(s *zfstest.Sim, args []string, kind string, mask int) [][]string {
	switch kind {
	case "set":
		kv := args[1 : len(args)-1]
		var sel []string
		for i, p := range kv {
			if mask&(1<<i) != 0 {
				sel = append(sel, p)
			}
		}
		return [][]string{append(append([]string{"set"}, sel...), args[len(args)-1])}
	case "destroyr":
		t := args[len(args)-1]
		var out [][]string
		for _, r := range cleanRows(s) {
			if strings.HasPrefix(r.Name, t+"@") {
				out = append(out, []string{"destroy", r.Name})
			}
		}
		return out
	}
	return nil
}

// ---- reading and classifying ------------------------------------------------

var cleanPrefix = dcPrefix

func cleanRows(s *zfstest.Sim) []Row {
	e, err := New(zfs.New(s), Config{Parent: fsmParent, PropertyPrefix: cleanPrefix})
	if err != nil {
		panic(err)
	}
	t, err := e.scan(context.Background(), "")
	if err != nil {
		panic(err)
	}
	return slices.Collect(t.All())
}

type snapshotOf struct {
	rows    []Row
	t       Table
	classes map[string]string // full name -> state
	raw     string            // rawExtra: what the scan may not see
}

func observe(s *zfstest.Sim, prov map[string]string, prefix string) snapshotOf {
	rows := cleanRows(s)
	t := NewTable(slices.Clone(rows))
	cl := map[string]string{}
	for _, r := range rows {
		cl[r.Name] = classify(r, t, prov, prefix)
	}
	return snapshotOf{rows, t, cl, rawExtra(s)}
}

// rawExtra reads, with plain zfs and not through the scan under test, what
// a world has that the scan may drop: datasets below a volume and user
// holds. It is part of a world's identity, so a scan that loses them
// cannot merge two worlds that zfs tells apart.
func rawExtra(s *zfstest.Sim) string {
	out, err := s.Run(context.Background(), "zfs", "get", "-Hp", "-r", "-o", "name,property,value,source",
		"-t", "filesystem,volume,snapshot", "userrefs", fsmParent)
	if err != nil {
		panic(err)
	}
	var b strings.Builder
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(l, "\t")
		if len(f) < 3 {
			continue
		}
		id, _ := strings.CutPrefix(f[0], fsmParent+"/")
		vol, _, _ := strings.Cut(id, "@")
		if strings.Contains(vol, "/") || f[2] != "0" && f[2] != "-" {
			fmt.Fprintf(&b, "%s=%s;", id, f[2])
		}
	}
	return b.String()
}

// fingerprint: everything but creation times; plus provenance of live rows.
func (o snapshotOf) fingerprint(prov map[string]string) string {
	var b strings.Builder
	for _, r := range o.rows {
		fmt.Fprintf(&b, "%s|%s|v%d|q%d|r%d|o%s|d%v|h%d|k%v|M%v|C%v|%s|%s|%s|%s|%s|%v|%s|%s;",
			r.ID, r.Type, r.Volsize, r.Refquota, r.Refres, strings.TrimPrefix(r.Origin, fsmParent+"/"), r.Deferred, r.Holds, r.Children,
			r.Managed, r.Committed, r.VolumeName, r.SourceType, r.SourceID, r.SnapshotName, r.SnapshotSource,
			r.Local, r.ShareNFS, prov[r.Name])
	}
	return b.String() + "|raw:" + o.raw
}

func (o snapshotOf) summary() string {
	var parts []string
	for _, r := range o.rows {
		parts = append(parts, r.ID+":"+o.classes[r.Name])
	}
	if len(parts) == 0 {
		return "(empty)"
	}
	return strings.Join(parts, " ")
}

// ---- actions ----------------------------------------------------------------

type action struct {
	name string
	oob  bool
	rpc  func(ctx context.Context, e *Endpoint) string              // returns response
	x    func(s *zfstest.Sim, rows []Row, prefix string) [][]string // OOB: commands, nil = n/a
}

func full(id string) string { return fsmParent + "/" + id }

func respVol(v Volume, err error) string {
	if err != nil {
		return fmt.Sprint(CodeOf(err))
	}
	return fmt.Sprintf("OK cap=%d src=%s:%s", v.CapacityBytes, v.SourceType, v.SourceID)
}

func respErr(err error) string { return fmt.Sprint(CodeOf(err)) }

func actions(cfg fsmCfg) []action {
	const G = 1 << 30
	k := cfg.kind
	cv := func(name string, r CreateVolumeRequest) action {
		r.Kind = k
		if k == Block {
			if r.Reserve {
				r.Reserve, r.Sparse = false, true
			}
		}
		return action{name: name, rpc: func(ctx context.Context, e *Endpoint) string { return respVol(e.CreateVolume(ctx, r)) }}
	}
	get := func(rows []Row, id string) (Row, bool) {
		for _, r := range rows {
			if r.ID == id {
				return r, true
			}
		}
		return Row{}, false
	}
	mk := func(name string, typ zfs.Type, props ...string) []string {
		c := []string{"create"}
		for i := 0; i+1 < len(props); i += 2 {
			c = append(c, "-o", props[i]+"="+props[i+1])
		}
		if typ == zfs.TypeVolume {
			c = append(c, "-V", strconv.Itoa(G))
		}
		return append(c, full(name))
	}
	typ := zfs.TypeFilesystem
	if k == Block {
		typ = zfs.TypeVolume
	}
	n := props{cfg.prefix}
	return []action{
		cv("CV(a)", CreateVolumeRequest{Name: "a", CapacityBytes: G}),
		cv("CV(b,reserve)", CreateVolumeRequest{Name: "b", CapacityBytes: G, Reserve: true}),
		cv("CV(b<-a@s)", CreateVolumeRequest{Name: "b", SourceSnapshotID: "a@s"}),
		cv("CV(b<-a,reserve)", CreateVolumeRequest{Name: "b", SourceVolumeID: "a", Reserve: true}),
		cv("CV(b<-a@autosnap)", CreateVolumeRequest{Name: "b", SourceSnapshotID: "a@autosnap"}),
		{name: "CS(s,a)", rpc: func(ctx context.Context, e *Endpoint) string {
			_, err := e.CreateSnapshot(ctx, "s", "a")
			return respErr(err)
		}},
		{name: "CS(s,b)", rpc: func(ctx context.Context, e *Endpoint) string {
			_, err := e.CreateSnapshot(ctx, "s", "b")
			return respErr(err)
		}},
		{name: "DS(a@s)", rpc: func(ctx context.Context, e *Endpoint) string { return respErr(e.DeleteSnapshot(ctx, "a@s")) }},
		{name: "DS(b@s)", rpc: func(ctx context.Context, e *Endpoint) string { return respErr(e.DeleteSnapshot(ctx, "b@s")) }},
		{name: "DV(a)", rpc: func(ctx context.Context, e *Endpoint) string { return respErr(e.DeleteVolume(ctx, "a")) }},
		{name: "DV(b)", rpc: func(ctx context.Context, e *Endpoint) string { return respErr(e.DeleteVolume(ctx, "b")) }},
		{name: "EV(a,2G)", rpc: func(ctx context.Context, e *Endpoint) string { return respVol(e.ExpandVolume(ctx, "a", 2*G, 0)) }},
		{name: "Reconcile{}", rpc: func(ctx context.Context, e *Endpoint) string {
			return reconResp(reconcileAsCaller(ctx, e, ReconcileRequest{}))
		}},
		{name: "Reconcile{a}", rpc: func(ctx context.Context, e *Endpoint) string {
			return reconResp(reconcileAsCaller(ctx, e, ReconcileRequest{DesiredVolumeIDs: []string{"a"}}))
		}},
		// out of band
		{name: "X:admin-create(b)", oob: true, x: func(s *zfstest.Sim, rows []Row, _ string) [][]string {
			if _, ok := get(rows, "b"); ok {
				return nil
			}
			return [][]string{mk("b", typ), {"#prov", full("b"), "admin"}}
		}},
		{name: "X:sanoid(a@autosnap)", oob: true, x: func(s *zfstest.Sim, rows []Row, _ string) [][]string {
			if _, ok := get(rows, "a"); !ok {
				return nil
			}
			if _, ok := get(rows, "a@autosnap"); ok {
				return nil
			}
			return [][]string{{"snapshot", full("a") + "@autosnap"}}
		}},
		{name: "X:recv(a->b)", oob: true, x: func(s *zfstest.Sim, rows []Row, _ string) [][]string {
			a, ok := get(rows, "a")
			if _, has := get(rows, "b"); !ok || has {
				return nil
			}
			cmds := [][]string{mk("b", a.Type), {"#prov", full("b"), "recv"}}
			for kk, v := range map[string]string{n.managed(): fmt.Sprint(a.Managed), n.success(): fmt.Sprint(a.Committed), n.volumeName(): a.VolumeName} {
				if v != "" && v != "false" {
					cmds = append(cmds, []string{"#recv", full("b"), kk, v})
				}
			}
			if sn, ok := get(rows, "a@s"); ok && sn.Managed {
				cmds = append(cmds, []string{"snapshot", full("b") + "@s"}, []string{"#prov", full("b") + "@s", "recv"},
					[]string{"#recv", full("b") + "@s", n.managed(), "true"},
					[]string{"#recv", full("b") + "@s", n.snapshotName(), "s"},
					[]string{"#recv", full("b") + "@s", n.snapshotSource(), "a"})
			}
			return cmds
		}},
		// a backup tool pinning a snapshot (HOLD-REPLAN): the clone source
		// a clone of a leaves, or a CSI snapshot (b's, once deleted, is the
		// one Reconcile{a} meets on an undesired volume)
		{name: "X:hold(*@*)", oob: true, x: func(s *zfstest.Sim, rows []Row, _ string) [][]string {
			for _, id := range []string{"a@" + cloneSourceSnapshotPrefix + "b", "a@s", "b@s"} {
				if r, ok := get(rows, id); ok && r.Holds == 0 {
					return [][]string{{"hold", "backup", full(id)}}
				}
			}
			return nil
		}},
		// someone's dataset below a filesystem volume (OWN-12); b is the
		// volume Reconcile{a} collects
		{name: "X:child(b)", oob: true, x: func(s *zfstest.Sim, rows []Row, _ string) [][]string {
			if r, ok := get(rows, "b"); !ok || r.Type != zfs.TypeFilesystem || s.Has(full("b")+"/kid") {
				return nil
			}
			return [][]string{{"create", full("b") + "/kid"}}
		}},
		{name: "X:dcsi-create(a)", oob: true, x: func(s *zfstest.Sim, rows []Row, _ string) [][]string {
			if _, ok := get(rows, "a"); ok {
				return nil
			}
			var ps []string
			if typ == zfs.TypeFilesystem {
				ps = append(ps, "refquota", strconv.Itoa(G))
			}
			ps = append(ps, dcPrefix+":managed_resource", "true", dcPrefix+":provision_success", "true",
				dcPrefix+":csi_volume_name", "a", dcPrefix+":csi_share_volume_context", "{}")
			return [][]string{mk("a", typ, ps...), {"#prov", full("a"), "dc"}}
		}},
	}
}

func reconResp(p ReconcilePlan, err error) string {
	if err != nil {
		return respErr(err)
	}
	var ts, failed []string
	for _, a := range p.Actions {
		ts = append(ts, a.Target)
	}
	// a group zfs refuses fails without a collected action: counting
	// actions alone reads a reconcile that fails the same way on every run
	// as idempotent (wave13 AN-01; HOLD-REPLAN, OWN-12)
	for _, sk := range p.Skipped {
		if strings.HasPrefix(sk.Reason, SkipFailed) {
			failed = append(failed, sk.Target)
		}
	}
	if len(failed) > 0 {
		return "OK collected=" + strings.Join(ts, ",") + " failed=" + strings.Join(failed, ",")
	}
	return "OK collected=" + strings.Join(ts, ",")
}

// ---- running ----------------------------------------------------------------

type runResult struct {
	resp     string
	w        world
	attempts [][]string
	touches  []string // foreign-touch violations
	obs      snapshotOf
	prov     map[string]string
}

type runOpts struct {
	budget   int
	kill     int
	partialK string
	partial  int
	injectAt int
	inject   *action
}

func (x *explorer) run(w world, a action, o runOpts, label string) runResult {
	s, prov := w.build()
	r := &fsmRunner{sim: s, budget: o.budget, kill: o.kill, partialK: o.partialK, partial: o.partial, injectAt: -1}
	var touches []string
	r.onMut = func(args []string) {
		if v := x.foreignTouch(s, prov, args); v != "" {
			touches = append(touches, v)
		}
	}
	var injected [][]string
	if o.inject != nil {
		r.injectAt = o.injectAt
		ia := *o.inject
		r.inject = func(rr *fsmRunner) {
			cmds := ia.x(s, cleanRows(s), x.cfg.prefix)
			for _, c := range cmds {
				switch c[0] {
				case "#recv":
					s.Receive(c[1], c[2], c[3])
				case "#prov":
					prov[c[1]] = c[2]
				default:
					if _, err := s.Run(context.Background(), "zfs", c...); err != nil {
						panic(err)
					}
				}
				injected = append(injected, c)
			}
		}
	}
	e, err := New(zfs.New(r), Config{Parent: fsmParent, PropertyPrefix: x.cfg.prefix, LockWait: time.Second,
		Now: func() time.Time { return time.Unix(1e9, 0) }})
	if err != nil {
		panic(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r.cancel = cancel
	resp := a.rpc(ctx, e)
	if r.dead {
		resp = "CRASHED"
	}
	// worlds reached through a race are reported, never expanded, so their
	// command order (injected commands interleaved) is not needed
	nw := world{cmds: append(slices.Clone(w.cmds), r.log...), path: append(slices.Clone(w.path), label)}
	if len(injected) > 0 {
		nw.cmds = nil
	}
	return runResult{resp: resp, w: nw, attempts: r.attempts, touches: touches, obs: observe(s, prov, x.cfg.prefix), prov: prov}
}

func (x *explorer) foreignTouch(s *zfstest.Sim, prov map[string]string, args []string) string {
	o := observe(s, prov, x.cfg.prefix)
	var targets []string
	t := args[len(args)-1]
	switch args[0] {
	case "snapshot":
		vol, _, _ := strings.Cut(t, "@")
		targets = []string{vol}
	case "clone":
		targets = []string{args[len(args)-2]} // origin snapshot (pinned)
		src, _, _ := strings.Cut(args[len(args)-2], "@")
		targets = append(targets, src)
	case "set", "inherit", "promote", "rename":
		targets = []string{t}
	case "destroy":
		targets = []string{t}
		if slices.Contains(args, "-r") {
			for _, r := range o.rows {
				if strings.HasPrefix(r.Name, t+"@") || strings.HasPrefix(r.Name, t+"/") {
					targets = append(targets, r.Name)
				}
			}
		}
	case "create":
		return ""
	}
	for _, n := range targets {
		if st, ok := o.classes[n]; ok && isForeign(st) {
			return fmt.Sprintf("`zfs %s` touches %s [%s]", strings.Join(args, " "), strings.TrimPrefix(n, fsmParent+"/"), st)
		}
	}
	return ""
}

// ---- explorer ---------------------------------------------------------------

type finding struct {
	kind  string
	sig   string
	depth int
	trace string
}

type explorer struct {
	cfg      fsmCfg
	acts     []action
	mu       sync.Mutex
	findings map[string]finding
	states   map[string]int // state-name -> count of rows observed
	combos   map[string]bool
	edges    int
	runs     int
}

func (x *explorer) report(kind, sig string, depth int, trace string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	key := kind + "|" + sig
	if f, ok := x.findings[key]; ok && f.depth <= depth {
		return
	}
	x.findings[key] = finding{kind, sig, depth, trace}
}

func (x *explorer) noteStates(o snapshotOf, depth int, trace string) {
	x.mu.Lock()
	var bad []string
	for _, r := range o.rows {
		st := o.classes[r.Name]
		x.states[st]++
		if strings.HasPrefix(st, "IMPOSSIBLE") {
			bad = append(bad, r.ID+":"+st)
		}
	}
	x.combos[o.summary()] = true
	x.mu.Unlock()
	for _, b := range bad {
		x.report("unnamed-state", b[strings.Index(b, ":")+1:], depth, trace+"\n    => "+b)
	}
}

func trace(path []string) string {
	if len(path) == 0 {
		return "(empty parent)"
	}
	return strings.Join(path, " ; ")
}

func (x *explorer) checkTouches(r runResult, depth int, tr string) {
	for _, v := range r.touches {
		sig := v
		if i := strings.Index(v, "` touches"); i >= 0 {
			cmd := strings.Fields(v[1:i])
			sig = cmd[0] + strings.Join(filterFlags(cmd), "") + v[i+1:]
		}
		x.report("foreign-touch", sig, depth, tr+"\n    => "+v)
	}
}

func filterFlags(c []string) []string {
	var f []string
	for _, s := range c {
		if strings.HasPrefix(s, "-") && len(s) == 2 {
			f = append(f, " "+s)
		}
	}
	return f
}

// expand one world: returns the successor worlds to enqueue.
func (x *explorer) expand(w world, depth int) []world {
	var next []world
	base := w
	s, prov := w.build()
	o := observe(s, prov, x.cfg.prefix)
	x.reconcileCheck(w, o, depth)
	for _, a := range x.acts {
		if a.oob {
			cmds := a.x(s, o.rows, x.cfg.prefix)
			if cmds == nil {
				continue
			}
			nw := world{cmds: append(slices.Clone(base.cmds), cmds...), path: append(slices.Clone(base.path), a.name)}
			s2, p2 := nw.build()
			x.noteStates(observe(s2, p2, x.cfg.prefix), depth+1, trace(nw.path))
			next = append(next, nw)
			continue
		}
		x.mu.Lock()
		x.edges++
		x.mu.Unlock()
		clean := x.run(base, a, runOpts{budget: -1}, a.name)
		tr := trace(clean.w.path)
		x.noteStates(clean.obs, depth+1, tr)
		x.checkTouches(clean, depth+1, tr)
		next = append(next, clean.w)
		F := clean.obs.fingerprint(clean.prov)
		n := len(clean.attempts)
		type cp struct {
			o     runOpts
			label string
		}
		var cps []cp
		for b := 0; b <= n; b++ {
			cps = append(cps, cp{runOpts{budget: b}, fmt.Sprintf("%s CRASH@%d/%d", a.name, b, n)})
		}
		for b, args := range clean.attempts {
			switch {
			case args[0] == "set" && len(args) > 3:
				m := len(args) - 2
				for mask := 1; mask < (1<<m)-1; mask++ {
					cps = append(cps, cp{runOpts{budget: b, partialK: "set", partial: mask},
						fmt.Sprintf("%s KILLED-mid(op %d: zfs set, applied mask %b of %v)", a.name, b+1, mask, args[1:len(args)-1])})
				}
			case args[0] == "destroy" && slices.Contains(args, "-r"):
				cps = append(cps, cp{runOpts{budget: b, partialK: "destroyr"},
					fmt.Sprintf("%s KILLED-mid(op %d: destroy -r, snapshots gone)", a.name, b+1)})
			}
		}
		for _, c := range cps {
			cr := x.run(base, a, c.o, c.label)
			ctr := trace(cr.w.path)
			x.noteStates(cr.obs, depth+1, ctr)
			x.checkTouches(cr, depth+1, ctr)
			if c.o.budget < n || c.o.partialK != "" {
				next = append(next, cr.w)
			}
			rt := x.run(cr.w, a, runOpts{budget: -1}, "retry "+a.name)
			rtr := trace(rt.w.path)
			x.noteStates(rt.obs, depth+2, rtr)
			x.checkTouches(rt, depth+2, rtr)
			if rt.obs.fingerprint(rt.prov) != F {
				x.report("retry-diverges", a.name+" "+crashKind(c.label)+" "+stateDiff(clean.obs, rt.obs), depth+2,
					rtr+"\n    uncrashed: "+clean.obs.summary()+" resp="+clean.resp+
						"\n    retried:   "+rt.obs.summary()+" resp="+rt.resp+
						"\n    diff: "+fpDiff(clean.obs, clean.prov, rt.obs, rt.prov)+
						"\n    then Reconcile{committed}: "+x.recovers(rt.w, clean))
			} else if rt.resp != clean.resp && !strings.HasPrefix(a.name, "Reconcile") {
				x.report("retry-response", a.name+" "+crashKind(c.label)+" "+clean.resp+" vs "+rt.resp, depth+2,
					rtr+"\n    uncrashed resp="+clean.resp+"  retried resp="+rt.resp)
			}
			x.mu.Lock()
			x.runs += 2
			x.mu.Unlock()
		}
		// deadline kills: the endpoint survives and answers; once any op of
		// the plan has run, the answer must be one the CO retries
		for b := 0; b < n; b++ {
			for _, kill := range []int{1, 2} {
				kr := x.run(base, a, runOpts{budget: b, kill: kill}, fmt.Sprintf("%s DEADLINE-KILL(op %d/%d, ran=%v)", a.name, b+1, n, kill == 2))
				applied := b > 0 || kill == 2
				if applied && !nonFinal(kr.resp) {
					x.report("kill-final-code", fmt.Sprintf("%s -> code %s", a.name, kr.resp), depth+1,
						trace(kr.w.path)+"\n    => response "+kr.resp+" after a partially applied plan (final for external-provisioner)")
				}
			}
		}
		// races: an out-of-band action between two of this plan's steps
		for b := 0; b < n; b++ {
			for i := range x.acts {
				oa := x.acts[i]
				if !oa.oob {
					continue
				}
				rr := x.run(base, a, runOpts{budget: -1, injectAt: b, inject: &oa}, fmt.Sprintf("%s [race: %s before op %d/%d]", a.name, oa.name, b+1, n))
				rtr := trace(rr.w.path)
				x.noteStates(rr.obs, depth+1, rtr)
				x.checkTouches(rr, depth+1, rtr)
			}
		}
	}
	return next
}

func crashKind(label string) string {
	if i := strings.Index(label, "CRASH@"); i >= 0 {
		return label[i:]
	}
	if i := strings.Index(label, "KILLED-mid"); i >= 0 {
		j := strings.Index(label[i:], ",")
		if j < 0 {
			return label[i:]
		}
		return label[i : i+j]
	}
	return label
}

func stateDiff(a, b snapshotOf) string {
	if a.summary() == b.summary() {
		return "(same states, different props)"
	}
	return "[" + a.summary() + "] vs [" + b.summary() + "]"
}

func fpDiff(a snapshotOf, pa map[string]string, b snapshotOf, pb map[string]string) string {
	one := func(o snapshotOf, p map[string]string) map[string]string {
		m := map[string]string{}
		for _, r := range o.rows {
			m[r.ID] = snapshotOf{rows: []Row{r}}.fingerprint(p)
		}
		return m
	}
	ma, mb := one(a, pa), one(b, pb)
	var d []string
	for id, v := range ma {
		if mb[id] != v {
			d = append(d, fmt.Sprintf("%s: %q -> %q", id, v, mb[id]))
		}
	}
	for id, v := range mb {
		if _, ok := ma[id]; !ok {
			d = append(d, fmt.Sprintf("%s: (absent) -> %q", id, v))
		}
	}
	sort.Strings(d)
	return strings.Join(d, "; ")
}

func (x *explorer) reconcileCheck(w world, o snapshotOf, depth int) {
	var desired []string
	for _, r := range o.rows {
		st := o.classes[r.Name]
		if strings.HasPrefix(st, "Committed") || st == "Adopted-dcsi" {
			desired = append(desired, r.ID)
		}
	}
	sets := [][]string{nil, desired}
	if len(desired) > 1 {
		// a proper subset: committed volumes outside it are undesired, the
		// only way a reconcile reaches a committed volume's snapshots and
		// children (HOLD-REPLAN, OWN-12)
		sets = append(sets, desired[:1])
	}
	for _, d := range sets {
		label := fmt.Sprintf("Reconcile%v", d)
		ra := action{name: label, rpc: func(ctx context.Context, e *Endpoint) string {
			return reconResp(reconcileAsCaller(ctx, e, ReconcileRequest{DesiredVolumeIDs: d}))
		}}
		r1 := x.run(w, ra, runOpts{budget: -1}, label)
		tr := trace(r1.w.path)
		x.checkTouches(r1, depth+1, tr)
		x.noteStates(r1.obs, depth+1, tr)
		// destroyed rows
		after := map[string]bool{}
		for _, r := range r1.obs.rows {
			after[r.Name] = true
		}
		for _, r := range o.rows {
			st := o.classes[r.Name]
			if after[r.Name] {
				continue
			}
			bad := st == "CSI-snap" || isForeign(st) ||
				(slices.Contains(d, r.ID) && (strings.HasPrefix(st, "Committed") || st == "Adopted-dcsi"))
			if bad {
				x.report("reconcile-unsafe", label[:min(len(label), 12)]+" destroys "+st, depth+1, tr+"\n    => destroyed "+r.ID+" ["+st+"]")
			}
		}
		r2 := x.run(r1.w, ra, runOpts{budget: -1}, label+" again")
		if r2.obs.fingerprint(r2.prov) != r1.obs.fingerprint(r1.prov) || !strings.HasSuffix(r2.resp, "collected=") {
			x.report("reconcile-not-idempotent", label[:min(len(label), 12)]+" "+o.summary(), depth+2,
				trace(r2.w.path)+"\n    first: "+r1.resp+"  second: "+r2.resp)
		}
	}
}

// TestExploreLifecycle runs the explorer over both volume kinds and both
// property prefixes (the endpoint's own and democratic-csi adoption) to
// depth 4 (about 25 s on 4 cores), and fails on any finding. The subtests
// run one at a time: cleanRows reads the package-level cleanPrefix.
// reconcileAsCaller runs r as a caller must (ADR 0001 v2 D3): a dry run,
// then that plan by its digest. The dry run mutates nothing, so it moves
// no crash or kill point.
func reconcileAsCaller(ctx context.Context, e *Endpoint, r ReconcileRequest) (ReconcilePlan, error) {
	dry := r
	dry.DryRun = true
	p, err := e.Reconcile(ctx, dry)
	if err != nil {
		return p, err
	}
	r.PlanDigest = p.Digest
	return e.Reconcile(ctx, r)
}

func TestExploreLifecycle(t *testing.T) {
	depth := 4
	if v := os.Getenv("FSM_DEPTH"); v != "" {
		depth, _ = strconv.Atoi(v)
	}
	kinds := []VolumeKind{Filesystem, Block}
	switch os.Getenv("FSM_KIND") {
	case "fs":
		kinds = kinds[:1]
	case "block":
		kinds = kinds[1:]
	}
	prefixes := []string{DefaultPropertyPrefix, dcPrefix}
	if v := os.Getenv("FSM_PREFIX"); v != "" {
		prefixes = []string{v}
	}
	for _, k := range kinds {
		for _, p := range prefixes {
			t.Run(fmt.Sprintf("%s/%s", k, p), func(t *testing.T) {
				exploreLifecycle(t, fsmCfg{prefix: p, kind: k, depth: depth})
			})
		}
	}
}

func exploreLifecycle(t *testing.T, cfg fsmCfg) {
	cleanPrefix = cfg.prefix
	x := &explorer{cfg: cfg, acts: actions(cfg), findings: map[string]finding{}, states: map[string]int{}, combos: map[string]bool{}}
	seen := map[string]bool{}
	frontier := []world{{}}
	{
		s, p := frontier[0].build()
		seen[observe(s, p, cfg.prefix).fingerprint(p)] = true
	}
	start := time.Now()
	for d := 0; d < cfg.depth && len(frontier) > 0; d++ {
		var mu sync.Mutex
		var next []world
		var wg sync.WaitGroup
		sem := make(chan struct{}, 8)
		for _, w := range frontier {
			wg.Add(1)
			sem <- struct{}{}
			go func(w world) {
				defer wg.Done()
				defer func() { <-sem }()
				succ := x.expand(w, d)
				mu.Lock()
				defer mu.Unlock()
				for _, nw := range succ {
					s, p := nw.build()
					fp := observe(s, p, cfg.prefix).fingerprint(p)
					if !seen[fp] {
						seen[fp] = true
						next = append(next, nw)
					}
				}
			}(w)
		}
		wg.Wait()
		t.Logf("depth %d: expanded %d worlds, %d new, %d distinct, edges=%d runs=%d (%s)", d, len(frontier), len(next), len(seen), x.edges, x.runs, time.Since(start).Round(time.Millisecond))
		frontier = next
	}
	// leaf worlds: still check states and reconcile
	for _, w := range frontier {
		s, p := w.build()
		o := observe(s, p, cfg.prefix)
		x.noteStates(o, cfg.depth, trace(w.path))
		x.reconcileCheck(w, o, cfg.depth)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# FSM explore: kind=%s prefix=%s depth=%d\n", cfg.kind, cfg.prefix, cfg.depth)
	fmt.Fprintf(&b, "distinct worlds=%d edges=%d crash/retry runs=%d combos=%d time=%s\n\n", len(seen), x.edges, x.runs, len(x.combos), time.Since(start).Round(time.Millisecond))
	fmt.Fprintf(&b, "## row states observed\n")
	for _, k := range fsmSortedKeys(x.states) {
		fmt.Fprintf(&b, "  %-60s %d\n", k, x.states[k])
	}
	fmt.Fprintf(&b, "\n## findings (minimal depth first)\n")
	fs := make([]finding, 0, len(x.findings))
	for _, f := range x.findings {
		fs = append(fs, f)
	}
	sort.Slice(fs, func(i, j int) bool {
		if fs[i].kind != fs[j].kind {
			return fs[i].kind < fs[j].kind
		}
		if fs[i].depth != fs[j].depth {
			return fs[i].depth < fs[j].depth
		}
		return fs[i].sig < fs[j].sig
	})
	for _, f := range fs {
		fmt.Fprintf(&b, "\n[%s] (depth %d) %s\n  trace: %s\n", f.kind, f.depth, f.sig, f.trace)
	}
	if out := os.Getenv("FSM_OUT"); out != "" {
		if err := os.WriteFile(fmt.Sprintf("%s-%s-%s.txt", out, cfg.kind, cfg.prefix), []byte(b.String()), 0o644); err != nil {
			t.Error(err)
		}
	}
	t.Logf("distinct worlds=%d edges=%d runs=%d, %d findings", len(seen), x.edges, x.runs, len(fs))
	// every finding is a bug in the endpoint, the simulator or the
	// classifier: an unnamed state, a foreign touch, a crash+retry that
	// diverges, a final code after a partial plan, an unsafe or
	// non-idempotent reconcile
	for i, f := range fs {
		if i == 20 {
			t.Errorf("... and %d more (FSM_OUT=<path> writes the full report)", len(fs)-i)
			break
		}
		t.Errorf("[%s] (depth %d) %s\n  trace: %s", f.kind, f.depth, f.sig, f.trace)
	}
}

func mapsKeys[M ~map[K]V, K comparable, V any](m M) []K {
	ks := make([]K, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

func fsmSortedKeys(m map[string]int) []string {
	ks := mapsKeys(m)
	sort.Strings(ks)
	return ks
}

// nonFinal: codes external-provisioner keeps retrying (isFinalError).
func nonFinal(resp string) bool {
	for _, c := range []Code{Aborted, Unavailable, ResourceExhausted, 4 /* DeadlineExceeded */, 1 /* Canceled */} {
		if resp == fmt.Sprint(c) {
			return true
		}
	}
	return strings.HasPrefix(resp, "OK")
}

// recovers: does a reconcile (desired = every committed volume of the
// uncrashed end state) bring the retried world to the uncrashed one?
func (x *explorer) recovers(w world, clean runResult) string {
	var desired []string
	for _, r := range clean.obs.rows {
		st := clean.obs.classes[r.Name]
		if strings.HasPrefix(st, "Committed") || st == "Adopted-dcsi" {
			desired = append(desired, r.ID)
		}
	}
	ra := action{name: "Reconcile", rpc: func(ctx context.Context, e *Endpoint) string {
		return reconResp(reconcileAsCaller(ctx, e, ReconcileRequest{DesiredVolumeIDs: desired}))
	}}
	rc := x.run(w, ra, runOpts{budget: -1}, "Reconcile")
	if rc.obs.fingerprint(rc.prov) == clean.obs.fingerprint(clean.prov) {
		return "converges (" + rc.resp + ")"
	}
	return "still differs (" + rc.resp + "): " + fpDiff(clean.obs, clean.prov, rc.obs, rc.prov)
}
