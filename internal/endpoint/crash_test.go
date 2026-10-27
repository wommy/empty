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
type faultRunner struct {
	zfs.Runner
	crashAt int  // -1: never
	after   bool // run the call, then lose its answer
	mu      sync.Mutex
	n       int
	crashed bool
}

func mutating(args []string) bool { return len(args) > 0 && args[0] != "get" && args[0] != "list" }

// step counts one mutation and says whether it is the one to crash at.
func (f *faultRunner) step() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	crash := f.n == f.crashAt
	f.crashed = f.crashed || crash
	f.n++
	return crash
}

// faultRoot puts the endpoint's root owner and mode change (not a zfs
// call) into the same count: it is a mutation a crash can precede or
// follow like any other.
type faultRoot struct {
	endpoint.RootSetter
	f *faultRunner
}

func (r faultRoot) SetRoot(ctx context.Context, dataset, mountpoint string, uid, gid, mode int64) error {
	crash := r.f.step()
	if crash && !r.f.after {
		return errCrash
	}
	err := r.RootSetter.SetRoot(ctx, dataset, mountpoint, uid, gid, mode)
	if crash {
		return errCrash
	}
	return err
}

func (f *faultRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	crash := mutating(args) && f.step()
	lost := &zfs.ExitError{Args: args, Code: -1, Underlay: errCrash}
	if crash && !f.after {
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
			_, err := ep.Reconcile(ctx, endpoint.ReconcileRequest{DesiredVolumeIDs: []string{"keep"}})
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
	ds, err := h.Z.List(context.Background(), h.Parent, zfs.ListOptions{
		Types: []zfs.Type{zfs.TypeFilesystem, zfs.TypeVolume, zfs.TypeSnapshot}, Depth: -1, Props: props})
	if err != nil {
		t.Fatal(err)
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
			kv = append(kv, s)
		}
		if d.Props["mounted"].Value == "yes" && d.Name != h.Parent {
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
	}
	if f, ok := r.(*faultRunner); ok {
		cfg.Root = faultRoot{cfg.Root, f}
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

// runCrash sets the scenario up, runs its op with a crash at mutating call
// crashAt (-1: none), restarts and retries, checks the retry is a no-op, and
// returns the end state and how many mutating calls the faulted run made.
func runCrash(t *testing.T, c crashCase, crashAt int, after bool) (string, int) {
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
	fr := &faultRunner{Runner: h.Z.Runner, crashAt: crashAt, after: after}
	err := c.op(ctx, crashEndpoint(t, h, fr, c.late))
	switch {
	case crashAt < 0 && err != nil:
		t.Fatalf("uninterrupted run: %v", err)
	case crashAt >= 0 && !fr.crashed:
		t.Fatalf("op made only %d mutating calls, never reached call %d", fr.count(), crashAt)
	}
	// restart and retry, as the CO would
	if crashAt >= 0 {
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
	return fingerprint(t, h), fr.count()
}

// crashSample: on a real pool the PR leg runs one crash point per case,
// rotated by the case's index, besides its uninterrupted run: the full sweep
// (93 runs) costs ~29s of the VM's 2 vCPUs, which ADR 0005's budget cannot
// carry. The simulator leg and ZFS_CRASH_FULL=1 run every point.
func crashSample(ci, k int, after bool, n int) bool {
	if os.Getenv("ZFS_TEST_POOL") == "" || os.Getenv("ZFS_CRASH_FULL") != "" {
		return true
	}
	i := 2 * k
	if after {
		i++
	}
	return i == ci%(2*n)
}

func TestCrashAtEveryMutationConverges(t *testing.T) {
	for ci, c := range crashCases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel() // each run has its own parent dataset (sim: its own Sim)
			var want string
			var n int
			t.Run("uninterrupted", func(t *testing.T) { want, n = runCrash(t, c, -1, false) })
			if want == "" && n == 0 {
				t.Skip("uninterrupted run did not complete")
			}
			for k := 0; k < n; k++ {
				for _, after := range []bool{false, true} {
					mode := "before"
					if after {
						mode = "after"
					}
					label := fmt.Sprintf("k%d-%s", k, mode)
					if !crashSample(ci, k, after, n) {
						continue
					}
					t.Run(label, func(t *testing.T) {
						t.Parallel()
						got, _ := runCrash(t, c, k, after)
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
							t.Errorf("end state after a crash differs from the uninterrupted run:\n--- uninterrupted\n%s\n--- crash at %d (%s)\n%s", want, k, mode, got)
						}
					})
				}
			}
		})
	}
}
