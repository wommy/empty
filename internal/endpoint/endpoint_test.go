package endpoint_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

const parent = "tank/k8s/vols"

func setup(t *testing.T) (*endpoint.Endpoint, *zfstest.Sim) {
	t.Helper()
	sim := zfstest.New("tank")
	z := zfs.New(sim)
	if err := z.CreateFilesystem(context.Background(), parent, nil, true); err != nil {
		t.Fatal(err)
	}
	ep, err := endpoint.New(z, endpoint.Config{Parent: parent, LockWait: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return ep, sim
}

func wantCode(t *testing.T, err error, code endpoint.Code) {
	t.Helper()
	if got := endpoint.CodeOf(err); got != code {
		t.Fatalf("code = %d (%v), want %d", got, err, code)
	}
}

func TestCreateVolumeIdempotent(t *testing.T) {
	ctx := context.Background()
	ep, sim := setup(t)
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
	if sim.Prop(parent+"/pvc-1", "democratic-csi:provision_success") != "true" {
		t.Fatal("commit marker missing")
	}

	// incompatible re-creates
	bigger := req
	bigger.CapacityBytes = 2 << 30
	_, err = ep.CreateVolume(ctx, bigger)
	wantCode(t, err, endpoint.AlreadyExists)
	fs := req
	fs.Kind = endpoint.Filesystem
	_, err = ep.CreateVolume(ctx, fs)
	wantCode(t, err, endpoint.AlreadyExists)
	limited := req
	limited.CapacityBytes, limited.LimitBytes = 1<<20, 1<<20
	_, err = ep.CreateVolume(ctx, limited)
	wantCode(t, err, endpoint.AlreadyExists)
}

func TestCreateVolumeResumesInterruptedProvision(t *testing.T) {
	ctx := context.Background()
	ep, sim := setup(t)
	// a crash after create, before the marker: owned but not committed
	z := zfs.New(sim)
	if err := z.CreateFilesystem(ctx, parent+"/pvc-2", map[string]string{
		"democratic-csi:managed_resource": "true",
		"democratic-csi:csi_volume_name":  "pvc-2",
		"refquota":                        "1073741824",
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
	if v.CapacityBytes != 1<<30 || sim.Prop(parent+"/pvc-2", "democratic-csi:provision_success") != "true" {
		t.Fatalf("not resumed: %+v", v)
	}
}

func TestNeverAdoptsOrDeletesUnmanaged(t *testing.T) {
	ctx := context.Background()
	ep, sim := setup(t)
	if err := zfs.New(sim).CreateFilesystem(ctx, parent+"/precious", nil, false); err != nil {
		t.Fatal(err)
	}
	_, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "precious"})
	wantCode(t, err, endpoint.AlreadyExists)
	wantCode(t, ep.DeleteVolume(ctx, "precious"), endpoint.FailedPrecondition)
	if !sim.Has(parent + "/precious") {
		t.Fatal("unmanaged dataset destroyed")
	}
}

func TestDeleteVolume(t *testing.T) {
	ctx := context.Background()
	ep, sim := setup(t)
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
	if !sim.Has(parent + "/pvc-3@snap-a") {
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

func TestSnapshotLifecycleWithClones(t *testing.T) {
	ctx := context.Background()
	ep, sim := setup(t)
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "src", Kind: endpoint.Block, CapacityBytes: 1 << 30}); err != nil {
		t.Fatal(err)
	}
	s, err := ep.CreateSnapshot(ctx, "snap-1", "src")
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "src@snap-1" || s.SizeBytes != 1<<30 || !s.ReadyToUse {
		t.Fatalf("snapshot %+v", s)
	}
	if again, err := ep.CreateSnapshot(ctx, "snap-1", "src"); err != nil || again != s {
		t.Fatalf("retry: %+v %v", again, err)
	}

	// names are global
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "other", Kind: endpoint.Block, CapacityBytes: 1 << 30}); err != nil {
		t.Fatal(err)
	}
	_, err = ep.CreateSnapshot(ctx, "snap-1", "other")
	wantCode(t, err, endpoint.AlreadyExists)

	// restore with growth
	c, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "restored", Kind: endpoint.Block, CapacityBytes: 2 << 30, SourceSnapshotID: "src@snap-1"})
	if err != nil {
		t.Fatal(err)
	}
	if c.CapacityBytes != 2<<30 || c.SourceType != "snapshot" || c.SourceID != "src@snap-1" {
		t.Fatalf("clone %+v", c)
	}

	// deleting a cloned snapshot succeeds (deferred), hides it, keeps the data
	if err := ep.DeleteSnapshot(ctx, "src@snap-1"); err != nil {
		t.Fatal(err)
	}
	if !sim.Has(parent + "/src@snap-1") {
		t.Fatal("cloned snapshot destroyed early")
	}
	snaps, _, err := ep.ListSnapshots(ctx, "", "", 0, "")
	if err != nil || len(snaps) != 0 {
		t.Fatalf("deferred snapshot listed: %+v %v", snaps, err)
	}
	if err := ep.DeleteVolume(ctx, "restored"); err != nil {
		t.Fatal(err)
	}
	if sim.Has(parent + "/src@snap-1") {
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
	ctx := context.Background()
	ep, sim := setup(t)
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "a", CapacityBytes: 1 << 30}); err != nil {
		t.Fatal(err)
	}
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "b", CapacityBytes: 1 << 30, SourceVolumeID: "a"}); err != nil {
		t.Fatal(err)
	}
	internal := parent + "/a@volume-source-for-volume-b"
	if !sim.Has(internal) {
		t.Fatal("internal snapshot missing")
	}
	// source cannot go while a clone depends on it
	wantCode(t, ep.DeleteVolume(ctx, "a"), endpoint.FailedPrecondition)
	if err := ep.DeleteVolume(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if sim.Has(internal) {
		t.Fatal("internal snapshot leaked")
	}
	if err := ep.DeleteVolume(ctx, "a"); err != nil {
		t.Fatal(err)
	}
}

func TestExpand(t *testing.T) {
	ctx := context.Background()
	ep, _ := setup(t)
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "e", Kind: endpoint.Block, CapacityBytes: 1 << 30}); err != nil {
		t.Fatal(err)
	}
	v, err := ep.ExpandVolume(ctx, "e", 3<<30+1, 0)
	if err != nil || v.CapacityBytes != 3<<30+16384 {
		t.Fatalf("expand %+v %v", v, err)
	}
	v, err = ep.ExpandVolume(ctx, "e", 1<<30, 0)
	if err != nil || v.CapacityBytes != 3<<30+16384 {
		t.Fatalf("shrink must be a no-op: %+v %v", v, err)
	}
	_, err = ep.ExpandVolume(ctx, "missing", 1, 0)
	wantCode(t, err, endpoint.NotFound)
}

func TestConcurrentCreatesSerialize(t *testing.T) {
	ctx := context.Background()
	ep, sim := setup(t)
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
		if len(c) > 1 && c[1] == "create" && c[len(c)-1] == parent+"/race" {
			creates++
		}
	}
	if creates != 1 {
		t.Fatalf("zfs create ran %d times", creates)
	}
}

func TestPagination(t *testing.T) {
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
}

// review H2: user properties inherit, so a snapshot an administrator took
// (or an internal one) must not look owned.
func TestInheritedOwnershipIsNotOwnership(t *testing.T) {
	ctx := context.Background()
	ep, sim := setup(t)
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "v", CapacityBytes: 1 << 30}); err != nil {
		t.Fatal(err)
	}
	if err := zfs.New(sim).Snapshot(ctx, parent+"/v@admin-backup", nil); err != nil {
		t.Fatal(err)
	}
	snaps, _, err := ep.ListSnapshots(ctx, "", "", 0, "")
	if err != nil || len(snaps) != 0 {
		t.Fatalf("admin snapshot listed: %+v %v", snaps, err)
	}
	wantCode(t, ep.DeleteSnapshot(ctx, "v@admin-backup"), endpoint.FailedPrecondition)
	if !sim.Has(parent + "/v@admin-backup") {
		t.Fatal("admin snapshot destroyed")
	}
}

// review C1 end-to-end: thick clones are idempotent after success
func TestThickCloneRetryIsIdempotent(t *testing.T) {
	ctx := context.Background()
	ep, _ := setup(t)
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "s", Kind: endpoint.Block, CapacityBytes: 1 << 30}); err != nil {
		t.Fatal(err)
	}
	req := endpoint.CreateVolumeRequest{Name: "c", Kind: endpoint.Block, CapacityBytes: 1 << 30, SourceVolumeID: "s"}
	for i := 0; i < 2; i++ {
		if _, err := ep.CreateVolume(ctx, req); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
}

// a failure between clone and resize converges on retry
func TestCloneResizeFailureConverges(t *testing.T) {
	ctx := context.Background()
	ep, sim := setup(t)
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "s", Kind: endpoint.Block, CapacityBytes: 1 << 30, Sparse: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := ep.CreateSnapshot(ctx, "snap", "s"); err != nil {
		t.Fatal(err)
	}
	fail := true
	sim.Fail = func(args []string) error {
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
