package endpoint_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

// Crash injection, as a table: for every scenario and every mutating zfs
// call k of its operation, the process "dies" either before call k runs or
// after it ran but before its answer arrived. A fresh endpoint (a restart:
// no locks, no memory) then retries the same request. It must succeed, reach
// the end state of an uninterrupted run, and a further identical call must
// mutate nothing.
//
// faultRunner wraps whatever runner the harness uses, so the table runs on
// the simulator and, with ZFS_TEST_POOL, on a real pool: nothing here needs
// the simulator's fault hooks.

var errCrash = errors.New("injected crash")

// faultRunner counts mutating zfs calls and fails call crashAt (0-based).
//
// What follows a crash depends on dead. A dead process makes no further
// call: every call after its crash fails, reads included, so the op
// returns with what the crash left, and the retry (a fresh endpoint)
// resumes it: Resume, the backstop for a real crash. A process that
// survives (dead false: the crash was only a lost answer) goes on, and a
// fresh CreateVolume then rolls itself back (ADR 0010) before the retry
// creates afresh.
//
// again, when above crashAt, is a second crash point, in a process that
// survived the first: the mutation it makes there (in a rolled-back
// create, one of the rollback's) crashes too, and dead applies to that
// one.
type faultRunner struct {
	zfs.Runner
	crashAt int  // -1: never
	after   bool // run the call, then lose its answer
	dead    bool // the (last) crash kills the process
	again   int  // a second crash point, if > crashAt
	mu      sync.Mutex
	n       int
	crashed bool
	twice   bool // crashed at again too
	gone    bool // died: every call fails
	busy    bool // sim: Settle is a no-op, so a busy destroy's retry runs (crashRun.busy)
	settles int  // Settle calls that returned nil: busy destroys the endpoint retried (apply.go destroy)
}

// counted wraps the endpoint's Settle to count the calls that let a busy
// destroy retry (a nil return; apply.go).
func (f *faultRunner) counted(settle func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		err := settle(ctx)
		if err == nil {
			f.mu.Lock()
			f.settles++
			f.mu.Unlock()
		}
		return err
	}
}

func (f *faultRunner) settled() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.settles
}

func mutating(args []string) bool { return len(args) > 0 && args[0] != "get" && args[0] != "list" }

// step counts one call (a mutation when mut) and says whether it is one to
// crash at, or whether the process is already gone.
func (f *faultRunner) step(mut bool) (crash, gone bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gone {
		return false, true
	}
	if !mut {
		return false, false
	}
	second := f.crashAt >= 0 && f.again > f.crashAt && f.n == f.again
	crash = f.n == f.crashAt || second
	f.crashed = f.crashed || crash
	f.twice = f.twice || second
	if crash && f.dead && (second || f.again <= f.crashAt || f.crashAt < 0) {
		f.gone = true
	}
	f.n++
	return crash, false
}

// faultRoot puts the endpoint's root owner and mode change (not a zfs
// call) into the same count: it is a mutation a crash can precede or
// follow like any other.
type faultRoot struct {
	endpoint.RootSetter
	f *faultRunner
}

func (r faultRoot) SetRoot(ctx context.Context, dataset, mountpoint string, uid, gid, mode int64) error {
	crash, gone := r.f.step(true)
	if gone || crash && !r.f.after {
		return errCrash
	}
	err := r.RootSetter.SetRoot(ctx, dataset, mountpoint, uid, gid, mode)
	if crash {
		return errCrash
	}
	return err
}

func (f *faultRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	crash, gone := f.step(mutating(args))
	lost := &zfs.ExitError{Args: args, Code: -1, Underlay: errCrash}
	if gone || crash && !f.after {
		return nil, lost
	}
	out, err := f.Runner.Run(ctx, name, args...)
	if crash {
		return nil, lost
	}
	return out, err
}

func (f *faultRunner) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

type crashCase struct {
	name  string
	late  bool // the endpoint's clock is two days ahead (reconcile)
	nfs   bool // needs exports to really happen
	setup func(context.Context, *endpoint.Endpoint, *harness) error
	op    func(context.Context, *endpoint.Endpoint) error
	// xfail: known divergences by subtest ("k0-after"), with the reason. A
	// listed crash point that converges fails the test: the fix must remove
	// its row, so the list only shrinks (a ratchet).
	xfail map[string]string
	// wrap, when set, puts a runner of the case's own between the endpoint
	// and the pool (a fault the op needs; TestDestroyNeverHitsAReplacedDataset)
	wrap func(zfs.Runner) zfs.Runner
}

const cmib = 1 << 20

func mk(reqs ...endpoint.CreateVolumeRequest) func(context.Context, *endpoint.Endpoint, *harness) error {
	return func(ctx context.Context, ep *endpoint.Endpoint, _ *harness) error {
		for _, r := range reqs {
			if _, err := ep.CreateVolume(ctx, r); err != nil {
				return err
			}
		}
		return nil
	}
}

func then(fs ...func(context.Context, *endpoint.Endpoint, *harness) error) func(context.Context, *endpoint.Endpoint, *harness) error {
	return func(ctx context.Context, ep *endpoint.Endpoint, h *harness) error {
		for _, f := range fs {
			if err := f(ctx, ep, h); err != nil {
				return err
			}
		}
		return nil
	}
}

func snapOf(name, vol string) func(context.Context, *endpoint.Endpoint, *harness) error {
	return func(ctx context.Context, ep *endpoint.Endpoint, _ *harness) error {
		_, err := ep.CreateSnapshot(ctx, name, vol)
		return err
	}
}

func createOp(r endpoint.CreateVolumeRequest) func(context.Context, *endpoint.Endpoint) error {
	return func(ctx context.Context, ep *endpoint.Endpoint) error { _, err := ep.CreateVolume(ctx, r); return err }
}

var crashCases = []crashCase{
	{name: "create-fs", op: createOp(endpoint.CreateVolumeRequest{Name: "v", CapacityBytes: 64 * cmib})},
	{name: "create-fs-reserve", op: createOp(endpoint.CreateVolumeRequest{Name: "v", CapacityBytes: 64 * cmib, Reserve: true})},
	{name: "create-fs-props", op: createOp(endpoint.CreateVolumeRequest{Name: "v", Properties: map[string]string{"compression": "zstd", "recordsize": "16K", "atime": "off"}})},
	{name: "create-zvol-sparse", op: createOp(endpoint.CreateVolumeRequest{Name: "v", Kind: endpoint.Block, CapacityBytes: 64 * cmib, Sparse: true})},
	{name: "create-zvol-thick", op: createOp(endpoint.CreateVolumeRequest{Name: "v", Kind: endpoint.Block, CapacityBytes: 64 * cmib})},
	{name: "restore-fs-grow",
		setup: then(mk(endpoint.CreateVolumeRequest{Name: "src", CapacityBytes: 16 * cmib}), snapOf("s", "src")),
		op:    createOp(endpoint.CreateVolumeRequest{Name: "r", CapacityBytes: 32 * cmib, SourceSnapshotID: "src@s"})},
	{name: "restore-fs-reserve",
		setup: then(mk(endpoint.CreateVolumeRequest{Name: "src", CapacityBytes: 16 * cmib}), snapOf("s", "src")),
		op:    createOp(endpoint.CreateVolumeRequest{Name: "r", SourceSnapshotID: "src@s", Reserve: true})},
	{name: "restore-zvol-grow",
		setup: then(mk(endpoint.CreateVolumeRequest{Name: "src", Kind: endpoint.Block, CapacityBytes: 16 * cmib, Sparse: true}), snapOf("s", "src")),
		op:    createOp(endpoint.CreateVolumeRequest{Name: "r", Kind: endpoint.Block, Sparse: true, CapacityBytes: 32 * cmib, SourceSnapshotID: "src@s"})},
	{name: "clone-volume-fs-reserve",
		setup: mk(endpoint.CreateVolumeRequest{Name: "src", CapacityBytes: 16 * cmib}),
		op:    createOp(endpoint.CreateVolumeRequest{Name: "c", SourceVolumeID: "src", Reserve: true})},
	{name: "clone-volume-zvol-thick",
		setup: mk(endpoint.CreateVolumeRequest{Name: "src", Kind: endpoint.Block, CapacityBytes: 16 * cmib}),
		op:    createOp(endpoint.CreateVolumeRequest{Name: "c", Kind: endpoint.Block, CapacityBytes: 16 * cmib, SourceVolumeID: "src"})},
	{name: "snapshot",
		setup: mk(endpoint.CreateVolumeRequest{Name: "v", CapacityBytes: 16 * cmib}),
		op: func(ctx context.Context, ep *endpoint.Endpoint) error {
			_, err := ep.CreateSnapshot(ctx, "s", "v")
			return err
		}},
	{name: "expand-zvol",
		setup: mk(endpoint.CreateVolumeRequest{Name: "v", Kind: endpoint.Block, CapacityBytes: 16 * cmib, Sparse: true}),
		op: func(ctx context.Context, ep *endpoint.Endpoint) error {
			_, err := ep.ExpandVolume(ctx, "v", 32*cmib, 0)
			return err
		}},
	{name: "delete-volume",
		setup: mk(endpoint.CreateVolumeRequest{Name: "v", CapacityBytes: 16 * cmib}),
		op:    func(ctx context.Context, ep *endpoint.Endpoint) error { return ep.DeleteVolume(ctx, "v") }},
	{name: "delete-clone-releases-source",
		setup: mk(endpoint.CreateVolumeRequest{Name: "src", CapacityBytes: 16 * cmib}, endpoint.CreateVolumeRequest{Name: "c", SourceVolumeID: "src"}),
		op:    func(ctx context.Context, ep *endpoint.Endpoint) error { return ep.DeleteVolume(ctx, "c") },
		// wave7 finding: the plan is destroy c, then destroy -d src@volume-source-for-volume-c.
		// Dying between them leaves the snapshot; the retry finds c gone and
		// plans nothing (OK), so only Reconcile's orphan rule collects it.
		// was xfail (k0-after, k1-before) before 07c3f98: the deferred
		// origin is now released first, so no crash point orphans it
	},
	{name: "delete-restored-clone",
		setup: then(mk(endpoint.CreateVolumeRequest{Name: "src", CapacityBytes: 16 * cmib}), snapOf("s", "src"),
			mk(endpoint.CreateVolumeRequest{Name: "r", SourceSnapshotID: "src@s"}),
			func(ctx context.Context, ep *endpoint.Endpoint, _ *harness) error {
				return ep.DeleteSnapshot(ctx, "src@s")
			}),
		op: func(ctx context.Context, ep *endpoint.Endpoint) error { return ep.DeleteVolume(ctx, "r") }},
	// N11: the root's owner and mode are set before the commit, by the
	// resume, on every pass: a crash on either side of the change converges
	{name: "create-nfs-root", nfs: true, op: createOp(endpoint.CreateVolumeRequest{Name: "v", CapacityBytes: 16 * cmib,
		NFS: &endpoint.NFSExport{Clients: []string{"10.20.0.0/24"}, UID: u32(1000), GID: u32(2000), Mode: u32(0o2775)}})},
	{name: "restore-nfs-root-mode", nfs: true,
		setup: then(mk(endpoint.CreateVolumeRequest{Name: "src", NFS: &endpoint.NFSExport{Clients: []string{"10.20.0.0/24"}, UID: u32(1000)}}),
			snapOf("s", "src")),
		op: createOp(endpoint.CreateVolumeRequest{Name: "r", SourceSnapshotID: "src@s",
			NFS: &endpoint.NFSExport{Clients: []string{"10.20.0.0/24"}, Mode: u32(0o700)}})},
	{name: "publish", nfs: true,
		setup: mk(endpoint.CreateVolumeRequest{Name: "v"}),
		op: func(ctx context.Context, ep *endpoint.Endpoint) error {
			_, err := ep.PublishFileExport(ctx, "v", "10.0.0.1", false)
			return err
		}},
	{name: "reconcile", late: true,
		setup: mk(endpoint.CreateVolumeRequest{Name: "keep"}, endpoint.CreateVolumeRequest{Name: "drop"},
			endpoint.CreateVolumeRequest{Name: "src"}, endpoint.CreateVolumeRequest{Name: "c", SourceVolumeID: "src"}),
		op: func(ctx context.Context, ep *endpoint.Endpoint) error {
			// as a caller must (ADR 0001 v2 D3): dry run, then apply its plan
			req := endpoint.ReconcileRequest{DesiredVolumeIDs: []string{"keep"}, DryRun: true}
			dry, err := ep.Reconcile(ctx, req)
			if err != nil {
				return err
			}
			req.DryRun, req.PlanDigest = false, dry.Digest
			_, err = ep.Reconcile(ctx, req)
			return err
		}},
}

// fingerprint is the parent's observable state: every dataset and snapshot
// below it with the properties the endpoint decides, names relative to the
// parent, sorted. creation, guid and space accounting are left out.
func fingerprint(t *testing.T, h *harness) string {
	t.Helper()
	p := endpoint.DefaultPropertyPrefix + ":"
	props := []string{"type", "volsize", "refquota", "refreservation", "origin", "sharenfs", "compression", "recordsize", "mountpoint", "mounted",
		p + "managed_resource", p + "provision_success", p + "csi_volume_name", p + "csi_snapshot_name", p + "nfs_clients",
		p + "csi_volume_content_source_id"}
	// one call reads the parent too: its row is the parent's mountpoint,
	// and the fork a second get would cost (ADR 0005)
	cols, err := h.Z.ListColumns(context.Background(), h.Parent, zfs.ListOptions{
		Types: []zfs.Type{zfs.TypeFilesystem, zfs.TypeVolume, zfs.TypeSnapshot}, Depth: -1, Props: props})
	if err != nil {
		t.Fatal(err)
	}
	var ds []zfs.Dataset
	parentMP := ""
	for i, n := range cols.Names {
		if n == h.Parent {
			// a mountpoint names the parent's, which differs per run on a pool
			parentMP = cols.Cell(i, cols.Col("mountpoint")).Value
			continue
		}
		ds = append(ds, cols.Dataset(i))
	}
	if parentMP == "" {
		t.Fatalf("%s is not listed", h.Parent)
	}
	var lines []string
	for _, d := range ds {
		var kv []string
		for _, k := range props {
			v := d.Props[k]
			if v.Value == "" || v.Value == "-" {
				continue
			}
			s := k + "=" + strings.TrimPrefix(v.Value, h.Parent+"/")
			if strings.HasPrefix(k, p) && !v.Local() {
				// a source names the parent, which differs per run
				s += "(" + strings.ReplaceAll(v.Source, h.Parent+"/", "") + ")"
			}
			if k == "mountpoint" {
				// pinned (local) or following the parent's
				s = k + "=" + strings.Replace(v.Value, parentMP, "$PARENT", 1) + "(" + map[bool]string{true: "local", false: "inherited"}[v.Local()] + ")"
			}
			kv = append(kv, s)
		}
		if d.Props["mounted"].Value == "yes" {
			// the root directory's owner and mode (N11)
			r := h.rootOf(t, d.Name, d.Props["mountpoint"].Value)
			kv = append(kv, fmt.Sprintf("root=%d:%d:%04o", r.UID, r.GID, r.Mode))
		}
		lines = append(lines, strings.TrimPrefix(d.Name, h.Parent+"/")+" "+strings.Join(kv, " "))
	}
	slices.Sort(lines)
	return strings.Join(lines, "\n")
}

func crashEndpoint(t *testing.T, h *harness, r zfs.Runner, late bool) *endpoint.Endpoint {
	t.Helper()
	cfg := endpoint.Config{Parent: h.Parent, LockWait: time.Second, FileExport: true, NFSServers: []string{"192.0.2.10"}}
	cfg.Root = endpoint.RootSetter(endpoint.LocalRoot{})
	if h.sim != nil {
		cfg.Etab, cfg.Root = h.sim, h.sim
		if f, ok := r.(*faultRunner); ok && f.busy {
			// a busy destroy retries only after a settle (apply.go)
			cfg.Settle = func(context.Context) error { return nil }
		}
	} else {
		// as cmd/zfs-endpoint: a rollback destroys a zvol udev may
		// still be probing
		cfg.Settle = endpoint.UdevSettle
	}
	if f, ok := r.(*faultRunner); ok {
		cfg.Root = faultRoot{cfg.Root, f}
		if cfg.Settle != nil {
			cfg.Settle = f.counted(cfg.Settle)
		}
	}
	if late {
		cfg.Now = func() time.Time { return time.Now().Add(48 * time.Hour) }
	}
	ep, err := endpoint.New(zfs.New(r), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return ep
}

// crashRun says where runCrash crashes: at mutating call at (-1: none),
// before or after it runs; whether the process survives that crash (see
// faultRunner); and again (> at), a second crash point in a survivor,
// which it then survives or not as dies says.
type crashRun struct {
	at       int
	after    bool
	survives bool
	again    int
	dies     bool
	busy     bool // sim: the faulted run's first destroy fails busy once
}

// runCrash sets the scenario up, runs its op with r's crashes, restarts
// and retries, checks the retry is a no-op, and returns the end state, how
// many mutating calls the faulted run made, and how many of those busy
// destroys it retried (settles: see faultRunner.counted). reached is false
// when the run never got to r.again: nothing is checked then.
func runCrash(t *testing.T, c crashCase, r crashRun) (state string, n, settles int, reached bool) {
	t.Helper()
	ctx := context.Background()
	ep0, h := setupNFS(t, "192.0.2.10") // as crashEndpoint
	if c.nfs {
		h.NFS(t)
	}
	if c.setup != nil {
		if err := c.setup(ctx, ep0, h); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	twice := r.at >= 0 && r.again > r.at
	fr := &faultRunner{Runner: h.Z.Runner, crashAt: r.at, after: r.after, dead: !r.survives || twice && r.dies, again: r.again, busy: r.busy}
	var sim *zfstest.Sim
	if r.busy {
		if h.sim == nil {
			t.Fatal("busy injection needs the simulator")
		}
		sim = h.sim
		hit := false
		sim.Fail = func(a []string) error {
			if a[0] == "destroy" && !hit {
				hit = true
				return &zfs.ExitError{Code: 1, Stderr: "cannot destroy: dataset is busy\n"}
			}
			return nil
		}
	}
	err := c.op(ctx, crashEndpoint(t, h, fr, c.late))
	if sim != nil {
		sim.Fail = nil
	}
	switch {
	case r.at < 0 && err != nil:
		t.Fatalf("uninterrupted run: %v", err)
	case r.at >= 0 && !fr.crashed:
		t.Fatalf("op made only %d mutating calls, never reached call %d", fr.count(), r.at)
	case twice && !fr.twice:
		return "", fr.count(), fr.settled(), false
	}
	// restart and retry, as the CO would
	if r.at >= 0 {
		retry := crashEndpoint(t, h, h.Z.Runner, c.late)
		for i := 0; i < 3; i++ {
			if err = c.op(ctx, retry); err == nil {
				break
			}
		}
		if err != nil {
			t.Fatalf("retry after the crash did not converge: %v", err)
		}
	}
	idle := &faultRunner{Runner: h.Z.Runner, crashAt: -1}
	if err := c.op(ctx, crashEndpoint(t, h, idle, c.late)); err != nil || idle.count() != 0 {
		t.Fatalf("a repeat after success: %v, %d mutating calls, want none", err, idle.count())
	}
	return fingerprint(t, h), fr.count(), fr.settled(), true
}

// crashSample: on a real pool the PR leg runs one crash point per case,
// rotated by the case's index over its points (k, before or after, the
// process dead or surviving), besides its uninterrupted run: the full
// sweep costs ~29s of the VM's 2 vCPUs, which ADR 0005's budget cannot
// carry. The simulator leg and ZFS_CRASH_FULL=1 run every point, and the
// rollback's own crash points (fullSweep).
func crashSample(ci, k int, after, survives bool, n int) bool {
	if fullSweep() {
		return true
	}
	i := 4 * k
	if after {
		i += 2
	}
	if survives {
		i++
	}
	return i == ci%(4*n)
}

func fullSweep() bool { return os.Getenv("ZFS_TEST_POOL") == "" || os.Getenv("ZFS_CRASH_FULL") != "" }

// Every crash point k of every case, before and after the call runs, in
// two process models:
//   - kK-before, kK-after: the process dies with the crash; the retry
//     resumes what it left (Resume, the backstop for a real crash);
//   - kK-before-survives, kK-after-survives: the crash only lost an
//     answer, and the process goes on: a fresh CreateVolume rolls itself
//     back (ADR 0010), and the retry creates afresh.
//
// Both must reach the uninterrupted run's end state. A survivor that
// rolled back is crashed once more at each of the rollback's own
// mutations (…/rollback-crashes: dying there, and surviving with the
// rollback failed), which must converge too.
func TestCrashAtEveryMutationConverges(t *testing.T) {
	for ci, c := range crashCases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel() // each run has its own parent dataset (sim: its own Sim)
			var want string
			var n int
			t.Run("uninterrupted", func(t *testing.T) { want, n, _, _ = runCrash(t, c, crashRun{at: -1}) })
			if want == "" && n == 0 {
				t.Skip("uninterrupted run did not complete")
			}
			for k := 0; k < n; k++ {
				for _, after := range []bool{false, true} {
					for _, survives := range []bool{false, true} {
						mode := "before"
						if after {
							mode = "after"
						}
						label := fmt.Sprintf("k%d-%s", k, mode)
						if survives {
							label += "-survives"
						}
						if !crashSample(ci, k, after, survives, n) {
							continue
						}
						t.Run(label, func(t *testing.T) {
							t.Parallel()
							got, m, s, _ := runCrash(t, c, crashRun{at: k, after: after, survives: survives})
							if why, known := c.xfail[label]; known {
								if got == want {
									t.Fatalf("known divergence (%s) now converges: remove it from xfail", why)
								}
								// logged, not skipped: a skip would break the real
								// leg's skip budget (ci/check-skips.sh)
								t.Logf("XFAIL known divergence: %s", why)
								return
							}
							if got != want {
								t.Errorf("end state after a crash differs from the uninterrupted run:\n--- uninterrupted\n%s\n--- crash at %d (%s)\n%s", want, k, label, got)
							}
							if !survives || !fullSweep() {
								return
							}
							checkSecondPoints(t, c, want, k, after, m, s)
						})
					}
				}
			}
		})
	}
}

// checkSecondPoints crashes a survivor (of a crash at k, after or not) again
// at each later mutation j of its run, up to its m mutations, and checks
// each end state is want. m and s are the faulted run's mutations and busy
// retries (runCrash). A busy retry (apply.go destroy) is one call the
// faulted run makes and a run without it does not, so the last points of
// that run may not be reached when the second run has fewer retries: that
// shortfall, and nothing else, is accepted.
func checkSecondPoints(t *testing.T, c crashCase, want string, k int, after bool, m, s int) {
	t.Helper()
	for j := k + 1; j < m; j++ {
		for _, dies := range []bool{true, false} {
			r := crashRun{at: k, after: after, survives: true, again: j, dies: dies}
			got, n2, s2, reached := runCrash(t, c, r)
			if !reached && !(s > s2 && m-n2 <= s-s2) {
				t.Fatalf("crash at %d then %d: the second point was not reached", k, j)
			}
			if !reached {
				continue
			}
			if got != want {
				t.Errorf("end state after a crash at %d (after %v), then at %d (dies %v), differs from the uninterrupted run:\n--- uninterrupted\n%s\n--- crashed\n%s",
					k, after, j, dies, want, got)
			}
		}
	}
}

// TestCrashSecondPointAllowsBusyRetry is the sweep's create-zvol-sparse
// k1-before-survives case on the simulator, with the rollback's destroy busy
// once (nightly run 40853558021): that run makes one more call than a clean
// run, and its second crash points must still converge.
func TestCrashSecondPointAllowsBusyRetry(t *testing.T) {
	t.Parallel()
	requireSim(t)
	var c crashCase
	for _, x := range crashCases {
		if x.name == "create-zvol-sparse" {
			c = x
		}
	}
	want, _, _, _ := runCrash(t, c, crashRun{at: -1})
	_, clean, _, _ := runCrash(t, c, crashRun{at: 1, survives: true})
	got, m, s, _ := runCrash(t, c, crashRun{at: 1, survives: true, busy: true})
	if s != 1 || m != clean+1 {
		t.Fatalf("busy first run: %d busy retries and %d calls, want 1 and %d", s, m, clean+1)
	}
	if got != want {
		t.Errorf("end state after a busy rollback then a crash differs from the uninterrupted run:\n--- uninterrupted\n%s\n--- crashed\n%s", want, got)
	}
	checkSecondPoints(t, c, want, 1, false, m, s)
}

// OWN-15: between a plan and its destroy, the target is renamed away and
// another dataset made under its name, carrying the same ownership
// markers. The destroy must not hit it: the endpoint checks the guid the
// plan read right before each destroy and aborts on a mismatch.
//
// swapRunner does the swap for the at-th destroy of the run (0-based),
// before the first command that touches its target: the guid check, or
// the destroy itself where no check runs.
type swapRunner struct {
	zfs.Runner
	t        *testing.T
	z        *zfs.Client
	at, n    int
	swapped  string // the target swapped, "" until then
	moved    string // where the original went
	impostor string // the guid of the dataset made in its place
}

func (s *swapRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	target := ""
	switch {
	case len(args) > 1 && args[0] == "destroy":
		target = args[len(args)-1]
	// a single dataset's Get (the guid check), not a scan or a List: those
	// name their types with -t
	case len(args) > 1 && args[0] == "get" && !slices.Contains(args, "-t") && slices.Contains(args, "guid"):
		target = args[len(args)-1]
	}
	if target != "" && s.swapped == "" && s.n == s.at {
		s.swap(ctx, target)
	}
	if len(args) > 0 && args[0] == "destroy" {
		s.n++
	}
	return s.Runner.Run(ctx, name, args...)
}

// ownership markers an impostor copies from the dataset it replaces
var swapMarkers = func() []string {
	p := endpoint.DefaultPropertyPrefix + ":"
	return []string{p + "managed_resource", p + "provision_success", p + "csi_volume_name", p + "csi_snapshot_name",
		p + "csi_snapshot_source_volume_id", p + "clone_source_for"}
}()

func (s *swapRunner) markers(ctx context.Context, name string) []string {
	d, err := s.z.Get(ctx, name, swapMarkers...)
	if err != nil {
		s.t.Fatalf("swap: read %s: %v", name, err)
	}
	var out []string
	for _, k := range swapMarkers {
		if v := d.Props[k]; v.Local() {
			out = append(out, "-o", k+"="+v.Value)
		}
	}
	return out
}

func (s *swapRunner) zfs(ctx context.Context, args ...string) {
	if _, err := s.Runner.Run(ctx, "zfs", args...); err != nil {
		s.t.Fatalf("swap: zfs %v: %v", args, err)
	}
}

func (s *swapRunner) swap(ctx context.Context, target string) {
	vol, snap, isSnap := strings.Cut(target, "@")
	volMarks := s.markers(ctx, vol)
	var snapMarks []string
	if isSnap {
		snapMarks = s.markers(ctx, target)
	}
	s.swapped, s.moved = target, vol+"-moved"
	s.zfs(ctx, "rename", vol, s.moved)
	s.zfs(ctx, append(append([]string{"create", "-o", "canmount=noauto"}, volMarks...), vol)...)
	if isSnap {
		s.moved += "@" + snap
		s.zfs(ctx, append(append([]string{"snapshot"}, snapMarks...), target)...)
	}
	d, err := s.z.Get(ctx, target, "guid")
	if err != nil {
		s.t.Fatal(err)
	}
	s.impostor = d.Props["guid"].Value
}

var swapCases = []crashCase{
	crashCaseNamed("delete-volume"),
	crashCaseNamed("delete-clone-releases-source"),
	crashCaseNamed("delete-restored-clone"),
	crashCaseNamed("reconcile"),
	{name: "delete-snapshot",
		setup: then(mk(endpoint.CreateVolumeRequest{Name: "v", CapacityBytes: 16 * cmib}), snapOf("s", "v")),
		op: func(ctx context.Context, ep *endpoint.Endpoint) error {
			snaps, _, err := ep.ListSnapshots(ctx, "", "v", 0, "")
			if err != nil || len(snaps) != 1 {
				return fmt.Errorf("snapshots %v: %v", snaps, err)
			}
			return ep.DeleteSnapshot(ctx, snaps[0].ID)
		}},
	// ADR 0010: a fresh clone whose commit fails is rolled back, by
	// planDelete's destroys (the released origin, then the clone), each
	// tied to the guid its re-scan read. Unswapped, the rollback succeeds
	// and the op reports the failure's own code (Internal), which it
	// accepts; swapped, the rollback is Aborted, joined to the failure.
	{name: "rollback-create",
		setup: mk(endpoint.CreateVolumeRequest{Name: "src", CapacityBytes: 16 * cmib}),
		wrap:  func(r zfs.Runner) zfs.Runner { return failCommit{r} },
		op: func(ctx context.Context, ep *endpoint.Endpoint) error {
			_, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "c", SourceVolumeID: "src"})
			if endpoint.CodeOf(err) == endpoint.Internal && strings.Contains(err.Error(), "rolled back") {
				return nil
			}
			return err
		}},
}

// failCommit fails every commit (`zfs set …:provision_success=true`) with
// an unclassified error, on any runner.
type failCommit struct{ zfs.Runner }

func (r failCommit) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if isSetOf(":provision_success=true")(args) {
		return nil, &zfs.ExitError{Args: args, Code: 1, Stderr: "boom\n"}
	}
	return r.Runner.Run(ctx, name, args...)
}

func crashCaseNamed(name string) crashCase {
	for _, c := range crashCases {
		if c.name == name {
			return c
		}
	}
	panic("no crash case " + name)
}

// swapSweep: how many destroys of a case to swap, -1 for every one. On a
// real pool the PR leg swaps only the first destroy of each case: every case
// still reaches the guard, and the guard is one code path for every
// destroy, while the full sweep (19 runs) cost 19-25 test-seconds on the
// VM's one shard (ADR 0005). The simulator leg and ZFS_CRASH_FULL=1
// (nightly) swap every destroy.
func swapSweep() int {
	if os.Getenv("ZFS_TEST_POOL") == "" || os.Getenv("ZFS_CRASH_FULL") != "" {
		return -1
	}
	return 1
}

// destroySweepCap bounds TestDestroyNeverHitsAReplacedDataset's sweep: no
// case makes this many destroys, so reaching it is a failure, not a loop.
const destroySweepCap = 64

// destroyRunTimeout bounds one swapped run, so a regression that wedges an
// op fails the named subtest instead of the binary's -timeout.
const destroyRunTimeout = 60 * time.Second

func TestDestroyNeverHitsAReplacedDataset(t *testing.T) {
	sweep := swapSweep()
	for _, c := range swapCases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			for k := 0; ; k++ {
				if k == destroySweepCap {
					t.Fatalf("still swapping at destroy %d: the sweep does not end", k)
				}
				done, ran := false, false
				t.Run(fmt.Sprintf("destroy%d", k), func(t *testing.T) {
					ran = true
					ctx, cancel := context.WithTimeout(context.Background(), destroyRunTimeout)
					defer cancel()
					ep0, h := setupNFS(t, "192.0.2.10")
					if c.setup != nil {
						if err := c.setup(ctx, ep0, h); err != nil {
							t.Fatalf("setup: %v", err)
						}
					}
					run := h.Z.Runner
					if c.wrap != nil {
						run = c.wrap(run)
					}
					sw := &swapRunner{Runner: run, t: t, z: h.Z, at: k}
					err := c.op(ctx, crashEndpoint(t, h, sw, c.late))
					if sw.swapped == "" {
						done = true // the run made fewer destroys
						if err != nil {
							t.Fatalf("unswapped run: %v", err)
						}
						return
					}
					d, gerr := h.Z.Get(ctx, sw.swapped, "guid")
					if gerr != nil || d.Props["guid"].Value != sw.impostor {
						t.Errorf("the dataset made in place of %s was destroyed (%v)", sw.swapped, gerr)
					}
					if ok, _ := h.Z.Exists(ctx, sw.moved); !ok {
						t.Errorf("the original, renamed to %s, is gone", sw.moved)
					}
					// Reconcile reports a failed group as skipped, not as an error
					if c.name != "reconcile" && endpoint.CodeOf(err) != endpoint.Aborted {
						t.Errorf("op after the swap of %s: %v (code %d), want Aborted", sw.swapped, err, endpoint.CodeOf(err))
					}
				})
				// t.Run returns without running f under -failfast after a
				// failure elsewhere, or when -run filters destroy<k> out:
				// the sweep ends there instead of spinning
				if !ran {
					return
				}
				if done || t.Failed() {
					if k == 0 && done {
						t.Fatal("the op made no destroy")
					}
					return
				}
				if sweep >= 0 && k+1 >= sweep {
					return
				}
			}
		})
	}
}
