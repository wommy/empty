package endpoint_test

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

// harness runs the contract tests against the simulator, or against a real
// pool when ZFS_TEST_POOL is set (CI: a file-backed pool; see
// .github/workflows/ci.yml). Each test gets its own parent dataset.
type harness struct {
	Parent string
	Z      *zfs.Client
	sim    *zfstest.Sim
}

func (h *harness) Has(name string) bool {
	ok, err := h.Z.Exists(context.Background(), name)
	if err != nil {
		panic(err)
	}
	return ok
}

func (h *harness) Prop(name, prop string) string {
	d, err := h.Z.Get(context.Background(), name, prop)
	if err != nil {
		return ""
	}
	return d.Props[prop].Value
}

// Sim returns the simulator, skipping tests that need fault injection or
// call accounting when running against real ZFS.
func (h *harness) Sim(t *testing.T) *zfstest.Sim {
	t.Helper()
	if h.sim == nil {
		t.Skip("needs the simulator (fault injection / call accounting)")
	}
	return h.sim
}

// requireSim skips a simulator-only test before it creates anything: on a
// real pool the skip then costs no dataset, and it happens outside any
// synctest bubble (a t.Skip inside synctest.Test reports PASS, not SKIP,
// which would both hide the test and break ci/check-skips.sh).
func requireSim(t *testing.T) {
	t.Helper()
	if os.Getenv("ZFS_TEST_POOL") != "" {
		t.Skip("needs the simulator (fault injection / call accounting)")
	}
}

type slowRunner struct {
	zfs.Runner
	get, mut time.Duration
}

func (r slowRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if len(args) > 0 && (args[0] == "get" || args[0] == "list") {
		time.Sleep(r.get)
	} else {
		time.Sleep(r.mut)
	}
	return r.Runner.Run(ctx, name, args...)
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

func setup(t *testing.T) (*endpoint.Endpoint, *harness) {
	t.Helper()
	return setupPrefix(t, "")
}

// setupPrefix is setup with a property prefix; "democratic-csi" is the
// adoption mode.
func setupPrefix(t *testing.T, prefix string) (*endpoint.Endpoint, *harness) {
	t.Helper()
	return setupConfig(t, prefix, nil)
}

// setupNFS is setup for an endpoint advertising NFS servers.
func setupNFS(t *testing.T, servers ...string) (*endpoint.Endpoint, *harness) {
	t.Helper()
	return setupConfig(t, "", func(c *endpoint.Config) { c.NFSServers = servers })
}

// setupConfig is setupPrefix with mod applied to the endpoint's Config.
func setupConfig(t *testing.T, prefix string, mod func(*endpoint.Config)) (*endpoint.Endpoint, *harness) {
	t.Helper()
	ctx := context.Background()
	h := &harness{}
	if pool := os.Getenv("ZFS_TEST_POOL"); pool != "" {
		h.Z = zfs.New(zfs.LocalRunner{})
		// the name part is capped so the parent stays within
		// endpoint.MaxParentLen whatever a subtest is called
		name := unsafeChars.ReplaceAllString(t.Name(), "_")
		name = name[:min(len(name), 64)]
		h.Parent = fmt.Sprintf("%s/ep-%s-%d", pool, name, time.Now().UnixNano())
		t.Cleanup(func() {
			if err := h.Z.Destroy(ctx, h.Parent, zfs.DestroyOptions{Recursive: true}); err != nil {
				t.Errorf("cleanup %s: %v", h.Parent, err)
			}
		})
	} else if os.Getenv("ZFS_REQUIRE_REAL") != "" {
		t.Fatal("ZFS_REQUIRE_REAL is set but ZFS_TEST_POOL is not: refusing the simulator fallback")
	} else {
		h.sim = zfstest.New("tank")
		h.Z = zfs.New(h.sim)
		h.Parent = "tank/k8s/vols"
		if lat := os.Getenv("ZFS_TEST_LATENCY"); lat != "" {
			// a latency model of a real pool (wave7 suite.md): get and
			// mutating calls each cost a fixed time, so parallelism and
			// call counts can be measured without one
			var get, mut time.Duration
			fmt.Sscanf(lat, "%d,%d", &get, &mut)
			h.Z = zfs.New(slowRunner{h.sim, get * time.Microsecond, mut * time.Microsecond})
		}
		if os.Getenv("ZFS_TEST_CALLS") != "" {
			sim := h.sim
			t.Cleanup(func() {
				g, m := 0, 0
				for _, c := range sim.Calls {
					if len(c) > 1 && (c[1] == "get" || c[1] == "list") {
						g++
					} else {
						m++
					}
				}
				t.Logf("CALLS get=%d mut=%d", g, m)
			})
		}
	}
	if err := h.Z.CreateFilesystem(ctx, h.Parent, nil, true); err != nil {
		t.Fatal(err)
	}
	return h.endpoint(t, prefix, mod), h
}

// endpoint starts another endpoint on h's parent, as a restart does.
func (h *harness) endpoint(t *testing.T, prefix string, mod func(*endpoint.Config)) *endpoint.Endpoint {
	t.Helper()
	cfg := endpoint.Config{Parent: h.Parent, LockWait: time.Second, FileExport: true, PropertyPrefix: prefix}
	if h.sim != nil {
		cfg.Etab = h.sim
	} else {
		// as cmd/zfs-endpoint: a zvol udev is still probing is not in use
		cfg.Settle = endpoint.UdevSettle
	}
	if mod != nil {
		mod(&cfg)
	}
	ep, err := endpoint.New(h.Z, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return ep
}

func wantCode(t *testing.T, err error, code endpoint.Code) {
	t.Helper()
	if got := endpoint.CodeOf(err); got != code {
		t.Fatalf("code = %d (%v), want %d", got, err, code)
	}
}

func TestCreateVolumeIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ep, h := setup(t)
	req := endpoint.CreateVolumeRequest{Name: "pvc-1", Kind: endpoint.Block, CapacityBytes: 1<<30 + 1, Sparse: true}

	v1, err := ep.CreateVolume(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	// rounded up to volblocksize, never below the request
	if v1.CapacityBytes != 1<<30+16384 {
		t.Fatalf("capacity %d", v1.CapacityBytes)
	}
	v2, err := ep.CreateVolume(ctx, req)
	if err != nil || v2 != v1 {
		t.Fatalf("retry: %+v %v", v2, err)
	}
	if h.Prop(h.Parent+"/pvc-1", "zfs-endpoint:provision_success") != "true" {
		t.Fatal("commit marker missing")
	}

	// incompatible re-creates
	bigger := req
	bigger.CapacityBytes = 2 << 30
	_, err = ep.CreateVolume(ctx, bigger)
	wantCode(t, err, endpoint.AlreadyExists)
	fs := req
	fs.Kind, fs.Sparse = endpoint.Filesystem, false
	_, err = ep.CreateVolume(ctx, fs)
	wantCode(t, err, endpoint.AlreadyExists)
	limited := req
	limited.CapacityBytes, limited.LimitBytes = 1<<20, 1<<20
	_, err = ep.CreateVolume(ctx, limited)
	wantCode(t, err, endpoint.AlreadyExists)
}

func TestCreateVolumeResumesInterruptedProvision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ep, h := setup(t)
	// a crash after create, before the marker: owned but not committed
	z := h.Z
	if err := z.CreateFilesystem(ctx, h.Parent+"/pvc-2", map[string]string{
		"zfs-endpoint:managed_resource": "true",
		"zfs-endpoint:csi_volume_name":  "pvc-2",
		"refquota":                      "1073741824",
	}, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ep.ListVolumes(ctx, 0, ""); err != nil {
		t.Fatal(err)
	}
	vols, _, _ := ep.ListVolumes(ctx, 0, "")
	if len(vols) != 0 {
		t.Fatalf("uncommitted volume listed: %+v", vols)
	}
	v, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "pvc-2", CapacityBytes: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}
	if v.CapacityBytes != 1<<30 || h.Prop(h.Parent+"/pvc-2", "zfs-endpoint:provision_success") != "true" {
		t.Fatalf("not resumed: %+v", v)
	}
}

func TestNeverAdoptsOrDeletesUnmanaged(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ep, h := setup(t)
	if err := h.Z.CreateFilesystem(ctx, h.Parent+"/precious", nil, false); err != nil {
		t.Fatal(err)
	}
	_, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "precious"})
	wantCode(t, err, endpoint.AlreadyExists)
	wantCode(t, ep.DeleteVolume(ctx, "precious"), endpoint.FailedPrecondition)
	if !h.Has(h.Parent + "/precious") {
		t.Fatal("unmanaged dataset destroyed")
	}
}

func TestDeleteVolume(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ep, h := setup(t)
	if err := ep.DeleteVolume(ctx, "never-existed"); err != nil {
		t.Fatalf("delete missing: %v", err)
	}
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "pvc-3", CapacityBytes: 1 << 30}); err != nil {
		t.Fatal(err)
	}
	if _, err := ep.CreateSnapshot(ctx, "snap-a", "pvc-3"); err != nil {
		t.Fatal(err)
	}
	// CSI snapshots must survive: refuse, leave everything in place
	wantCode(t, ep.DeleteVolume(ctx, "pvc-3"), endpoint.FailedPrecondition)
	if !h.Has(h.Parent + "/pvc-3@snap-a") {
		t.Fatal("snapshot destroyed")
	}
	if err := ep.DeleteSnapshot(ctx, "pvc-3@snap-a"); err != nil {
		t.Fatal(err)
	}
	if err := ep.DeleteVolume(ctx, "pvc-3"); err != nil {
		t.Fatal(err)
	}
	if err := ep.DeleteVolume(ctx, "pvc-3"); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}

// mib64 sizes thick zvols: small enough that the real leg's parallel tests
// fit the 10G pool (thick zvols reserve their full size), large enough to
// be many volblocks.
const mib64 = 64 << 20

func TestSnapshotLifecycleWithClones(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ep, h := setup(t)
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "src", Kind: endpoint.Block, CapacityBytes: mib64}); err != nil {
		t.Fatal(err)
	}
	s, err := ep.CreateSnapshot(ctx, "snap-1", "src")
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "src@snap-1" || s.SizeBytes != mib64 || !s.ReadyToUse {
		t.Fatalf("snapshot %+v", s)
	}
	if again, err := ep.CreateSnapshot(ctx, "snap-1", "src"); err != nil || again != s {
		t.Fatalf("retry: %+v %v", again, err)
	}

	// names are global
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "other", Kind: endpoint.Block, CapacityBytes: mib64}); err != nil {
		t.Fatal(err)
	}
	_, err = ep.CreateSnapshot(ctx, "snap-1", "other")
	wantCode(t, err, endpoint.AlreadyExists)

	// restore with growth
	c, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "restored", Kind: endpoint.Block, CapacityBytes: 2 * mib64, SourceSnapshotID: "src@snap-1"})
	if err != nil {
		t.Fatal(err)
	}
	if c.CapacityBytes != 2*mib64 || c.SourceType != "snapshot" || c.SourceID != "src@snap-1" {
		t.Fatalf("clone %+v", c)
	}

	// deleting a cloned snapshot succeeds (deferred), hides it, keeps the data
	if err := ep.DeleteSnapshot(ctx, "src@snap-1"); err != nil {
		t.Fatal(err)
	}
	if !h.Has(h.Parent + "/src@snap-1") {
		t.Fatal("cloned snapshot destroyed early")
	}
	snaps, _, err := ep.ListSnapshots(ctx, "", "", 0, "")
	if err != nil || len(snaps) != 0 {
		t.Fatalf("deferred snapshot listed: %+v %v", snaps, err)
	}
	if err := ep.DeleteVolume(ctx, "restored"); err != nil {
		t.Fatal(err)
	}
	if h.Has(h.Parent + "/src@snap-1") {
		t.Fatal("deferred snapshot leaked after last clone")
	}

	// unknown source
	_, err = ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "x", Kind: endpoint.Block, CapacityBytes: 1, SourceSnapshotID: "src@nope"})
	wantCode(t, err, endpoint.NotFound)
	// ids we could never have issued are already deleted
	if err := ep.DeleteSnapshot(ctx, "garbage"); err != nil {
		t.Fatal(err)
	}
}

func TestCloneFromVolumeReleasesInternalSnapshot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ep, h := setup(t)
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "a", CapacityBytes: 1 << 30}); err != nil {
		t.Fatal(err)
	}
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "b", CapacityBytes: 1 << 30, SourceVolumeID: "a"}); err != nil {
		t.Fatal(err)
	}
	internal := h.Parent + "/a@volume-source-for-volume-b"
	if !h.Has(internal) {
		t.Fatal("internal snapshot missing")
	}
	// source cannot go while a clone depends on it
	wantCode(t, ep.DeleteVolume(ctx, "a"), endpoint.FailedPrecondition)
	if err := ep.DeleteVolume(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if h.Has(internal) {
		t.Fatal("internal snapshot leaked")
	}
	if err := ep.DeleteVolume(ctx, "a"); err != nil {
		t.Fatal(err)
	}
}

func TestExpand(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ep, _ := setup(t)
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "e", Kind: endpoint.Block, CapacityBytes: mib64}); err != nil {
		t.Fatal(err)
	}
	v, err := ep.ExpandVolume(ctx, "e", 3*mib64+1, 0)
	if err != nil || v.CapacityBytes != 3*mib64+16384 {
		t.Fatalf("expand %+v %v", v, err)
	}
	v, err = ep.ExpandVolume(ctx, "e", mib64, 0)
	if err != nil || v.CapacityBytes != 3*mib64+16384 {
		t.Fatalf("shrink must be a no-op: %+v %v", v, err)
	}
	_, err = ep.ExpandVolume(ctx, "missing", 1, 0)
	wantCode(t, err, endpoint.NotFound)
}

// The 16 creates wait for one lock with LockWait 1s. On the real clock a
// loaded host (or -race) made a waiter time out (Aborted), a flake; in a
// synctest bubble time advances only when every goroutine is blocked, so
// LockWait can expire only on a real deadlock. requireSim stays outside the
// bubble: a skip inside one reports PASS.
func TestConcurrentCreatesSerialize(t *testing.T) {
	t.Parallel()
	requireSim(t)
	synctest.Test(t, concurrentCreatesSerialize)
}

func concurrentCreatesSerialize(t *testing.T) {
	ctx := context.Background()
	_, h := setup(t)
	sim := h.Sim(t)
	// the first `zfs create` parks until every other create is blocked:
	// with the volume lock they wait for it and then find the volume;
	// without it they reach `zfs create` too, and the count says so
	g := &parkFirstCreate{Runner: sim, entered: make(chan struct{}), release: make(chan struct{})}
	ep, err := endpoint.New(zfs.New(g), endpoint.Config{Parent: h.Parent, LockWait: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		<-g.entered
		synctest.Wait()
		close(g.release)
	}()
	var wg sync.WaitGroup
	errs := make([]error, 16)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "race", CapacityBytes: 1 << 30})
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("concurrent create: %v", err)
		}
	}
	creates := 0
	for _, c := range sim.Calls {
		if len(c) > 1 && c[1] == "create" && c[len(c)-1] == h.Parent+"/race" {
			creates++
		}
	}
	if creates != 1 {
		t.Fatalf("zfs create ran %d times", creates)
	}
}

// parkFirstCreate holds the first `zfs create` until release is closed;
// every other call goes straight through.
type parkFirstCreate struct {
	zfs.Runner
	once             sync.Once
	entered, release chan struct{}
}

func (g *parkFirstCreate) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "create" {
		first := false
		g.once.Do(func() { first = true })
		if first {
			close(g.entered)
			<-g.release
		}
	}
	return g.Runner.Run(ctx, name, args...)
}

func TestPagination(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ep, _ := setup(t)
	for _, n := range []string{"v1", "v2", "v3"} {
		if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: n}); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	tok := ""
	for {
		page, next, err := ep.ListVolumes(ctx, 2, tok)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range page {
			got = append(got, v.ID)
		}
		if next == "" {
			break
		}
		tok = next
	}
	if len(got) != 3 || got[0] != "v1" || got[2] != "v3" {
		t.Fatalf("pages %v", got)
	}
	_, _, err := ep.ListVolumes(ctx, 2, "nope")
	wantCode(t, err, endpoint.Aborted)
	// S4: the token's volume deleted between pages; the walk goes on
	first, tok, err := ep.ListVolumes(ctx, 2, "")
	if err != nil || len(first) != 2 {
		t.Fatal(first, err)
	}
	if err := ep.DeleteVolume(ctx, first[1].ID); err != nil {
		t.Fatal(err)
	}
	rest, next, err := ep.ListVolumes(ctx, 2, tok)
	if err != nil || len(rest) != 1 || rest[0].ID != "v3" || next != "" {
		t.Fatalf("after deleting %s: %+v %q %v", first[1].ID, rest, next, err)
	}
}

// review H2: user properties inherit, so a snapshot an administrator took
// (or an internal one) must not look owned.
func TestInheritedOwnershipIsNotOwnership(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ep, h := setup(t)
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "v", CapacityBytes: 1 << 30}); err != nil {
		t.Fatal(err)
	}
	if err := h.Z.Snapshot(ctx, h.Parent+"/v@admin-backup", nil); err != nil {
		t.Fatal(err)
	}
	snaps, _, err := ep.ListSnapshots(ctx, "", "", 0, "")
	if err != nil || len(snaps) != 0 {
		t.Fatalf("admin snapshot listed: %+v %v", snaps, err)
	}
	wantCode(t, ep.DeleteSnapshot(ctx, "v@admin-backup"), endpoint.FailedPrecondition)
	if !h.Has(h.Parent + "/v@admin-backup") {
		t.Fatal("admin snapshot destroyed")
	}
}

// a failure between clone and resize converges on retry
func TestCloneResizeFailureConverges(t *testing.T) {
	t.Parallel()
	requireSim(t)
	ctx := context.Background()
	ep, h := setup(t)
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "s", Kind: endpoint.Block, CapacityBytes: 1 << 30, Sparse: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := ep.CreateSnapshot(ctx, "snap", "s"); err != nil {
		t.Fatal(err)
	}
	fail := true
	h.Sim(t).Fail = func(args []string) error {
		if fail && args[0] == "set" && strings.HasPrefix(args[1], "volsize=") {
			fail = false
			return &zfs.ExitError{Code: 1, Stderr: "internal error: Input/output error\n"}
		}
		return nil
	}
	req := endpoint.CreateVolumeRequest{Name: "r", Kind: endpoint.Block, CapacityBytes: 2 << 30, Sparse: true, SourceSnapshotID: "s@snap"}
	if _, err := ep.CreateVolume(ctx, req); err == nil {
		t.Fatal("injected failure not surfaced")
	}
	v, err := ep.CreateVolume(ctx, req)
	if err != nil || v.CapacityBytes != 2<<30 {
		t.Fatalf("retry: %+v %v", v, err)
	}
}

// review 2 #1: zfs reports "dataset does not exist" for an exported pool;
// that must not read as "already deleted"
func TestMissingParentIsUnavailableNotAbsent(t *testing.T) {
	t.Parallel()
	requireSim(t)
	ctx := context.Background()
	ep, h := setup(t)
	h.Sim(t)
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "v", CapacityBytes: 1 << 30}); err != nil {
		t.Fatal(err)
	}
	if _, err := ep.CreateSnapshot(ctx, "s", "v"); err != nil {
		t.Fatal(err)
	}
	if err := h.Z.Destroy(ctx, h.Parent, zfs.DestroyOptions{Recursive: true}); err != nil {
		t.Fatal(err)
	}
	wantCode(t, ep.DeleteSnapshot(ctx, "v@s"), endpoint.Unavailable)
	_, err := ep.GetVolume(ctx, "v")
	wantCode(t, err, endpoint.Unavailable)
}

// StorageClass properties end to end: applied at create, read back from the
// one scan for idempotency, converged on an interrupted provision, and
// refused when they would change a committed volume or a clone's block size.
func TestCreateVolumeProperties(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ep, h := setup(t)

	fs := endpoint.CreateVolumeRequest{Name: "fs", CapacityBytes: 1 << 30,
		Properties: map[string]string{"compression": "lz4", "recordsize": "16K", "atime": "off"}}
	v1, err := ep.CreateVolume(ctx, fs)
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"compression": "lz4", "recordsize": "16384", "atime": "off"} {
		if got := h.Prop(h.Parent+"/fs", k); got != want {
			t.Fatalf("%s = %q, want %q", k, got, want)
		}
	}
	calls := 0
	if h.sim != nil {
		calls = mutations(h.sim)
	}
	if v2, err := ep.CreateVolume(ctx, fs); err != nil || v2 != v1 {
		t.Fatalf("retry: %+v %v", v2, err)
	}
	if h.sim != nil && mutations(h.sim) != calls {
		t.Fatal("retry with properties mutated state")
	}
	fs.Properties = map[string]string{"compression": "lz4", "recordsize": "128K"}
	_, err = ep.CreateVolume(ctx, fs)
	wantCode(t, err, endpoint.AlreadyExists)
	_, err = ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "evil", Properties: map[string]string{"mountpoint": "/etc"}})
	wantCode(t, err, endpoint.InvalidArgument)
	if h.Has(h.Parent + "/evil") {
		t.Fatal("denied request created a dataset")
	}

	// zvol: volblocksize at create rounds volsize; restores must match it
	z := endpoint.CreateVolumeRequest{Name: "z", Kind: endpoint.Block, CapacityBytes: 1<<20 + 1,
		Properties: map[string]string{"volblocksize": "8K", "sync": "always"}}
	zv, err := ep.CreateVolume(ctx, z)
	if err != nil || zv.CapacityBytes != 1<<20+8192 || h.Prop(h.Parent+"/z", "volblocksize") != "8192" {
		t.Fatalf("zvol: %+v %v", zv, err)
	}
	if _, err := ep.CreateVolume(ctx, z); err != nil {
		t.Fatalf("zvol retry: %v", err)
	}
	if _, err := ep.CreateSnapshot(ctx, "zs", "z"); err != nil {
		t.Fatal(err)
	}
	restore := endpoint.CreateVolumeRequest{Name: "zr", Kind: endpoint.Block, SourceSnapshotID: "z@zs",
		Properties: map[string]string{"volblocksize": "16K"}}
	_, err = ep.CreateVolume(ctx, restore)
	wantCode(t, err, endpoint.InvalidArgument)
	restore.Properties = map[string]string{"volblocksize": "8K", "compression": "zle"}
	for i := 0; i < 2; i++ {
		if _, err := ep.CreateVolume(ctx, restore); err != nil {
			t.Fatalf("restore attempt %d: %v", i, err)
		}
	}
	if h.Prop(h.Parent+"/zr", "compression") != "zle" || h.Prop(h.Parent+"/zr", "volblocksize") != "8192" {
		t.Fatal("restore properties not applied")
	}

	// an interrupted provision (owned, not committed) converges with set
	if err := h.Z.CreateFilesystem(ctx, h.Parent+"/half", map[string]string{
		"zfs-endpoint:managed_resource": "true",
		"zfs-endpoint:csi_volume_name":  "half",
	}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "half",
		Properties: map[string]string{"compression": "zle", "snapdir": "visible"}}); err != nil {
		t.Fatal(err)
	}
	if h.Prop(h.Parent+"/half", "compression") != "zle" || h.Prop(h.Parent+"/half", "snapdir") != "visible" ||
		h.Prop(h.Parent+"/half", "zfs-endpoint:provision_success") != "true" {
		t.Fatal("uncommitted volume did not converge")
	}
}

// An interrupted provision of the other thickness converges to the request
// (refreservation=none), so the commit and the identical retry agree with
// compatible(): before, the first call committed the thick row as-is and
// the retry was AlreadyExists.
func TestResumeConvergesThickToThin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ep, h := setup(t)
	own := func(name string) map[string]string {
		return map[string]string{"zfs-endpoint:managed_resource": "true", "zfs-endpoint:csi_volume_name": name}
	}
	if err := h.Z.CreateVolume(ctx, h.Parent+"/z", 1<<30, false, own("z")); err != nil {
		t.Fatal(err)
	}
	fsOwn := own("f")
	fsOwn["refquota"], fsOwn["refreservation"] = "1073741824", "1073741824"
	if err := h.Z.CreateFilesystem(ctx, h.Parent+"/f", fsOwn, false); err != nil {
		t.Fatal(err)
	}
	for _, r := range []endpoint.CreateVolumeRequest{
		{Name: "z", Kind: endpoint.Block, CapacityBytes: 1 << 30, Sparse: true},
		{Name: "f", CapacityBytes: 1 << 30},
	} {
		for i := 0; i < 2; i++ {
			if _, err := ep.CreateVolume(ctx, r); err != nil {
				t.Fatalf("%s attempt %d: %v", r.Name, i, err)
			}
		}
		if got := h.Prop(h.Parent+"/"+r.Name, "refreservation"); got != "0" {
			t.Errorf("%s: refreservation %s, want 0", r.Name, got)
		}
	}
}

// A thick filesystem's Expand sets refquota and refreservation in one
// `zfs set`; zfs applies refquota, then refreservation fails ENOSPC
// (zfs_set_prop_nvlist applies space properties one by one). The failed
// grow is rolled back: the volume keeps reporting, and reserving, its old
// size, the identical retry fails the same way, and a grow the pool can
// back succeeds. Before, the raised refquota stayed: the volume reported
// 1 PiB and every later Expand had to reserve it (ResourceExhausted). 1 PiB
// is beyond any test pool, so this runs on the simulator's space model and
// on a real pool (which may apply neither property) alike.
func TestExpandThickFilesystemRetryAfterENOSPC(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ep, h := setup(t)
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "thick", CapacityBytes: 1 << 30, Reserve: true}); err != nil {
		t.Fatal(err)
	}
	ds := h.Parent + "/thick"
	sizes := func() string { return h.Prop(ds, "refquota") + "/" + h.Prop(ds, "refreservation") }
	const huge = 1 << 50
	for i := 0; i < 3; i++ {
		_, err := ep.ExpandVolume(ctx, "thick", huge, 0)
		if endpoint.CodeOf(err) != endpoint.ResourceExhausted {
			t.Fatalf("attempt %d: %v, want ResourceExhausted", i, err)
		}
		if got := sizes(); got != "1073741824/1073741824" {
			t.Fatalf("attempt %d left refquota/refreservation %s, want the old 1 GiB both", i, got)
		}
		if v, err := ep.GetVolume(ctx, "thick"); err != nil || v.CapacityBytes != 1<<30 {
			t.Fatalf("attempt %d: volume reports %+v %v", i, v, err)
		}
	}
	if v, err := ep.ExpandVolume(ctx, "thick", 2<<30, 0); err != nil || v.CapacityBytes != 2<<30 || sizes() != "2147483648/2147483648" {
		t.Fatalf("a grow the pool backs, after the failed one: %+v %v (%s)", v, err, sizes())
	}

	// a half-applied grow the rollback never undid (killed in between):
	// Expand re-plans the reservation, never reports it done
	if err := h.Z.Set(ctx, ds, map[string]string{"refquota": fmt.Sprint(uint64(huge))}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []uint64{huge, 3 << 30} {
		if v, err := ep.ExpandVolume(ctx, "thick", c, 0); endpoint.CodeOf(err) != endpoint.ResourceExhausted {
			t.Fatalf("expand %d of a half-applied volume: %+v %v (%s), want ResourceExhausted", c, v, err, sizes())
		}
	}
}
