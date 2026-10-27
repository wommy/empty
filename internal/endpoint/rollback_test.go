package endpoint_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

// ADR 0010: a fresh CreateVolume (the call's first scan, under the lock,
// saw the volume absent, and the call then made something) that fails
// rolls back what it made, still under its locks, unless the failure is
// the caller's own deadline or cancellation. A successful rollback leaves
// the parent as it was before the call (R5) and returns the code the
// failure has before any change; a failed one is joined to the failure,
// whose after-change code it keeps, and the row is left for Resume.
//
// These tests inject faults through their own simulator: they never run
// against a real pool, and never skip on one.

// rbHarness is a harness on a simulator of its own.
func rbHarness(t *testing.T) (*harness, *zfstest.Sim) {
	t.Helper()
	sim := zfstest.New("tank")
	h := &harness{Parent: "tank/k8s/vols", Z: zfs.New(sim), sim: sim}
	if err := h.Z.CreateFilesystem(context.Background(), h.Parent, nil, true); err != nil {
		t.Fatal(err)
	}
	return h, sim
}

// rbEndpoint is an endpoint on h through r, configured as crashEndpoint.
func rbEndpoint(t *testing.T, h *harness, r zfs.Runner, mod func(*endpoint.Config)) *endpoint.Endpoint {
	t.Helper()
	cfg := endpoint.Config{Parent: h.Parent, LockWait: time.Second, FileExport: true, NFSServers: []string{nfsSrv1},
		Etab: h.sim, Root: h.sim}
	if mod != nil {
		mod(&cfg)
	}
	ep, err := endpoint.New(zfs.New(r), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return ep
}

// isSetOf matches a `zfs set` whose arguments contain prop.
func isSetOf(prop string) func([]string) bool {
	return func(a []string) bool { return a[0] == "set" && strings.Contains(strings.Join(a, " "), prop) }
}

var noSpace = &zfs.ExitError{Code: 1, Stderr: "cannot set property: size is greater than available space\n"}

// simMutations is what sim ran after the first `from` calls: the commands that
// change the pool.
func simMutations(sim *zfstest.Sim, from int) [][]string {
	var out [][]string
	for _, c := range sim.Calls[from:] {
		if len(c) > 1 && c[1] != "get" && c[1] != "list" {
			out = append(out, c[1:])
		}
	}
	return out
}

type rbShape struct {
	name  string
	setup func(context.Context, *endpoint.Endpoint, *harness) error
	req   endpoint.CreateVolumeRequest
}

var rbShapes = []rbShape{
	{name: "block-thick", req: endpoint.CreateVolumeRequest{Name: "v", Kind: endpoint.Block, CapacityBytes: 64 * cmib}},
	{name: "block-sparse", req: endpoint.CreateVolumeRequest{Name: "v", Kind: endpoint.Block, CapacityBytes: 64 * cmib, Sparse: true}},
	{name: "fs", req: endpoint.CreateVolumeRequest{Name: "v", CapacityBytes: 64 * cmib}},
	{name: "fs-reserve", req: endpoint.CreateVolumeRequest{Name: "v", CapacityBytes: 64 * cmib, Reserve: true}},
	{name: "fs-props", req: endpoint.CreateVolumeRequest{Name: "v", Properties: map[string]string{"compression": "zstd", "recordsize": "16K", "atime": "off"}}},
	{name: "nfs", req: nfsCreate("v", "", "10.20.0.0/24")},
	{name: "nfs-root", req: nfsRoot("v", u32(1000), u32(2000), u32(0o2775))},
	{name: "restore-zvol-thick",
		setup: then(mk(endpoint.CreateVolumeRequest{Name: "src", Kind: endpoint.Block, CapacityBytes: 16 * cmib}), snapOf("s", "src")),
		req:   endpoint.CreateVolumeRequest{Name: "r", Kind: endpoint.Block, CapacityBytes: 32 * cmib, SourceSnapshotID: "src@s"}},
	{name: "clone-volume-fs-reserve",
		setup: mk(endpoint.CreateVolumeRequest{Name: "src", CapacityBytes: 16 * cmib}),
		req:   endpoint.CreateVolumeRequest{Name: "c", SourceVolumeID: "src", Reserve: true}},
	{name: "clone-volume-zvol-thick",
		setup: mk(endpoint.CreateVolumeRequest{Name: "src", Kind: endpoint.Block, CapacityBytes: 16 * cmib}),
		req:   endpoint.CreateVolumeRequest{Name: "c", Kind: endpoint.Block, CapacityBytes: 16 * cmib, SourceVolumeID: "src"}},
}

// rbFault is a failure after the create's first change. arm injects it
// (fired reports that it was hit); disarm clears it. code is the code
// after a successful rollback: the failure's own, before any change.
type rbFault struct {
	name   string
	shapes []string // nil: every shape
	arm    func(t *testing.T, h *harness, sim *zfstest.Sim, rootErr *error, fired *bool)
	disarm func(t *testing.T, h *harness, sim *zfstest.Sim, rootErr *error)
	code   endpoint.Code
}

// failWhen arms sim to fail every call match matches with err.
func failWhen(sim *zfstest.Sim, match func([]string) bool, err error, fired *bool) {
	sim.Fail = func(a []string) error {
		if match(a) {
			*fired = true
			return err
		}
		return nil
	}
}

func clearFail(_ *testing.T, _ *harness, sim *zfstest.Sim, _ *error) { sim.Fail = nil }

var rbFaults = []rbFault{
	{name: "commit fails", code: endpoint.Internal,
		arm: func(_ *testing.T, _ *harness, sim *zfstest.Sim, _ *error, fired *bool) {
			failWhen(sim, isSetOf(":provision_success=true"), boom, fired)
		}, disarm: clearFail},
	{name: "no space for the reservation", code: endpoint.ResourceExhausted,
		shapes: []string{"restore-zvol-thick", "clone-volume-fs-reserve", "clone-volume-zvol-thick"},
		arm: func(_ *testing.T, _ *harness, sim *zfstest.Sim, _ *error, fired *bool) {
			failWhen(sim, isSetOf("refreservation="), noSpace, fired)
		}, disarm: clearFail},
	{name: "root owner fails", code: endpoint.Internal, shapes: []string{"nfs-root"},
		arm: func(_ *testing.T, _ *harness, _ *zfstest.Sim, rootErr *error, fired *bool) {
			*rootErr, *fired = errors.New("chown: input/output error"), true
		},
		disarm: func(_ *testing.T, _ *harness, _ *zfstest.Sim, rootErr *error) { *rootErr = nil }},
	{name: "export not served", code: endpoint.Unavailable, shapes: []string{"nfs", "nfs-root"},
		arm: func(_ *testing.T, _ *harness, sim *zfstest.Sim, _ *error, fired *bool) {
			sim.SetNFSDown(true)
			*fired = true
		},
		disarm: func(_ *testing.T, _ *harness, sim *zfstest.Sim, _ *error) { sim.SetNFSDown(false) }},
	{name: "served beyond its export", code: endpoint.FailedPrecondition, shapes: []string{"nfs"},
		arm: func(t *testing.T, h *harness, _ *zfstest.Sim, _ *error, fired *bool) {
			if err := h.Z.Set(context.Background(), "tank/k8s", map[string]string{"sharenfs": "on"}); err != nil {
				t.Fatal(err)
			}
			*fired = true
		},
		disarm: func(t *testing.T, h *harness, _ *zfstest.Sim, _ *error) {
			if err := h.Z.Set(context.Background(), "tank/k8s", map[string]string{"sharenfs": "off"}); err != nil {
				t.Fatal(err)
			}
		}},
}

// rbRun sets shape up on a fresh simulator and returns its harness, the
// simulator, an endpoint whose root setter fails while *rootErr is set,
// and rootErr.
func rbRun(t *testing.T, s rbShape) (*harness, *zfstest.Sim, *endpoint.Endpoint, *error) {
	t.Helper()
	h, sim := rbHarness(t)
	rootErr := new(error)
	ep := rbEndpoint(t, h, sim, func(c *endpoint.Config) { c.Root = failRoot{sim, rootErr} })
	if s.setup != nil {
		if err := s.setup(context.Background(), ep, h); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	return h, sim, ep, rootErr
}

// R5: after a successful rollback the parent is what it was before the
// call (no row, no clone-source snapshot pinning the source), the code is
// the failure's own before any change, and a clean retry reaches the end
// state of an uninterrupted run.
func TestCreateRollbackLeavesNothing(t *testing.T) {
	ctx := context.Background()
	for _, s := range rbShapes {
		h, _, ep, _ := rbRun(t, s)
		if _, err := ep.CreateVolume(ctx, s.req); err != nil {
			t.Fatalf("%s uninterrupted: %v", s.name, err)
		}
		clean := fingerprint(t, h)
		for _, f := range rbFaults {
			if f.shapes != nil && !slices.Contains(f.shapes, s.name) {
				continue
			}
			t.Run(s.name+"/"+f.name, func(t *testing.T) {
				h, sim, ep, rootErr := rbRun(t, s)
				fired := false
				f.arm(t, h, sim, rootErr, &fired)
				before := fingerprint(t, h)
				_, err := ep.CreateVolume(ctx, s.req)
				if !fired {
					t.Fatalf("the fault never fired (%v)", err)
				}
				wantCode(t, err, f.code)
				if !strings.Contains(err.Error(), "rolled back") {
					t.Errorf("the error does not say it was rolled back: %v", err)
				}
				f.disarm(t, h, sim, rootErr)
				if got := fingerprint(t, h); got != before {
					t.Fatalf("the parent after the rollback differs from before the call:\n--- before\n%s\n--- after\n%s", before, got)
				}
				if _, err := ep.CreateVolume(ctx, s.req); err != nil {
					t.Fatalf("clean retry: %v", err)
				}
				if got := fingerprint(t, h); got != clean {
					t.Fatalf("the retry differs from an uninterrupted run:\n--- uninterrupted\n%s\n--- retry\n%s", clean, got)
				}
				if s.req.SourceVolumeID != "" || s.req.SourceSnapshotID != "" {
					// nothing of the failed attempt pins the source
					if err := ep.DeleteVolume(ctx, s.req.Name); err != nil {
						t.Fatal(err)
					}
					if err := ep.DeleteSnapshot(ctx, s.req.SourceSnapshotID); s.req.SourceSnapshotID != "" && err != nil {
						t.Fatal(err)
					}
					if err := ep.DeleteVolume(ctx, "src"); err != nil {
						t.Fatalf("the source after its clone: %v", err)
					}
				}
			})
		}
	}
}

// A clone of a volume whose clone fails after the snapshot was cut: the
// row was never made, and the rollback releases the stamped
// <src>@volume-source-for-volume-<id>, which would pin the source.
func TestCreateRollbackReleasesCutSource(t *testing.T) {
	ctx := context.Background()
	h, sim, ep, _ := rbRun(t, rbShape{setup: mk(endpoint.CreateVolumeRequest{Name: "src", CapacityBytes: 16 * cmib})})
	before := fingerprint(t, h)
	fired := false
	failWhen(sim, func(a []string) bool { return a[0] == "clone" }, boom, &fired)
	_, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "c", SourceVolumeID: "src"})
	if !fired {
		t.Fatalf("the clone never ran (%v)", err)
	}
	wantCode(t, err, endpoint.Internal)
	sim.Fail = nil
	if sim.Has(h.Parent + "/src@volume-source-for-volume-c") {
		t.Fatal("the clone-source snapshot still pins the source")
	}
	if got := fingerprint(t, h); got != before {
		t.Fatalf("after the rollback:\n%s\nwant\n%s", got, before)
	}
	if err := ep.DeleteVolume(ctx, "src"); err != nil {
		t.Fatalf("the source: %v", err)
	}
}

// loseCreate runs the `zfs create` of target, then loses its answer (the
// k0-after shape): the first op of a fresh create may have run.
type loseCreate struct {
	zfs.Runner
	target string
	lost   bool
}

func (r *loseCreate) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := r.Runner.Run(ctx, name, args...)
	if !r.lost && err == nil && args[0] == "create" && args[len(args)-1] == r.target {
		r.lost = true
		return nil, &zfs.ExitError{Args: args, Code: -1, Underlay: errors.New("signal: killed")}
	}
	return out, err
}

// An ambiguous first op: the create ran, its answer was lost. Before, the
// call returned Internal (final) with the dataset there, a silent leak.
// The rollback's scan decides: it finds the row, and destroys it.
func TestCreateRollbackAmbiguousFirstOp(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []endpoint.VolumeKind{endpoint.Filesystem, endpoint.Block} {
		h, sim := rbHarness(t)
		r := &loseCreate{Runner: sim, target: h.Parent + "/v"}
		ep := rbEndpoint(t, h, r, nil)
		_, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "v", Kind: kind, CapacityBytes: 16 * cmib})
		if !r.lost {
			t.Fatalf("%v: no create (%v)", kind, err)
		}
		wantCode(t, err, endpoint.Internal)
		if sim.Has(h.Parent + "/v") {
			t.Fatalf("%v: the dataset the lost create made is still there", kind)
		}
	}
}

// slowClone runs the clone, then holds its answer until the caller's
// context ends: on a slow pool, the sidecar's timeout lands mid-provision.
type slowClone struct{ zfs.Runner }

func (r slowClone) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := r.Runner.Run(ctx, name, args...)
	if err == nil && args[0] == "clone" {
		<-ctx.Done()
		return nil, &zfs.ExitError{Args: args, Code: -1, Underlay: ctx.Err()}
	}
	return out, err
}

// R3: the caller's own deadline or cancellation never rolls back. A pool
// where a clone outlasts the sidecar's timeout would otherwise redo and
// undo it on every attempt; the row stays, and the next call resumes it
// (no create, no clone).
func TestCreateNoRollbackOnCallerDeadline(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc)
		code endpoint.Code
	}{
		{"deadline", func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 50*time.Millisecond)
		}, endpoint.DeadlineExceeded},
		{"canceled", func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			time.AfterFunc(50*time.Millisecond, cancel)
			return ctx, cancel
		}, endpoint.Aborted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, sim, _, _ := rbRun(t, rbShape{setup: mk(endpoint.CreateVolumeRequest{Name: "src", CapacityBytes: 16 * cmib})})
			ep := rbEndpoint(t, h, slowClone{sim}, nil)
			req := endpoint.CreateVolumeRequest{Name: "c", SourceVolumeID: "src", Reserve: true}
			ctx, cancel := tc.ctx()
			_, err := ep.CreateVolume(ctx, req)
			cancel()
			wantCode(t, err, tc.code)
			if !sim.Has(h.Parent+"/c") || sim.Prop(h.Parent+"/c", endpoint.DefaultPropertyPrefix+":provision_success") == "true" {
				t.Fatal("want the clone left uncommitted for the retry")
			}
			from := len(sim.Calls)
			if _, err := ep.CreateVolume(context.Background(), req); err != nil {
				t.Fatalf("retry: %v", err)
			}
			for _, m := range simMutations(sim, from) {
				if m[0] == "clone" || m[0] == "create" || m[0] == "snapshot" || m[0] == "destroy" {
					t.Fatalf("the retry made it again (%v) instead of resuming it", m)
				}
			}
		})
	}
}

// R3: a resume never rolls back. The row existed at the call's first
// scan (an earlier attempt's, here made by hand), so it is not this
// call's to undo, even when the resume fails.
func TestCreateNoRollbackOnResume(t *testing.T) {
	ctx := context.Background()
	h, sim, ep, _ := rbRun(t, rbShape{})
	own := map[string]string{"zfs-endpoint:managed_resource": "true", "zfs-endpoint:csi_volume_name": "half"}
	if err := h.Z.CreateFilesystem(ctx, h.Parent+"/half", own, false); err != nil {
		t.Fatal(err)
	}
	fired := false
	failWhen(sim, isSetOf(":provision_success=true"), boom, &fired)
	_, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "half"})
	if !fired {
		t.Fatalf("the commit never ran (%v)", err)
	}
	wantCode(t, err, endpoint.Aborted)
	sim.Fail = nil
	if !sim.Has(h.Parent + "/half") {
		t.Fatal("a resume rolled back a row it did not make")
	}
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "half"}); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

// R4: a rollback that fails is reported with the failure, joined; the code
// is the failure's after-change one (retryable), and the row stays,
// uncommitted, for the retry to resume.
func TestCreateRollbackFailureIsJoined(t *testing.T) {
	ctx := context.Background()
	_, sim, ep, _ := rbRun(t, rbShape{})
	const ds = "tank/k8s/vols/v"
	fired, busy := false, false
	sim.Fail = func(a []string) error {
		switch {
		case isSetOf(":provision_success=true")(a):
			fired = true
			return boom
		case a[0] == "destroy":
			busy = true
			return &zfs.ExitError{Code: 1, Stderr: "cannot destroy '" + ds + "': dataset is busy\n"}
		}
		return nil
	}
	req := endpoint.CreateVolumeRequest{Name: "v", Kind: endpoint.Block, CapacityBytes: 16 * cmib}
	_, err := ep.CreateVolume(ctx, req)
	if !fired || !busy {
		t.Fatalf("commit failed %v, rollback destroy failed %v (%v)", fired, busy, err)
	}
	wantCode(t, err, endpoint.Aborted)
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "rolling the create back") || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("want the failure and the rollback's, joined: %v", err)
	}
	sim.Fail = nil
	if !sim.Has(ds) || sim.Prop(ds, endpoint.DefaultPropertyPrefix+":provision_success") == "true" {
		t.Fatal("want the row left uncommitted")
	}
	from := len(sim.Calls)
	if _, err := ep.CreateVolume(ctx, req); err != nil {
		t.Fatalf("retry: %v", err)
	}
	for _, m := range simMutations(sim, from) {
		if m[0] == "create" || m[0] == "destroy" {
			t.Fatalf("the retry %v instead of resuming", m)
		}
	}
}

// Before, a failed fresh create left a row that made the same name with
// another kind or source AlreadyExists, final, for good. After its
// rollback the name is free.
func TestCreateRollbackFreesTheName(t *testing.T) {
	ctx := context.Background()
	_, sim, ep, _ := rbRun(t, rbShape{})
	fired := false
	failWhen(sim, isSetOf(":provision_success=true"), boom, &fired)
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "v", CapacityBytes: 16 * cmib}); err == nil || !fired {
		t.Fatalf("want the injected failure (%v)", err)
	}
	sim.Fail = nil
	v, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "v", Kind: endpoint.Block, CapacityBytes: 16 * cmib})
	if err != nil || v.Kind != endpoint.Block {
		t.Fatalf("the same name as a zvol: %+v %v", v, err)
	}
}

// parkFailCommit fails the first commit, after parking it until released:
// the failing call holds the volume's lock meanwhile.
type parkFailCommit struct {
	zfs.Runner
	once             sync.Once
	entered, release chan struct{}
}

func (g *parkFailCommit) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if isSetOf(":provision_success=true")(args) {
		first := false
		g.once.Do(func() { first = true })
		if first {
			close(g.entered)
			<-g.release
			return nil, boom
		}
	}
	return g.Runner.Run(ctx, name, args...)
}

// Two requests for one name: the first fails at its commit and rolls back
// under the volume's lock; the second waits for the lock, finds nothing,
// and creates the volume itself. It never sees the half-made row: no
// AlreadyExists (it asks for another kind), no resume.
func TestCreateRollbackConcurrentSameName(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		h, sim := rbHarness(t)
		g := &parkFailCommit{Runner: sim, entered: make(chan struct{}), release: make(chan struct{})}
		ep := rbEndpoint(t, h, g, nil)
		var wg sync.WaitGroup
		var err1, err2 error
		var v2 endpoint.Volume
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err1 = ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "v", CapacityBytes: 16 * cmib})
		}()
		<-g.entered
		from := len(sim.Calls)
		wg.Add(1)
		go func() {
			defer wg.Done()
			v2, err2 = ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "v", Kind: endpoint.Block, CapacityBytes: 16 * cmib})
		}()
		synctest.Wait() // the second request is blocked on the lock
		if n := len(sim.Calls); n != from {
			t.Fatalf("the second request ran %v while the first held the lock", sim.Calls[from:])
		}
		close(g.release)
		wg.Wait()
		wantCode(t, err1, endpoint.Internal)
		if err2 != nil || v2.Kind != endpoint.Block {
			t.Fatalf("second request: %+v %v", v2, err2)
		}
		creates := 0
		for _, m := range simMutations(sim, 0) {
			if m[0] == "create" && m[len(m)-1] == h.Parent+"/v" {
				creates++
			}
		}
		if creates != 2 {
			t.Fatalf("%d creates of v, want 2 (the rolled-back one and the second request's)", creates)
		}
	})
}

// The create's answer was lost (it ran) and the rollback then fails (a
// busy destroy): the row is left, so the code must be one the provisioner
// retries, never a final one (ADR 0010 R4).
func TestCreateRollbackFailsAfterAmbiguousFirstOpIsRetryable(t *testing.T) {
	ctx := context.Background()
	h, sim := rbHarness(t)
	r := &loseCreate{Runner: sim, target: h.Parent + "/v"}
	ep := rbEndpoint(t, h, r, nil)
	sim.Fail = func(a []string) error {
		if a[0] == "destroy" {
			return &zfs.ExitError{Code: 1, Stderr: "cannot destroy: dataset is busy\n"}
		}
		return nil
	}
	_, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "v", Kind: endpoint.Block, CapacityBytes: 16 * cmib})
	sim.Fail = nil
	if !sim.Has(h.Parent + "/v") {
		t.Fatal("the busy destroy did not leave the row; the test no longer reaches a failed rollback")
	}
	if c := endpoint.CodeOf(err); c != endpoint.Aborted {
		t.Fatalf("row left uncommitted with code %v (%v): want Aborted, a final code strands it", c, err)
	}
}

// R4 for every fault, deliberate refusals included: when the rollback
// fails (every destroy busy) and leaves something, the code is one the
// provisioner retries, never the fault's own final code.
func TestCreateRollbackFailureIsAlwaysRetryable(t *testing.T) {
	ctx := context.Background()
	busy := &zfs.ExitError{Code: 1, Stderr: "cannot destroy: dataset is busy\n"}
	for _, s := range rbShapes {
		for _, f := range rbFaults {
			if f.shapes != nil && !slices.Contains(f.shapes, s.name) {
				continue
			}
			t.Run(s.name+"/"+f.name, func(t *testing.T) {
				h, sim, ep, rootErr := rbRun(t, s)
				fired := false
				f.arm(t, h, sim, rootErr, &fired)
				armed := sim.Fail
				sim.Fail = func(a []string) error {
					if a[0] == "destroy" {
						return busy
					}
					if armed != nil {
						return armed(a)
					}
					return nil
				}
				before := fingerprint(t, h)
				_, err := ep.CreateVolume(ctx, s.req)
				f.disarm(t, h, sim, rootErr)
				sim.Fail = nil
				if !fired {
					t.Fatalf("the fault never fired (%v)", err)
				}
				// Aborted and Unavailable are the codes external-provisioner
				// retries; any other code with something left strands it
				if c := endpoint.CodeOf(err); fingerprint(t, h) != before && c != endpoint.Aborted && c != endpoint.Unavailable {
					t.Fatalf("left something with final code %v: %v", c, err)
				}
			})
		}
	}
}
