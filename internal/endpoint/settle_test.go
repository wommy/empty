package endpoint_test

import (
	"context"
	"testing"

	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

// A zvol is long-held, so `zfs destroy` is EBUSY ("dataset is busy"), while
// anything has its /dev/zdN open (zvol_first_open owns the objset;
// dsl_destroy_head_check_impl refuses a long-held dataset). zfs create -V
// returns once the minor exists (dmu_objset_create waits on the zvol
// taskq), but the device's add uevent is still being handled: udev's
// 60-zvol.rules runs zvol_id on it and the persistent-storage rules run
// blkid, both opening it. A DeleteVolume right after CreateVolume (csi-sanity
// "SingleNodeWriter NoCapacity", CI 0a0bea3: busy ~150ms after the create)
// meets that hold. OpenZFS's own create error path waits out its async
// minor handling and retries the destroy (zfs_ioctl.c zfs_ioc_create);
// the endpoint's equivalent is to settle the device events once and retry
// once. A destroy still busy after that is a volume in use:
// FAILED_PRECONDITION, as before, and never a retry loop.
//
// The simulator models the transient hold as "busy until the device events
// have settled": the settle is what releases it, so no clock is involved.
func TestDeleteVolumeSettlesDeviceEventsOnBusy(t *testing.T) {
	ctx := context.Background()
	sim := zfstest.New("tank")
	z := zfs.New(sim)
	const parent = "tank/k8s/vols"
	if err := z.CreateFilesystem(ctx, parent, nil, true); err != nil {
		t.Fatal(err)
	}
	settles := 0
	ep, err := endpoint.New(z, endpoint.Config{Parent: parent, Settle: func(context.Context) error {
		settles++
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	busy := &zfs.ExitError{Code: 1, Stderr: "cannot destroy 'x': dataset is busy\n"}
	destroys := 0
	create := func(name string) endpoint.Volume {
		t.Helper()
		v, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: name, Kind: endpoint.Block, CapacityBytes: 1 << 20})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	// udev still has the new device open: busy until the events settle
	v := create("fresh")
	held := settles
	sim.Fail = func(a []string) error {
		if a[0] != "destroy" {
			return nil
		}
		destroys++
		if settles == held {
			return busy
		}
		return nil
	}
	if err := ep.DeleteVolume(ctx, v.ID); err != nil {
		t.Fatalf("DeleteVolume of a zvol udev was still probing: %v", err)
	}
	if settles != held+1 || destroys != 2 {
		t.Fatalf("settles %d, destroys %d; want one settle between two destroys", settles-held, destroys)
	}
	if ok, err := z.Exists(ctx, v.Dataset); err != nil || ok {
		t.Fatalf("%s still exists (%v)", v.Dataset, err)
	}

	// held open by a consumer: still busy after the settle, so in use
	sim.Fail = nil
	v = create("in-use")
	held, destroys = settles, 0
	sim.Fail = func(a []string) error {
		if a[0] == "destroy" {
			destroys++
			return busy
		}
		return nil
	}
	wantCode(t, ep.DeleteVolume(ctx, v.ID), endpoint.FailedPrecondition)
	if settles != held+1 || destroys != 2 {
		t.Fatalf("settles %d, destroys %d; want exactly one settle and one retry", settles-held, destroys)
	}
}
