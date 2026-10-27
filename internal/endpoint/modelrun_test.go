package endpoint_test

// Stateful property test: random sequences of RPCs and out-of-band actions,
// with crashes, faults and races injected at plan step k, run against the
// endpoint and the reference model (model_test.go). After every step it
// compares the returned code and result, and a projection of the world read
// back from zfs, and checks safety invariants that hold whatever the model
// says. Failures shrink to a minimal trace and print a replay line.
//
//	go test ./internal/endpoint -run TestModel                       # sim, MODEL_SEQS (64) x MODEL_LEN (40)
//	go test -tags nightly ./internal/endpoint -run TestExploreModel  # 20k-sequence sweep, masks still bite
//	MODEL_SEED=17 go test ./internal/endpoint -run TestModel -v       # replay one sequence
//	ZFS_TEST_POOL=tank ZFS_REQUIRE_REAL=1 go test -run TestModel ...  # real pool, MODEL_BUDGET steps
//	go test ./internal/endpoint -run '^$' -fuzz FuzzModel             # coverage-guided

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

const mPrefix = endpoint.DefaultPropertyPrefix

// ---- projection shared by model and world ------------------------------------

func projDS(k endpoint.VolumeKind, size uint64, res bool, origin string, m, c bool, vn, st, si string) string {
	return fmt.Sprintf("%s size=%d res=%v origin=%s managed=%v committed=%v vn=%s src=%s:%s", k, size, res, origin, m, c, vn, st, si)
}

func projSnap(deferred bool, holds int, m bool, sn, ss, cs string) string {
	return fmt.Sprintf("snapshot deferred=%v holds=%d managed=%v name=%s src=%s clonefor=%s", deferred, holds, m, sn, ss, cs)
}

// ---- fault-injecting runner: works on the simulator and on real zfs ----------

type faultKind uint8

const (
	noFault    faultKind = iota
	killBefore           // the caller's deadline kills zfs before it runs
	killAfter            // ... after it ran (outcome unknown to the endpoint)
	errBefore            // zfs fails with an unclassified error
	race                 // an out-of-band action lands just before the op
)

var faultNames = [...]string{"none", "killBefore", "killAfter", "errBefore", "race"}

type Fault struct {
	Kind faultKind
	At   int   // index of the mutating zfs command it hits
	Race *Step // the out-of-band action, for race
}

var mMutating = map[string]bool{"create": true, "snapshot": true, "clone": true, "set": true, "destroy": true,
	"inherit": true, "rename": true, "promote": true, "hold": true, "release": true}

type mFaultRunner struct {
	inner  zfs.Runner
	mu     sync.Mutex
	f      Fault
	muts   int
	fired  bool
	stop   context.CancelFunc
	onRace func() // runs the race action through the admin client
	log    [][]string
}

func (r *mFaultRunner) arm(f Fault, stop context.CancelFunc, onRace func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.f, r.muts, r.fired, r.stop, r.onRace, r.log = f, 0, false, stop, onRace, nil
}

func (r *mFaultRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	r.log = append(r.log, args)
	hit := false
	if len(args) > 0 && mMutating[args[0]] {
		hit = r.f.Kind != noFault && !r.fired && r.muts == r.f.At
		r.muts++
		if hit {
			r.fired = true
		}
	}
	f, stop, onRace := r.f, r.stop, r.onRace
	r.mu.Unlock()
	if !hit {
		return r.inner.Run(ctx, name, args...)
	}
	killed := &zfs.ExitError{Args: append([]string{name}, args...), Code: -1, Underlay: context.Canceled}
	switch f.Kind {
	case killBefore:
		stop()
		return nil, killed
	case killAfter:
		out, err := r.inner.Run(context.WithoutCancel(ctx), name, args...)
		stop()
		if err != nil {
			return out, err
		}
		return out, killed
	case errBefore:
		return nil, &zfs.ExitError{Args: append([]string{name}, args...), Code: 1, Stderr: "internal error: Input/output error\n"}
	case race:
		onRace()
	}
	return r.inner.Run(ctx, name, args...)
}

// ---- steps --------------------------------------------------------------------

// Step is one RPC or out-of-band action, as plain data: traces are
// generated, shrunk, printed and replayed as []Step.
type Step struct {
	Op      string
	Req     endpoint.CreateVolumeRequest // CreateVolume
	ID      string                       // volume, snapshot, or admin source
	Name    string                       // snapshot name, or admin target
	Desired []string                     // Reconcile
	Fault   Fault
}

func (s Step) String() string {
	var b strings.Builder
	switch s.Op {
	case "CreateVolume":
		r := s.Req
		fmt.Fprintf(&b, "CreateVolume(%s %s cap=%d", r.Name, r.Kind, r.CapacityBytes)
		if r.Sparse {
			b.WriteString(" sparse")
		}
		if r.Reserve {
			b.WriteString(" reserve")
		}
		if r.SourceSnapshotID != "" {
			b.WriteString(" from-snap=" + r.SourceSnapshotID)
		}
		if r.SourceVolumeID != "" {
			b.WriteString(" from-vol=" + r.SourceVolumeID)
		}
		b.WriteString(")")
	case "Reconcile":
		fmt.Fprintf(&b, "Reconcile(desired=%v)", s.Desired)
	default:
		fmt.Fprintf(&b, "%s(%s", s.Op, s.ID)
		if s.Name != "" {
			b.WriteString(", " + s.Name)
		}
		b.WriteString(")")
	}
	if s.Fault.Kind != noFault {
		fmt.Fprintf(&b, " !%s@%d", faultNames[s.Fault.Kind], s.Fault.At)
		if s.Fault.Race != nil {
			b.WriteString("[" + s.Fault.Race.String() + "]")
		}
	}
	return b.String()
}

func (s Step) admin() bool { return strings.HasPrefix(s.Op, "adm.") }

// ---- generator ------------------------------------------------------------------

type source interface{ Intn(n int) int }

type rngSource struct{ r *rand.Rand }

func (s rngSource) Intn(n int) int { return s.r.IntN(n) }

// byteSource lets the fuzzer drive the same generator.
type byteSource struct {
	b []byte
	i int
}

func (s *byteSource) Intn(n int) int {
	if s.i >= len(s.b) {
		return 0
	}
	v := int(s.b[s.i])
	s.i++
	return v % n
}

var (
	volPool  = []string{"v0", "v1", "v2", "v3"}
	snapPool = []string{"s0", "s1"}
	caps     = []uint64{0, 1 << 20, 2 << 20, 3<<20 + 5}
)

func pick[T any](s source, xs []T) T { return xs[s.Intn(len(xs))] }

func chance(s source, pct int) bool { return s.Intn(100) < pct }

// genTrace generates n steps, tracking a shadow model only to pick ids
// that exist (the executor never trusts it).
func genTrace(s source, n int) []Step {
	m := newModel()
	last := map[string]endpoint.CreateVolumeRequest{}
	var out []Step
	owned := func() string { // an existing volume, most of the time
		var vs []string
		for _, v := range volPool {
			if m.ownedVol(v) {
				vs = append(vs, v)
			}
		}
		if len(vs) == 0 || chance(s, 25) {
			return pick(s, volPool)
		}
		return pick(s, vs)
	}
	snapIDs := func() []string {
		ids := slices.Sorted(maps.Keys(m.Snaps))
		return append(ids, "v0@s0", "v1@s1")
	}
	for len(out) < n {
		var st Step
		w := s.Intn(100)
		switch {
		case w < 30:
			name := pick(s, volPool)
			r, ok := last[name]
			if !ok || chance(s, 60) {
				r = endpoint.CreateVolumeRequest{Name: name, Kind: endpoint.VolumeKind(s.Intn(2))}
				switch s.Intn(4) {
				case 0:
					r.SourceSnapshotID = pick(s, snapIDs())
				case 1:
					r.SourceVolumeID = owned()
				}
				r.CapacityBytes = pick(s, caps)
				if r.Kind == endpoint.Block {
					r.Sparse = chance(s, 50)
					if r.CapacityBytes == 0 && r.SourceSnapshotID == "" && r.SourceVolumeID == "" {
						r.CapacityBytes = 1 << 20
					}
				} else {
					r.Reserve = chance(s, 25)
				}
			}
			last[name] = r
			st = Step{Op: "CreateVolume", Req: r}
		case w < 42:
			id := owned()
			if chance(s, 3) {
				id = "not/issued"
			}
			st = Step{Op: "DeleteVolume", ID: id}
		case w < 57:
			st = Step{Op: "CreateSnapshot", ID: owned(), Name: pick(s, snapPool)}
		case w < 67:
			st = Step{Op: "DeleteSnapshot", ID: pick(s, snapIDs())}
		case w < 72:
			var d []string
			for _, v := range volPool {
				if m.ownedVol(v) && chance(s, 60) {
					d = append(d, v)
				}
			}
			st = Step{Op: "Reconcile", Desired: d}
		default:
			st = genAdmin(s, m)
		}
		if !st.admin() && chance(s, 25) {
			st.Fault = Fault{Kind: faultKind(1 + s.Intn(4)), At: pick(s, []int{0, 0, 0, 1, 1, 2})}
			if st.Fault.Kind == race {
				r := genRace(s, st)
				st.Fault.Race = &r
			}
		}
		applyModel(m, st) // shadow only
		out = append(out, st)
		if st.Op == "adm.recv" {
			// a received copy carries every marker as "received": aim an RPC
			// at it, or detection of OWN-01/OWN-06 is left to chance (A6)
			fu := []Step{{Op: "DeleteVolume", ID: st.Name}, {Op: "CreateSnapshot", ID: st.Name, Name: pick(s, snapPool)},
				{Op: "DeleteSnapshot", ID: st.Name + "@" + pick(s, snapPool)},
				{Op: "CreateVolume", Req: endpoint.CreateVolumeRequest{Name: pick(s, volPool), Kind: endpoint.VolumeKind(s.Intn(2)), SourceVolumeID: st.Name, Sparse: true}}}
			f := pick(s, fu)
			applyModel(m, f)
			out = append(out, f)
		}
	}
	return out
}

func genAdmin(s source, m *Model) Step {
	snaps := slices.Sorted(maps.Keys(m.Snaps))
	var plain []string // holds go on CSI and admin snapshots, not internal ones
	for _, k := range snaps {
		if !isInternal(k) {
			plain = append(plain, k)
		}
	}
	switch s.Intn(10) {
	case 9:
		return Step{Op: "adm.recvsnap", ID: pick(s, volPool), Name: pick(s, snapPool)}
	case 0, 1:
		// "s0" collides with CSI names; the reserved clone-source name is an
		// admin's snapshot all the same (OWN-13)
		return Step{Op: "adm.snapshot", ID: pick(s, volPool), Name: pick(s, []string{"s0", "adm", internalPrefix + pick(s, volPool)})}
	case 2:
		return Step{Op: "adm.create", Name: pick(s, volPool)}
	case 3:
		return Step{Op: "adm.child", ID: pick(s, volPool)}
	case 4:
		if len(snaps) > 0 {
			return Step{Op: "adm.clone", ID: pick(s, snaps), Name: pick(s, volPool)}
		}
	case 5:
		return Step{Op: "adm.recv", ID: pick(s, volPool), Name: pick(s, volPool)}
	case 6:
		return Step{Op: "adm.rename", ID: pick(s, volPool), Name: pick(s, append(slices.Clone(volPool), "r0"))}
	case 7:
		if len(plain) > 0 {
			return Step{Op: "adm.hold", ID: pick(s, plain)}
		}
	case 8:
		if len(plain) > 0 {
			return Step{Op: "adm.release", ID: pick(s, plain)}
		}
	}
	return Step{Op: "adm.snapshot", ID: pick(s, volPool), Name: "adm"}
}

// genRace picks an out-of-band action aimed at what the RPC touches.
func genRace(s source, st Step) Step {
	switch st.Op {
	case "CreateVolume":
		if chance(s, 50) && st.Req.SourceVolumeID != "" {
			return Step{Op: "adm.snapshot", ID: st.Req.SourceVolumeID, Name: "adm"}
		}
		return Step{Op: "adm.create", Name: st.Req.Name}
	case "DeleteVolume":
		if chance(s, 50) {
			return Step{Op: "adm.snapshot", ID: st.ID, Name: "adm"}
		}
		return Step{Op: "adm.clone", ID: st.ID + "@s0", Name: "v3"}
	case "CreateSnapshot":
		return Step{Op: "adm.snapshot", ID: st.ID, Name: st.Name}
	case "DeleteSnapshot":
		return Step{Op: "adm.hold", ID: st.ID}
	}
	if chance(s, 50) {
		return Step{Op: "adm.clone", ID: pick(s, volPool) + "@s0", Name: pick(s, volPool)}
	}
	return Step{Op: "adm.snapshot", ID: pick(s, volPool), Name: "adm"}
}

// applyModel runs a step on the model; admin steps return ok in Code (OK or
// FailedPrecondition for "zfs refused it").
func applyModel(m *Model, st Step) mResult {
	adm := func(ok bool) mResult {
		if ok {
			return mResult{}
		}
		return mResult{Code: endpoint.FailedPrecondition}
	}
	switch st.Op {
	case "CreateVolume":
		return m.CreateVolume(st.Req)
	case "DeleteVolume":
		return m.DeleteVolume(st.ID)
	case "CreateSnapshot":
		return m.CreateSnapshot(st.Name, st.ID)
	case "DeleteSnapshot":
		return m.DeleteSnapshot(st.ID)
	case "Reconcile":
		return m.Reconcile(st.Desired)
	case "adm.snapshot", "adm.recvsnap":
		return adm(m.AdminSnapshot(st.ID, st.Name))
	case "adm.create":
		return adm(m.AdminCreate(st.Name))
	case "adm.child":
		return adm(m.AdminCreate(st.ID + "/child"))
	case "adm.clone":
		return adm(m.AdminClone(st.ID, st.Name))
	case "adm.recv":
		return adm(m.AdminRecv(st.ID, "recv", st.Name))
	case "adm.rename":
		return adm(m.AdminRename(st.ID, st.Name))
	case "adm.hold":
		return adm(m.AdminHold(st.ID))
	case "adm.release":
		return adm(m.AdminRelease(st.ID))
	}
	panic(st.Op)
}

// ---- known open divergences: masked inputs, each with a spec row and expiry --
//
// The model states the contract. Where HEAD is known to break it (an
// open-finding or decided-pending spec row) the executor skips, or
// de-faults, the input that reaches it, so the suite stays green. A mask is
// never permanent:
//   - TestModel fails once a mask's expiry date has passed (fix it, or
//     renew it with a reason in review);
//   - TestExploreModelMasksStillBite (nightly) runs each mask unmasked and fails
//     when it no longer finds anything: the fix landed, delete the mask.
// MODEL_UNMASK=all, or a comma list of ids, runs the inputs anyway.

type knownOpen struct {
	id, row, expires, why string
	// match reports whether st reaches the divergence: skip the step, or
	// keep it without its fault.
	match func(m *Model, st Step) (skip, dropFault bool)
}

var knownOpens = []knownOpen{
	{id: "GAP-RECRACE", row: "OWN-15", expires: "2026-12-15",
		why: "model gap: a race inside one reconcile group is not interleaved (DeleteVolume's is)",
		match: func(m *Model, st Step) (bool, bool) {
			return false, st.Op == "Reconcile" && st.Fault.Kind == race && st.Fault.Race.Op == "adm.snapshot"
		}},
}

// unmaskOnly, when set, overrides MODEL_UNMASK (TestExploreModelMasksStillBite).
var unmaskOnly string

func masked(m *Model, st Step) (skip bool, dropFault bool, why string) {
	un := os.Getenv("MODEL_UNMASK")
	if unmaskOnly != "" {
		un = unmaskOnly
	}
	for _, k := range knownOpens {
		if un == "all" || slices.Contains(strings.Split(un, ","), k.id) {
			continue
		}
		if sk, df := k.match(m, st); sk || df {
			return sk, df, k.id + " (" + k.row + "): " + k.why
		}
	}
	return false, false, ""
}

// ---- the rig: one parent dataset, its endpoint, the admin side ----------------

type rig struct {
	tb       testing.TB
	parent   string
	sim      *zfstest.Sim // nil on a real pool
	fr       *mFaultRunner
	z, admin *zfs.Client
	ep       *endpoint.Endpoint
	clock    atomic.Int64 // steps taken: the endpoint's clock runs 2h per step, so MinAge never decides
}

var realRun = strconv.FormatInt(time.Now().UnixNano(), 36)

func newRig(tb testing.TB, worker string) *rig {
	return newRigOn(tb, worker, os.Getenv("ZFS_TEST_POOL"))
}

// newSimRig is a rig on the simulator wherever the test runs.
func newSimRig(tb testing.TB) *rig { return newRigOn(tb, "sim", "") }

func newRigOn(tb testing.TB, worker, pool string) *rig {
	ctx := context.Background()
	g := &rig{tb: tb}
	var base zfs.Runner
	if pool != "" {
		base = zfs.LocalRunner{}
		g.parent = pool + "/model-" + realRun + "-" + worker
	} else if os.Getenv("ZFS_REQUIRE_REAL") != "" && worker != "sim" {
		tb.Fatal("ZFS_REQUIRE_REAL is set but ZFS_TEST_POOL is not")
	} else {
		g.sim = zfstest.New("tank")
		base = g.sim
		g.parent = "tank/m"
	}
	g.fr = &mFaultRunner{inner: base}
	g.z, g.admin = zfs.New(g.fr), zfs.New(base)
	if err := g.admin.CreateFilesystem(ctx, g.parent, nil, true); err != nil {
		tb.Fatal(err)
	}
	if g.sim == nil {
		tb.Cleanup(g.destroyReal)
	}
	cfg := endpoint.Config{Parent: g.parent, LockWait: time.Second,
		Now: func() time.Time { return time.Now().Add(time.Duration(g.clock.Load()) * 2 * time.Hour) }}
	if g.sim == nil {
		// as zfs-endpoint runs: udev's blkid may still hold a new zvol when
		// it is destroyed, and the endpoint settles and retries once
		cfg.Settle = endpoint.UdevSettle
	}
	ep, err := endpoint.New(g.z, cfg)
	if err != nil {
		tb.Fatal(err)
	}
	g.ep = ep
	return g
}

func (g *rig) destroyReal() {
	ctx := context.Background()
	rows, _ := g.read(ctx)
	for name, r := range rows {
		if r.holds > 0 {
			g.raw(ctx, "release", "keep", g.parent+"/"+name)
		}
	}
	o := zfs.DestroyOptions{Recursive: true}
	err := g.admin.Destroy(ctx, g.parent, o)
	if errors.Is(err, zfs.ErrBusy) && endpoint.UdevSettle(ctx) == nil {
		err = g.admin.Destroy(ctx, g.parent, o)
	}
	if err != nil {
		g.tb.Errorf("cleanup %s: %v", g.parent, err)
	}
}

func (g *rig) raw(ctx context.Context, args ...string) error {
	_, err := g.admin.Runner.Run(ctx, "zfs", args...)
	return err
}

// admin runs an out-of-band action with real zfs commands (the simulator
// stands in for `zfs send | zfs recv` with Receive) and reports success.
func (g *rig) adminStep(ctx context.Context, st Step) bool {
	p := func(n string) string { return g.parent + "/" + n }
	var err error
	switch st.Op {
	case "adm.snapshot":
		err = g.raw(ctx, "snapshot", p(st.ID+"@"+st.Name))
	case "adm.create":
		err = g.raw(ctx, "create", p(st.Name))
	case "adm.child":
		err = g.raw(ctx, "create", p(st.ID+"/child"))
	case "adm.clone":
		err = g.raw(ctx, "clone", p(st.ID), p(st.Name))
	case "adm.rename":
		if strings.Contains(st.ID, "/") {
			return false
		}
		err = g.raw(ctx, "rename", p(st.ID), p(st.Name))
	case "adm.hold":
		err = g.raw(ctx, "hold", "keep", p(st.ID))
	case "adm.release":
		err = g.raw(ctx, "release", "keep", p(st.ID))
	case "adm.recv":
		return g.recv(ctx, st.ID, "recv", st.Name) == nil
	case "adm.recvsnap":
		// a snapshot that arrived by `zfs recv` carrying CSI markers from
		// wherever it was cut: received, so not ownership. Simulator only:
		// on a real pool this needs a replication pair (see model.md).
		if err = g.raw(ctx, "snapshot", p(st.ID+"@"+st.Name)); err == nil {
			for k, v := range map[string]string{"managed_resource": "true", "csi_snapshot_name": st.Name, "csi_snapshot_source_volume_id": st.ID} {
				g.sim.Receive(p(st.ID+"@"+st.Name), mPrefix+":"+k, v)
			}
		}
	}
	return err == nil
}

func (g *rig) recv(ctx context.Context, vol, tag, to string) error {
	if ok, _ := g.admin.Exists(ctx, g.parent+"/"+to); ok || strings.Contains(vol, "/") {
		return errors.New("exists")
	}
	if kids, err := g.admin.ListColumns(ctx, g.parent+"/"+vol, zfs.ListOptions{Types: []zfs.Type{zfs.TypeFilesystem, zfs.TypeVolume}, Depth: 0, Props: []string{"type"}}); err != nil || len(kids.Names) > 1 {
		return errors.New("has children, or missing")
	}
	if err := g.raw(ctx, "snapshot", g.parent+"/"+vol+"@"+tag); err != nil {
		return err
	}
	if g.sim == nil {
		_, err := g.admin.Runner.Run(ctx, "sh", "-c", `zfs send -R "$1" | zfs recv "$2"`, "sh",
			g.parent+"/"+vol+"@"+tag, g.parent+"/"+to)
		return err
	}
	// simulator: a full copy whose properties all arrive "received"
	src, err := g.admin.Get(ctx, g.parent+"/"+vol, "type", "volsize", "refquota", "refreservation")
	if err != nil {
		return err
	}
	dst := g.parent + "/" + to
	if src.Type() == zfs.TypeVolume {
		n, _ := src.Props["volsize"].Uint()
		err = g.admin.CreateVolume(ctx, dst, n, true, nil)
	} else {
		err = g.admin.CreateFilesystem(ctx, dst, nil, false)
	}
	if err != nil {
		return err
	}
	cols, err := g.admin.ListColumns(ctx, g.parent+"/"+vol, zfs.ListOptions{
		Types: []zfs.Type{zfs.TypeFilesystem, zfs.TypeVolume, zfs.TypeSnapshot}, Depth: 0, Props: projProps()})
	if err != nil {
		return err
	}
	for i, name := range cols.Names {
		target := dst + strings.TrimPrefix(name, g.parent+"/"+vol)
		if name != g.parent+"/"+vol {
			if err := g.raw(ctx, "snapshot", target); err != nil {
				return err
			}
			if strings.Contains(name, "@") {
				g.sim.KeepGUID(name, target)
			}
		}
		for j, prop := range cols.Props {
			c := cols.Cell(i, j)
			isUser := strings.Contains(prop, ":")
			if c.Source == "local" && (isUser || prop == "refquota" || prop == "refreservation") {
				g.sim.Receive(target, prop, c.Value)
			}
		}
	}
	return nil
}

func projProps() []string {
	return []string{"type", "guid", "origin", "defer_destroy", "userrefs", "volsize", "refquota", "refreservation",
		mPrefix + ":managed_resource", mPrefix + ":provision_success", mPrefix + ":csi_volume_name",
		mPrefix + ":csi_volume_content_source_type", mPrefix + ":csi_volume_content_source_id",
		mPrefix + ":csi_snapshot_name", mPrefix + ":csi_snapshot_source_volume_id", mPrefix + ":clone_source_for"}
}

type wrow struct {
	guid     string
	proj     string
	snap     bool
	deferred bool
	holds    int
	origin   string
	marked   bool // any ownership marker set locally
	owned    bool // the markers name this row (ADR 0003), judged from zfs alone
	cloneFor string
}

// read is the world as zfs reports it, projected like Model.Project.
func (g *rig) read(ctx context.Context) (map[string]wrow, error) {
	cols, err := g.admin.ListColumns(ctx, g.parent, zfs.ListOptions{
		Types: []zfs.Type{zfs.TypeFilesystem, zfs.TypeVolume, zfs.TypeSnapshot}, Depth: -1, Props: projProps()})
	if err != nil {
		return nil, err
	}
	out := map[string]wrow{}
	for i, name := range cols.Names {
		if name == g.parent || strings.HasPrefix(name, g.parent+"@") {
			continue
		}
		cell := func(p string) zfs.Prop { return cols.Cell(i, cols.Col(p)) }
		num := func(p string) uint64 { n, _ := cell(p).Uint(); return n }
		loc := func(p string) string {
			if c := cell(mPrefix + ":" + p); c.Source == "local" { // not Prop.Local: the observer shares no code with the system under test
				return c.Value
			}
			return ""
		}
		rel := strings.TrimPrefix(name, g.parent+"/")
		origin := cell("origin").Value
		if origin == "-" {
			origin = ""
		}
		origin = strings.TrimPrefix(origin, g.parent+"/")
		holds, _ := strconv.Atoi(cell("userrefs").Value)
		r := wrow{guid: cell("guid").Value, origin: origin, holds: holds}
		for _, p := range []string{"managed_resource", "provision_success", "csi_volume_name", "csi_snapshot_name", "csi_snapshot_source_volume_id"} {
			r.marked = r.marked || loc(p) != ""
		}
		switch zfs.Type(cell("type").Value) {
		case zfs.TypeSnapshot:
			vol, _, _ := strings.Cut(rel, "@")
			_, sname, _ := strings.Cut(rel, "@")
			r.owned = loc("managed_resource") == "true" && (loc("csi_snapshot_name") != "" && loc("csi_snapshot_source_volume_id") == vol ||
				loc("clone_source_for") != "" && sname == internalPrefix+loc("clone_source_for"))
			r.snap, r.deferred = true, cell("defer_destroy").Value == "on"
			r.cloneFor = loc("clone_source_for")
			r.proj = projSnap(r.deferred, holds, loc("managed_resource") == "true", loc("csi_snapshot_name"), loc("csi_snapshot_source_volume_id"), r.cloneFor)
		default:
			r.owned = loc("managed_resource") == "true" && loc("csi_volume_name") == rel
			k, size := endpoint.Filesystem, num("refquota")
			if zfs.Type(cell("type").Value) == zfs.TypeVolume {
				k, size = endpoint.Block, num("volsize")
			}
			r.proj = projDS(k, size, num("refreservation") > 0, origin, loc("managed_resource") == "true",
				loc("provision_success") == "true", loc("csi_volume_name"),
				loc("csi_volume_content_source_type"), loc("csi_volume_content_source_id"))
		}
		out[rel] = r
	}
	return out, nil
}

// ---- running a trace ------------------------------------------------------------

// failure is a violated check; kind is stable across shrinking.
type failure struct {
	step int
	kind string
	msg  string
}

func (f *failure) Error() string { return fmt.Sprintf("step %d: %s: %s", f.step, f.kind, f.msg) }

func retryable(c endpoint.Code) bool {
	return c == endpoint.Aborted || c == endpoint.DeadlineExceeded || c == endpoint.Unavailable
}

func (g *rig) rpc(ctx context.Context, st Step) mResult {
	var res mResult
	var err error
	switch st.Op {
	case "CreateVolume":
		var v endpoint.Volume
		v, err = g.ep.CreateVolume(ctx, st.Req)
		res.Vol = endpoint.Volume{ID: v.ID, Kind: v.Kind, CapacityBytes: v.CapacityBytes, SourceType: v.SourceType, SourceID: v.SourceID}
	case "DeleteVolume":
		err = g.ep.DeleteVolume(ctx, st.ID)
	case "CreateSnapshot":
		var s endpoint.Snapshot
		s, err = g.ep.CreateSnapshot(ctx, st.Name, st.ID)
		res.Snap = endpoint.Snapshot{ID: s.ID, Name: s.Name, SourceVolumeID: s.SourceVolumeID, SizeBytes: s.SizeBytes, ReadyToUse: s.ReadyToUse}
	case "DeleteSnapshot":
		err = g.ep.DeleteSnapshot(ctx, st.ID)
	case "Reconcile":
		// as a caller must (ADR 0001 v2 D3): a dry run, then its plan;
		// a dry run mutates nothing, so fault indices are unchanged
		req := endpoint.ReconcileRequest{DesiredVolumeIDs: st.Desired, MinAge: time.Hour, DryRun: true}
		var p endpoint.ReconcilePlan
		if p, err = g.ep.Reconcile(ctx, req); err == nil {
			req.DryRun, req.PlanDigest = false, p.Digest
			p, err = g.ep.Reconcile(ctx, req)
		}
		for _, a := range p.Actions {
			res.Acts = append(res.Acts, a.Target+"|"+a.Reason)
		}
		sort.Strings(res.Acts)
	}
	res.Code = endpoint.CodeOf(err)
	if res.Code != endpoint.OK {
		res.Vol, res.Snap, res.Acts = endpoint.Volume{}, endpoint.Snapshot{}, nil
	}
	return res
}

func sameResult(a, b mResult) bool {
	return a.Code == b.Code && a.Vol == b.Vol && a.Snap == b.Snap && slices.Equal(a.Acts, b.Acts)
}

func (r mResult) String() string {
	switch {
	case r.Code != endpoint.OK:
		return fmt.Sprintf("code %d", r.Code)
	case r.Vol.ID != "":
		return fmt.Sprintf("OK %+v", r.Vol)
	case r.Snap.ID != "":
		return fmt.Sprintf("OK %+v", r.Snap)
	case r.Acts != nil:
		return fmt.Sprintf("OK acts=%v", r.Acts)
	}
	return "OK"
}

// diffWorld compares the world with the model's projection.
func diffWorld(world map[string]wrow, model map[string]string) string {
	var d []string
	for k, v := range model {
		if w, ok := world[k]; !ok {
			d = append(d, fmt.Sprintf("  missing %s: %s", k, v))
		} else if w.proj != v {
			d = append(d, fmt.Sprintf("  %s:\n    zfs:   %s\n    model: %s", k, w.proj, v))
		}
	}
	for k, w := range world {
		if _, ok := model[k]; !ok {
			d = append(d, fmt.Sprintf("  extra %s: %s", k, w.proj))
		}
	}
	sort.Strings(d)
	return strings.Join(d, "\n")
}

// adoptResidue: a failed, killed or raced request may leave one kind of
// garbage the contract allows, an orphaned clone-source snapshot on an
// owned volume (Reconcile's to collect). The model adopts what it finds.
func adoptResidue(m *Model, world map[string]wrow) {
	clones := map[string]bool{}
	for _, w := range world {
		if w.origin != "" {
			clones[w.origin] = true
		}
	}
	for k, w := range world {
		vol, _, _ := strings.Cut(k, "@")
		_, n, _ := strings.Cut(k, "@")
		if _, known := m.Snaps[k]; known || !w.snap || w.cloneFor == "" || n != internalPrefix+w.cloneFor || w.deferred || w.holds > 0 || clones[k] || !m.ownedVol(vol) {
			continue
		}
		m.Snaps[k] = &mSnap{Managed: true, CloneFor: w.cloneFor}
	}
}

// stats says what a run exercised, so a green run is not a vacuous one.
type stats struct {
	mu     sync.Mutex
	counts map[string]int
}

var runStats = &stats{counts: map[string]int{}}

func (s *stats) add(k string) {
	s.mu.Lock()
	s.counts[k]++
	s.mu.Unlock()
}

func (s *stats) report() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(s.counts)) {
		fmt.Fprintf(&b, "\n  %-60s %d", k, s.counts[k])
	}
	return b.String()
}

type runner struct {
	g         *rig
	m         *Model
	protected map[string]bool // identities (ident) of datasets the endpoint must never change
	masks     map[string]int
}

func (r *runner) protect(world map[string]wrow, names ...string) {
	for _, n := range names {
		for k, w := range world {
			if (k == n || strings.HasPrefix(k, n+"@") || strings.HasPrefix(k, n+"/")) && !(w.snap && isInternal(k)) {
				r.protected[ident(world, k)] = true
			}
		}
	}
}

// ident names a row across renames: its guid, and for a snapshot also its
// dataset's guid, since `zfs send | zfs recv` keeps a snapshot's guid and a
// received copy would otherwise collide with its source (run 40496125430).
func ident(world map[string]wrow, k string) string {
	id := world[k].guid
	if ds, _, ok := strings.Cut(k, "@"); ok {
		id += "@" + world[ds].guid
	}
	return id
}

// foreignSnapshot is the ident -> (name, projection) of protected rows.
func (r *runner) foreignSnapshot(world map[string]wrow) map[string]string {
	out := map[string]string{}
	for k, w := range world {
		if id := ident(world, k); r.protected[id] && !w.deferred && !w.owned {
			out[id] = k + " " + w.proj
		}
	}
	return out
}

func runTrace(tb testing.TB, g *rig, trace []Step) *failure {
	ctx := context.Background()
	r := &runner{g: g, m: newModel(), protected: map[string]bool{}, masks: map[string]int{}}
	for i, st := range trace {
		if f := r.step(ctx, i, st); f != nil {
			return f
		}
	}
	// end of trace: collect everything collectable, then nothing is left to do
	var desired []string
	for _, v := range slices.Sorted(maps.Keys(r.m.DS)) {
		if r.m.ownedVol(v) {
			desired = append(desired, v)
		}
	}
	if f := r.step(ctx, len(trace), Step{Op: "Reconcile", Desired: desired}); f != nil {
		return f
	}
	return nil
}

func (r *runner) step(ctx context.Context, i int, st Step) *failure {
	g, m := r.g, r.m
	fail := func(kind, format string, a ...any) *failure {
		return &failure{step: i, kind: kind, msg: st.String() + ": " + fmt.Sprintf(format, a...)}
	}
	if st.Op == "adm.recvsnap" && g.sim == nil {
		runStats.add("skipped on a real pool: adm.recvsnap")
		return nil
	}
	skip, drop, why := masked(m, st)
	if skip {
		runStats.add("masked: " + why)
		return nil
	}
	if drop {
		runStats.add("masked: " + why)
		st.Fault = Fault{}
	}
	pre, err := g.read(ctx)
	if err != nil {
		return fail("read", "%v", err)
	}

	if st.admin() {
		want := applyModel(m, st).Code == endpoint.OK
		got := g.adminStep(ctx, st)
		runStats.add(fmt.Sprintf("%s accepted=%v", st.Op, got))
		if got != want {
			return fail("admin", "zfs accepted=%v, model=%v", got, want)
		}
		if got {
			post, err := g.read(ctx)
			if err != nil {
				return fail("read", "%v", err)
			}
			switch st.Op {
			case "adm.snapshot", "adm.recvsnap":
				r.protect(post, st.ID+"@"+st.Name)
			case "adm.create", "adm.clone":
				r.protect(post, st.Name)
			case "adm.child":
				r.protect(post, st.ID+"/child")
			case "adm.recv":
				r.protect(post, st.ID+"@recv", st.Name)
			case "adm.rename":
				r.protect(post, st.Name)
			}
			if d := diffWorld(post, m.Project()); d != "" {
				return fail("world", "after admin action:\n%s", d)
			}
		}
		return nil
	}

	g.clock.Add(1)
	foreignBefore := r.foreignSnapshot(pre)
	pm := m.clone()
	rctx, cancel := context.WithCancel(ctx)
	raced := new(bool)
	raceOK := new(bool)
	g.fr.arm(st.Fault, cancel, func() {
		*raced = true
		*raceOK = g.adminStep(context.Background(), *st.Fault.Race)
	})
	got := g.rpc(rctx, st)
	cancel()
	g.fr.mu.Lock()
	fired, log := g.fr.fired, g.fr.log
	g.fr.f = Fault{}
	g.fr.mu.Unlock()

	runStats.add(fmt.Sprintf("%s -> %d", st.Op, got.Code))
	if fired {
		runStats.add(fmt.Sprintf("fault fired: %s on %s", faultNames[st.Fault.Kind], st.Op))
	}
	if f := r.checkCoverage(st, got, log); f != nil {
		f.step = i
		return f
	}

	// candidates for what should have happened
	type cand struct {
		m   *Model
		res mResult
	}
	var cands []cand
	switch {
	case *raced:
		a := pm.clone()
		if (applyModel(a, *st.Fault.Race).Code == endpoint.OK) == *raceOK {
			cands = append(cands, cand{a, applyModel(a, st)})
		}
		b := pm.clone()
		res := applyModel(b, st)
		if (applyModel(b, *st.Fault.Race).Code == endpoint.OK) == *raceOK {
			cands = append(cands, cand{b, res})
		}
		if st.Op == "DeleteVolume" { // the delete's own op order (DEL-06) is interleavable
			c := pm.clone()
			ok := false
			res := c.DeleteVolumeRaced(st.ID, st.Fault.At, func(m *Model) { ok = applyModel(m, *st.Fault.Race).Code == endpoint.OK })
			if res.Code >= 0 && ok == *raceOK {
				cands = append(cands, cand{c, res})
			}
		}
		if len(cands) == 0 {
			return fail("race", "the out-of-band action's outcome (%v) fits no linearization", *raceOK)
		}
	default:
		c := pm.clone()
		cands = append(cands, cand{c, applyModel(c, st)})
	}

	world, err := g.read(ctx)
	if err != nil {
		return fail("read", "%v", err)
	}
	if fired && st.Fault.Kind != race {
		switch {
		case st.Op == "Reconcile" && got.Code == endpoint.OK:
			// REC-12: a failed group is reported as a skip and the run goes
			// on; the caller's next reconcile finishes it (retried below)
		case st.Fault.Kind != errBefore && !retryable(got.Code):
			// a killed zfs command has an unknown outcome: never final
			return fail("killed-final", "killed at op %d, returned %s (want a retryable code)", st.Fault.At, got)
		case !retryable(got.Code) && got.Code != endpoint.OK:
			// a final code promises nothing changed
			if d := diffWorld(world, pm.Project()); d != "" {
				return fail("final-not-atomic", "returned final %s after changing the world:\n%s", got, d)
			}
			r.m = pm
			adoptResidue(r.m, world)
			return r.checkForeign(i, st, foreignBefore, world)
		}
	}
	// retries converge: the caller retries a retryable answer
	retried := false
	for n := 0; (retryable(got.Code) || n == 0 && fired && st.Op == "Reconcile") && n < 3; n++ {
		retried = true
		got = g.rpc(ctx, st)
		runStats.add("retry")
		if world, err = g.read(ctx); err != nil {
			return fail("read", "%v", err)
		}
	}
	var lastDiff string
	for _, c := range cands {
		if st.Op == "Reconcile" && (fired || retried) {
			// a retried reconcile reports only what was left to do
			c.res.Acts, got.Acts = nil, nil
		}
		if !sameResult(got, c.res) {
			lastDiff = fmt.Sprintf("returned %s, model %s", got, c.res)
			continue
		}
		cm := c.m
		if fired || got.Code != endpoint.OK || *raced {
			n := len(cm.Snaps)
			adoptResidue(cm, world)
			if len(cm.Snaps) > n {
				runStats.add("residue adopted (orphan clone source)")
			}
		}
		if d := diffWorld(world, cm.Project()); d != "" {
			lastDiff = "world differs from the model:\n" + d
			continue
		}
		r.m = cm
		if f := r.checkForeign(i, st, foreignBefore, world); f != nil {
			return f
		}
		if st.Op == "Reconcile" && got.Code == endpoint.OK {
			// reconcile twice gives an empty plan
			p, err := g.ep.Reconcile(ctx, endpoint.ReconcileRequest{DesiredVolumeIDs: st.Desired, MinAge: time.Hour, DryRun: true})
			if err != nil || len(p.Actions) > 0 {
				return fail("reconcile-twice", "second reconcile: %+v %v", p.Actions, err)
			}
		}
		return nil
	}
	kind := "result"
	if strings.HasPrefix(lastDiff, "world") {
		kind = "world"
	}
	if fired || *raced {
		kind += "-after-" + faultNames[st.Fault.Kind]
	}
	return fail(kind, "%s", lastDiff)
}

// checkForeign: nothing the endpoint does not own ever changes.
func (r *runner) checkForeign(i int, st Step, before map[string]string, world map[string]wrow) *failure {
	after := r.foreignSnapshot(world)
	for id, was := range before {
		if now, ok := after[id]; !ok || now != was {
			if !ok {
				now = "(gone)"
			}
			return &failure{step: i, kind: "foreign-changed", msg: fmt.Sprintf("%s: unowned %s -> %s", st, was, now)}
		}
	}
	return nil
}

// checkCoverage: no conclusion from a partial table. Every RPC that got
// past validation read its subject (the volume, or the whole parent), and
// CreateSnapshot (global names) and Reconcile (everything not wanted) read
// the whole parent before mutating.
func (r *runner) checkCoverage(st Step, got mResult, log [][]string) *failure {
	parent := r.g.parent
	covers := func(args []string, vol string) bool {
		if args[0] != "get" {
			return false
		}
		root := args[len(args)-1]
		whole := root == parent && (slices.Contains(args, "-r") || slices.Contains(args, "2"))
		return whole || vol != "" && root == parent+"/"+vol
	}
	var subjects []string
	switch st.Op {
	case "CreateVolume":
		subjects = append(subjects, st.Req.Name)
		if v, _, _ := strings.Cut(st.Req.SourceSnapshotID+st.Req.SourceVolumeID, "@"); v != "" {
			subjects = append(subjects, v)
		}
	case "DeleteVolume", "CreateSnapshot":
		subjects = []string{st.ID}
	case "DeleteSnapshot":
		v, _, _ := strings.Cut(st.ID, "@")
		subjects = []string{v}
	}
	if got.Code == endpoint.InvalidArgument || len(log) == 0 {
		return nil
	}
	for _, v := range subjects {
		if !validID(v) {
			continue
		}
		if !slices.ContainsFunc(log, func(a []string) bool { return covers(a, v) }) {
			return &failure{kind: "partial-table", msg: fmt.Sprintf("%s concluded %s without reading %s", st, got, v)}
		}
	}
	if st.Op == "CreateSnapshot" || st.Op == "Reconcile" {
		for j, a := range log {
			if mMutating[a[0]] && !slices.ContainsFunc(log[:j], func(a []string) bool { return covers(a, "") }) {
				return &failure{kind: "partial-table", msg: fmt.Sprintf("%s ran %v before reading the whole parent", st, a)}
			}
		}
	}
	return nil
}

// ---- shrinking ------------------------------------------------------------------

// shrink reduces a failing trace to a locally minimal one that fails with
// the same kind: drop chunks, then single steps, then faults.
func shrink(tb testing.TB, trace []Step, kind string, newG func() *rig, budget int) ([]Step, *failure) {
	runs := 0
	fails := func(t []Step) *failure {
		runs++
		f := runTrace(tb, newG(), t)
		if f != nil && f.kind == kind {
			return f
		}
		return nil
	}
	// a replay that fails another way is not this failure; none at all means
	// the failure did not reproduce, and the caller prints the trace as found
	best := fails(trace)
	if best == nil {
		tb.Logf("shrink: the failure (%s) did not reproduce on replay", kind)
		return trace, nil
	}
	for chunk := len(trace) / 2; chunk >= 1 && runs < budget; {
		progress := false
		for i := 0; i+chunk <= len(trace) && runs < budget; {
			cand := append(slices.Clone(trace[:i]), trace[i+chunk:]...)
			if f := fails(cand); f != nil {
				trace, best, progress = cand, f, true
				continue
			}
			i += chunk
		}
		if !progress {
			chunk /= 2
		}
	}
	for i := range trace {
		if trace[i].Fault.Kind == noFault || runs >= budget {
			continue
		}
		cand := slices.Clone(trace)
		cand[i].Fault = Fault{}
		if f := fails(cand); f != nil {
			trace, best = cand, f
		}
	}
	return trace, best
}

// ---- entry points ---------------------------------------------------------------

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return v
	}
	return def
}

// TestModel runs MODEL_SEQS seeded sequences of MODEL_LEN steps on the
// simulator (on a real pool: MODEL_BUDGET steps in all), in parallel, one
// parent dataset per sequence.
//
// The PR tier (ADR 0005) runs 64 sequences: about 1 s, 4 s under -race, on
// 4 cores; the nightly sweep (model_nightly_test.go) runs 20,000. On a real
// pool it runs MODEL_BUDGET (50) steps, at most 25 per sequence.
func TestModel(t *testing.T) {
	checkMaskExpiry(t)
	seqs, n := envInt("MODEL_SEQS", 64), envInt("MODEL_LEN", 40)
	if os.Getenv("ZFS_TEST_POOL") != "" {
		n = min(n, 25)
		seqs = max(1, envInt("MODEL_BUDGET", 50)/n)
	}
	if testing.Short() {
		seqs = min(seqs, 16)
	}
	seed0 := uint64(envInt("MODEL_SEED0", 1))
	if s := os.Getenv("MODEL_SEED"); s != "" {
		v, _ := strconv.ParseUint(s, 10, 64)
		seed0, seqs = v, 1
	}
	modelSweep(t, seqs, n, seed0)
}

// checkMaskExpiry: a mask is a known divergence with a deadline, never a
// permanent exemption.
func checkMaskExpiry(t *testing.T) {
	for _, k := range knownOpens {
		if exp, err := time.Parse(time.DateOnly, k.expires); err != nil || time.Now().After(exp) {
			t.Errorf("mask %s (%s) expired %s: fix the divergence, or renew it in review", k.id, k.row, k.expires)
		}
	}
}

// modelSweep runs sequences seed0 .. seed0+seqs-1 of n steps on
// MODEL_WORKERS workers, and reports the lowest failing seed, shrunk.
func modelSweep(t *testing.T, seqs, n int, seed0 uint64) {
	workers := envInt("MODEL_WORKERS", 8)
	var mu sync.Mutex
	var first *failure
	firstSeed := uint64(0)
	steps := 0
	start := time.Now()
	var detect time.Duration
	detectSteps := 0
	t.Run("seqs", func(t *testing.T) {
		for w := range workers {
			t.Run(fmt.Sprintf("w%d", w), func(t *testing.T) {
				t.Parallel()
				for seed := seed0 + uint64(w); seed < seed0+uint64(seqs); seed += uint64(workers) {
					mu.Lock()
					stop := first != nil
					mu.Unlock()
					if stop {
						return
					}
					trace := genTrace(rngSource{rand.New(rand.NewPCG(seed, 0x5eed))}, n)
					f := runTrace(t, newRig(t, fmt.Sprintf("s%d", seed)), trace)
					mu.Lock()
					steps += len(trace)
					if f != nil && first == nil {
						detect, detectSteps = time.Since(start), steps
					}
					if f != nil && (first == nil || seed < firstSeed) {
						first, firstSeed = f, seed
					}
					mu.Unlock()
				}
			})
		}
	})
	if testing.Verbose() {
		t.Log("exercised:" + runStats.report())
	}
	t.Logf("%d sequences x %d steps (%d steps) in %v", seqs, n, steps, time.Since(start).Round(time.Millisecond))
	if first == nil {
		return
	}
	// the divergence as first seen, before shrinking replays anything
	t.Logf("first divergence: seed %d step %d: %v", firstSeed, first.step, first)
	trace := genTrace(rngSource{rand.New(rand.NewPCG(firstSeed, 0x5eed))}, n)
	budget := envInt("MODEL_SHRINK", 400)
	if os.Getenv("ZFS_TEST_POOL") != "" {
		budget = envInt("MODEL_SHRINK", 30) // each replay is real zfs work
	}
	k := 0
	newG := func() *rig { k++; return newRig(t, fmt.Sprintf("shrink%d", k)) }
	small, f := shrink(t, trace[:min(first.step+1, len(trace))], first.kind, newG, budget)
	if f == nil {
		f = first
		small = trace
	}
	var b strings.Builder
	for i, st := range small {
		fmt.Fprintf(&b, "\n  %2d %s", i, st)
	}
	t.Fatalf("model check failed: detected after %v and %d steps; shrunk by %v (seed %d, replay: MODEL_SEED=%d go test ./internal/endpoint -run TestModel)\n%v\nminimal trace (%d of %d steps):%s",
		detect.Round(time.Millisecond), detectSteps, time.Since(start).Round(time.Millisecond), firstSeed, firstSeed, f, len(small), first.step+1, b.String())
}

// FuzzModel drives the same generator from fuzz bytes.
func FuzzModel(f *testing.F) {
	for _, s := range []string{"", "\x00\x01\x02\x03\x04\x05\x06\x07", "abcdefghijklmnopqrstuvwxyz0123456789"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		trace := genTrace(&byteSource{b: b}, min(len(b)/4+1, 30))
		// the simulator only: the fuzzer explores the generator's space, and
		// the seed corpus must not cost the real-pool leg a dataset
		if fl := runTrace(t, newSimRig(t), trace); fl != nil {
			t.Fatal(fl)
		}
	})
}

// A received snapshot keeps its source's guid, on a real pool and now on the
// simulator: the foreign-object check must not take the copy for its source
// (run 40496125430, seed 2, shrunk).
func TestModelReceivedSnapshotSharesGUID(t *testing.T) {
	t.Parallel()
	trace := []Step{
		{Op: "CreateVolume", Req: endpoint.CreateVolumeRequest{Name: "v2", Kind: endpoint.Filesystem, CapacityBytes: 2 << 20}},
		{Op: "adm.recv", ID: "v2", Name: "v1"},
	}
	if f := runTrace(t, newSimRig(t), trace); f != nil {
		t.Fatal(f)
	}
}
