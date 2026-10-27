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
