package endpoint_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

// Wave 25: no final code after a change. external-provisioner v5.2.0
// retries only Canceled, DeadlineExceeded, Unavailable, Aborted and
// non-gRPC errors in the background; any other code after the endpoint
// changed the pool strands the half-made volume once the PVC is deleted
// (and external-snapshotter drops its being-created annotation the same
// way). These tests inject an unclassified zfs failure ("boom") after the
// first change and want Aborted, then a retry that converges. They run on
// their own simulator (sim faults), never on a real pool.

// boom is a zfs failure no classifier knows: zfs.KindUnknown.
var boom = &zfs.ExitError{Code: 1, Stderr: "boom\n"}

// failNthScanAfter arms sim to fail, once, the nth single-volume scan
// (`get ... -d`) that follows the first command trigger matches.
func failNthScanAfter(sim *zfstest.Sim, n int, trigger func(args []string) bool) *bool {
	armed, scans, fired := false, 0, new(bool)
	sim.Fail = func(args []string) error {
		switch {
		case *fired:
		case !armed:
			armed = trigger(args)
		case args[0] == "get" && slices.Contains(args, "-d"):
			if scans++; scans == n {
				*fired = true
				return boom
			}
		}
		return nil
	}
	return fired
}

func isCreateOf(ds string) func([]string) bool {
	return func(a []string) bool { return a[0] == "create" && a[len(a)-1] == ds }
}

func isCommitOf(ds string) func([]string) bool {
	return func(a []string) bool {
		return a[0] == "set" && a[len(a)-1] == ds && strings.Contains(strings.Join(a, " "), ":provision_success=true")
	}
}

// Item 2: CreateVolume's reads after its first change (the second pass's
// scan, verifyExport's scan, the re-read after the commit) are Aborted
// when they fail unclassified, not Internal.
//
// Flipped by ADR 0010 before the commit: the fresh create rolls back, and
// with nothing left to strand the code is the read's own, Internal, as a
// read before any change is (TestCreateVolumeReadBeforeChangeStaysInternal).
// Aborted still holds where the change stays: a failed rollback (stuck),
// and the re-read after the commit, which is never rolled back.
func TestCreateVolumeReadAfterChangeIsRetryable(t *testing.T) {
	ctx := context.Background()
	const ds = "tank/k8s/vols/pvc"
	plain := endpoint.CreateVolumeRequest{Name: "pvc", CapacityBytes: 1 << 30}
	nfs := nfsCreate("pvc", "", "10.20.0.0/24")
	for _, tc := range []struct {
		name      string
		req       endpoint.CreateVolumeRequest
		trigger   func([]string) bool
		n         int
		committed bool
	}{
		{"second pass scan", plain, isCreateOf(ds), 1, false},
		{"re-read after the commit", plain, isCommitOf(ds), 1, true},
		{"NFS second pass scan", nfs, isCreateOf(ds), 1, false},
		{"NFS verifyExport scan", nfs, isCreateOf(ds), 2, false},
		{"NFS re-read after the commit", nfs, isCommitOf(ds), 1, true},
	} {
		for _, stuck := range []bool{false, true} {
			name := tc.name
			if stuck {
				name += "/rollback fails"
			}
			t.Run(name, func(t *testing.T) {
				sim, _, ep := simNFS(t, nil)
				fired := failNthScanAfter(sim, tc.n, tc.trigger)
				if stuck {
					fail := sim.Fail
					sim.Fail = func(a []string) error {
						if a[0] == "destroy" {
							return &zfs.ExitError{Code: 1, Stderr: "cannot destroy: dataset is busy\n"}
						}
						return fail(a)
					}
				}
				_, err := ep.CreateVolume(ctx, tc.req)
				if !*fired {
					t.Fatalf("the scan was never failed (%v)", err)
				}
				if tc.committed || stuck {
					wantCode(t, err, endpoint.Aborted)
				} else {
					wantCode(t, err, endpoint.Internal)
					if sim.Has(ds) {
						t.Fatal("want the fresh create rolled back")
					}
				}
				sim.Fail = nil
				v, err := ep.CreateVolume(ctx, tc.req)
				if err != nil {
					t.Fatalf("retry: %v", err)
				}
				if got := sim.Prop(ds, endpoint.DefaultPropertyPrefix+":provision_success"); got != "true" || v.Dataset != ds {
					t.Fatalf("retry: committed %q, %+v", got, v)
				}
			})
		}
	}
}

// Item 2, the other side: what CreateVolume refuses on purpose keeps its
// code after a change. A failed scan before any change stays Internal.
func TestCreateVolumeReadBeforeChangeStaysInternal(t *testing.T) {
	ctx := context.Background()
	sim, _, ep := simNFS(t, nil)
	sim.Fail = func(args []string) error {
		if args[0] == "get" && slices.Contains(args, "-d") {
			return boom
		}
		return nil
	}
	_, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "pvc", CapacityBytes: 1 << 30})
	wantCode(t, err, endpoint.Internal)
	sim.Fail = nil
	if sim.Has("tank/k8s/vols/pvc") {
		t.Fatal("created on a failed first scan")
	}
}

// afterSnapshot runs do, once, right before the first single-volume scan
// that follows a `zfs snapshot`: CreateSnapshot's re-read of the cut.
type afterSnapshot struct {
	zfs.Runner
	do      []string
	fail    error
	cut     bool
	trigger bool
}

func (r *afterSnapshot) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if r.cut && !r.trigger && args[0] == "get" && slices.Contains(args, "-d") {
		r.trigger = true
		if r.do != nil {
			if _, err := r.Runner.Run(ctx, "zfs", r.do...); err != nil {
				return nil, err
			}
		}
		if r.fail != nil {
			return nil, r.fail
		}
	}
	out, err := r.Runner.Run(ctx, name, args...)
	if args[0] == "snapshot" && err == nil {
		r.cut = true
	}
	return out, err
}

// Item 3: once CreateSnapshot cut the snapshot, a failed re-read and a
// snapshot gone by then are Aborted: a final code makes external-snapshotter
// drop its being-created annotation, and the content is then deleted
// without a DeleteSnapshot, leaking the snapshot (and refusing its source's
// DeleteVolume for good).
func TestCreateSnapshotAfterCutIsRetryable(t *testing.T) {
	ctx := context.Background()
	const snap = "tank/k8s/vols/src@s"
	for _, tc := range []struct {
		name string
		r    afterSnapshot
	}{
		{"re-read fails", afterSnapshot{fail: boom}},
		{"vanished", afterSnapshot{do: []string{"destroy", snap}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sim := zfstest.New("tank")
			r := tc.r
			r.Runner = sim
			z := zfs.New(&r)
			if err := z.CreateFilesystem(ctx, "tank/k8s/vols", nil, true); err != nil {
				t.Fatal(err)
			}
			ep, err := endpoint.New(z, endpoint.Config{Parent: "tank/k8s/vols"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "src", CapacityBytes: 1 << 30}); err != nil {
				t.Fatal(err)
			}
			_, err = ep.CreateSnapshot(ctx, "s", "src")
			if !r.trigger {
				t.Fatalf("no re-read after the cut (%v)", err)
			}
			wantCode(t, err, endpoint.Aborted)
			s, err := ep.CreateSnapshot(ctx, "s", "src")
			if err != nil || s.ID != "src@s" {
				t.Fatalf("retry: %+v %v", s, err)
			}
			if snaps, _, err := ep.ListSnapshots(ctx, "", "src", 0, ""); err != nil || len(snaps) != 1 {
				t.Fatalf("listed %+v (%v), want the one snapshot", snaps, err)
			}
		})
	}
}

// Item 4: no space for a fresh clone's reservation, after the snapshot and
// the clone ran, is Aborted, not ResourceExhausted (Finished for
// external-provisioner under Immediate binding): the uncommitted clone
// would be stranded, and with it the source's DeleteVolume ("has dependent
// clone"). A retry once the space is there converges, and the source can
// then be deleted after its clone.
//
// Flipped by ADR 0010: the fresh clone is now rolled back (the clone, and
// the snapshot cut on its source), so nothing is stranded and the code is
// ResourceExhausted, true again; Aborted holds where the clone stays (a
// failed rollback, TestCreateRollbackFailureIsJoined). The retry
// converges as before.
func TestCloneNoSpaceAfterChangeIsRetryable(t *testing.T) {
	ctx := context.Background()
	sim := zfstest.New("tank")
	z := zfs.New(sim)
	if err := z.CreateFilesystem(ctx, "tank/k8s/vols", nil, true); err != nil {
		t.Fatal(err)
	}
	ep, err := endpoint.New(z, endpoint.Config{Parent: "tank/k8s/vols"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "src", CapacityBytes: 1 << 30}); err != nil {
		t.Fatal(err)
	}
	req := endpoint.CreateVolumeRequest{Name: "dst", SourceVolumeID: "src", Reserve: true}
	cloned, fired := false, false
	sim.Fail = func(a []string) error {
		switch {
		case a[0] == "clone":
			cloned = true
		case cloned && !fired && a[0] == "set" && strings.Contains(strings.Join(a, " "), "refreservation="):
			fired = true
			return &zfs.ExitError{Code: 1, Stderr: "cannot set property for 'tank/k8s/vols/dst': size is greater than available space\n"}
		}
		return nil
	}
	_, err = ep.CreateVolume(ctx, req)
	if !fired {
		t.Fatalf("the reservation was never failed (%v)", err)
	}
	wantCode(t, err, endpoint.ResourceExhausted)
	if sim.Has("tank/k8s/vols/dst") || sim.Has("tank/k8s/vols/src@volume-source-for-volume-dst") {
		t.Fatal("want the clone and its cut rolled back")
	}
	sim.Fail = nil
	if _, err := ep.CreateVolume(ctx, req); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if err := ep.DeleteVolume(ctx, "dst"); err != nil {
		t.Fatal(err)
	}
	if err := ep.DeleteVolume(ctx, "src"); err != nil {
		t.Fatalf("source after its clone: %v", err)
	}
}

// Before any change, no space stays ResourceExhausted (rescheduled under
// WaitForFirstConsumer, final otherwise: nothing was made to strand).
func TestCreateNoSpaceBeforeChangeStaysExhausted(t *testing.T) {
	ctx := context.Background()
	sim := zfstest.New("tank")
	z := zfs.New(sim)
	if err := z.CreateFilesystem(ctx, "tank/k8s/vols", nil, true); err != nil {
		t.Fatal(err)
	}
	ep, err := endpoint.New(z, endpoint.Config{Parent: "tank/k8s/vols"})
	if err != nil {
		t.Fatal(err)
	}
	sim.Fail = func(a []string) error {
		if a[0] == "create" {
			return &zfs.ExitError{Code: 1, Stderr: "cannot create 'tank/k8s/vols/v': out of space\n"}
		}
		return nil
	}
	_, err = ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "v", Kind: endpoint.Block, CapacityBytes: 1 << 30})
	wantCode(t, err, endpoint.ResourceExhausted)
}

// Item 5: an NFS filesystem under a parent mounted nowhere (mountpoint
// none or legacy) would inherit that, never mount, and so never be
// served: created, then refused (with a root owner) or Unavailable on
// every retry. It is refused before anything is made.
func TestFileExportCreateUnderUnmountedParentIsRefused(t *testing.T) {
	ctx := context.Background()
	for _, mp := range []string{"none", "legacy"} {
		for _, owner := range []bool{false, true} {
			name := mp
			if owner {
				name += " with a root owner"
			}
			t.Run(name, func(t *testing.T) {
				sim, z, ep := simNFS(t, nil)
				if err := z.Set(ctx, "tank/k8s/vols", map[string]string{"mountpoint": mp}); err != nil {
					t.Fatal(err)
				}
				req := nfsCreate("pvc", "", "10.20.0.0/24")
				if owner {
					uid := uint32(1000)
					req.NFS.UID = &uid
				}
				_, err := ep.CreateVolume(ctx, req)
				wantCode(t, err, endpoint.FailedPrecondition)
				if !strings.Contains(err.Error(), "mountpoint="+mp) {
					t.Fatalf("not the parent's refusal: %v", err)
				}
				if sim.Has("tank/k8s/vols/pvc") {
					t.Fatal("created before the refusal")
				}
			})
		}
	}
}
