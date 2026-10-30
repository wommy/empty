package endpoint_test

import (
	"context"
	"errors"
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

// OWN-15 in the settle window: a busy destroy waits out the device events
// (Config.Settle) before its retry, the longest unguarded gap of any
// destroy. A volume renamed away and replaced by an impostor with the same
// markers meanwhile must not be destroyed by the retry: the guid is checked
// again after the settle. And a settle that fails reports the busy it was
// meant to clear, with no retry.
func TestDeleteVolumeSettleWindow(t *testing.T) {
	ctx := context.Background()
	busy := &zfs.ExitError{Code: 1, Stderr: "cannot destroy 'x': dataset is busy\n"}
	setup := func(t *testing.T, settle func(context.Context) error) (*zfstest.Sim, *zfs.Client, *endpoint.Endpoint, endpoint.Volume) {
		t.Helper()
		sim := zfstest.New("tank")
		z := zfs.New(sim)
		if err := z.CreateFilesystem(ctx, "tank/k8s/vols", nil, true); err != nil {
			t.Fatal(err)
		}
		ep, err := endpoint.New(z, endpoint.Config{Parent: "tank/k8s/vols", Settle: settle})
		if err != nil {
			t.Fatal(err)
		}
		v, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "pvc", Kind: endpoint.Block, CapacityBytes: 1 << 20})
		if err != nil {
			t.Fatal(err)
		}
		// the first destroy of the volume is busy
		first := true
		sim.Fail = func(a []string) error {
			if a[0] == "destroy" && a[len(a)-1] == v.Dataset && first {
				first = false
				return busy
			}
			return nil
		}
		return sim, z, ep, v
	}
	destroys := func(sim *zfstest.Sim) (n int) {
		for _, c := range sim.Calls {
			if len(c) > 1 && c[1] == "destroy" {
				n++
			}
		}
		return n
	}
	guid := func(t *testing.T, z *zfs.Client, name string) string {
		t.Helper()
		d, err := z.Get(ctx, name, "guid")
		if err != nil {
			t.Fatal(err)
		}
		return d.Props["guid"].Value
	}

	t.Run("replaced while settling", func(t *testing.T) {
		var sim *zfstest.Sim
		var z *zfs.Client
		var v endpoint.Volume
		var impostor string
		sim, z, ep, v := setup(t, func(ctx context.Context) error {
			d, err := z.Get(ctx, v.Dataset, swapMarkers...)
			if err != nil {
				return err
			}
			marks := map[string]string{}
			for _, k := range swapMarkers {
				if p := d.Props[k]; p.Local() {
					marks[k] = p.Value
				}
			}
			if _, err := sim.Run(ctx, "zfs", "rename", v.Dataset, v.Dataset+"-moved"); err != nil {
				return err
			}
			if err := z.CreateVolume(ctx, v.Dataset, 1<<20, false, marks); err != nil {
				return err
			}
			impostor = guid(t, z, v.Dataset)
			return nil
		})
		original := guid(t, z, v.Dataset)
		wantCode(t, ep.DeleteVolume(ctx, v.ID), endpoint.Aborted)
		if impostor == "" || guid(t, z, v.Dataset) != impostor {
			t.Fatalf("impostor %q at %s destroyed or never made", impostor, v.Dataset)
		}
		if got := guid(t, z, v.Dataset+"-moved"); got != original {
			t.Fatalf("original moved away: guid %s, want %s", got, original)
		}
		if n := destroys(sim); n != 1 {
			t.Fatalf("%d destroys, want only the busy one", n)
		}
	})

	t.Run("settle fails", func(t *testing.T) {
		sim, z, ep, v := setup(t, func(context.Context) error { return errors.New("udevadm settle: timeout") })
		err := ep.DeleteVolume(ctx, v.ID)
		wantCode(t, err, endpoint.FailedPrecondition)
		if !errors.Is(err, zfs.ErrBusy) {
			t.Fatalf("not the busy the settle was meant to clear: %v", err)
		}
		if n := destroys(sim); n != 1 {
			t.Fatalf("%d destroys after a failed settle, want 1", n)
		}
		if ok, err := z.Exists(ctx, v.Dataset); err != nil || !ok {
			t.Fatalf("%s gone (%v)", v.Dataset, err)
		}
	})
}
