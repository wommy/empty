package endpoint_test

import (
	"context"
	"testing"

	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/zfs"
)

// zfs prints "dataset does not exist" for an exported pool too. Every call
// that scans the whole parent reports a missing parent as Unavailable, as
// the single-volume scan does: NotFound is final for the CSI sidecars.
func TestMissingParentIsUnavailableForWholeScans(t *testing.T) {
	t.Parallel()
	requireSim(t)
	ctx := context.Background()
	ep, h := setup(t)
	h.Sim(t)
	if err := h.Z.Destroy(ctx, h.Parent, zfs.DestroyOptions{Recursive: true}); err != nil {
		t.Fatal(err)
	}
	_, err2 := ep.CreateSnapshot(ctx, "s", "v")
	_, _, err3 := ep.ListVolumes(ctx, 0, "")
	_, _, err4 := ep.ListSnapshots(ctx, "", "", 0, "")
	_, err5 := ep.GetCapacity(ctx)
	_, err6 := ep.Reconcile(ctx, endpoint.ReconcileRequest{})
	for name, err := range map[string]error{
		"DeleteVolume": ep.DeleteVolume(ctx, "v"), "CreateSnapshot": err2, "ListVolumes": err3,
		"ListSnapshots": err4, "GetCapacity": err5, "Reconcile": err6,
	} {
		if endpoint.CodeOf(err) != endpoint.Unavailable {
			t.Errorf("%s: code %d (%v), want Unavailable", name, endpoint.CodeOf(err), err)
		}
	}
}

// A missing parent (or its pool exported: zfs says "dataset does not
// exist" for both) is Unavailable for every CreateVolume and DeleteVolume,
// never NotFound (final for the CSI sidecars: a delete would be forgotten,
// a clone's source reported gone) and never "absent, so create". Each
// kind proves the parent its own way (a scan's Exists, the parent's
// mountpoint, a source scan, the delete's snapshot listing): a cut to any
// of them must keep this.
func TestMissingParentFailsCreateAndDeleteUnavailable(t *testing.T) {
	t.Parallel()
	requireSim(t)
	ctx := context.Background()
	ep, h := setup(t)
	h.Sim(t)
	for _, r := range []endpoint.CreateVolumeRequest{
		{Name: "srcb", Kind: endpoint.Block, CapacityBytes: 1 << 30},
		{Name: "srcf", CapacityBytes: 1 << 30},
	} {
		if _, err := ep.CreateVolume(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := ep.CreateSnapshot(ctx, "s", "srcb")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Z.Destroy(ctx, h.Parent, zfs.DestroyOptions{Recursive: true}); err != nil {
		t.Fatal(err)
	}
	for name, r := range map[string]endpoint.CreateVolumeRequest{
		"block":             {Name: "b", Kind: endpoint.Block, CapacityBytes: 1 << 30},
		"filesystem":        {Name: "f", CapacityBytes: 1 << 30},
		"clone of snapshot": {Name: "cs", Kind: endpoint.Block, CapacityBytes: 1 << 30, SourceSnapshotID: snap.ID},
		"clone of volume":   {Name: "cv", CapacityBytes: 1 << 30, SourceVolumeID: "srcf"},
		"existing volume":   {Name: "srcf", CapacityBytes: 1 << 30},
	} {
		if _, err := ep.CreateVolume(ctx, r); endpoint.CodeOf(err) != endpoint.Unavailable {
			t.Errorf("CreateVolume %s: code %d (%v), want Unavailable", name, endpoint.CodeOf(err), err)
		}
	}
	for _, id := range []string{"never", "srcb", "srcf"} {
		if err := ep.DeleteVolume(ctx, id); endpoint.CodeOf(err) != endpoint.Unavailable {
			t.Errorf("DeleteVolume %s: code %d (%v), want Unavailable", id, endpoint.CodeOf(err), err)
		}
	}
	if h.Has(h.Parent) {
		t.Fatal("a create made the missing parent")
	}
}
