package endpoint_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wommy/empty/internal/endpoint"
)

// ADR 0003: never touch what you don't own. A snapshot without a locally
// set csi_snapshot_name (an administrator's, a backup tool's, a received
// one) is not the endpoint's, and `zfs destroy -r` of its volume would take
// it. DeleteVolume refuses, naming them, unless the operator opted in.
func TestDeleteVolumeSparesForeignSnapshots(t *testing.T) {
	ctx := context.Background()
	ep, h := setup(t)
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "pvc"}); err != nil {
		t.Fatal(err)
	}
	ds := h.Parent + "/pvc"
	if err := h.Z.Snapshot(ctx, ds+"@backup-1", nil); err != nil {
		t.Fatal(err)
	}
	// a CSI name that is not set locally (received) is not ownership either
	if err := h.Z.Snapshot(ctx, ds+"@copied", nil); err != nil {
		t.Fatal(err)
	}
	sim := h.Sim(t)
	sim.Receive(ds+"@copied", "zfs-endpoint:managed_resource", "true")
	sim.Receive(ds+"@copied", "zfs-endpoint:csi_snapshot_name", "copied")
	err := ep.DeleteVolume(ctx, "pvc")
	wantCode(t, err, endpoint.FailedPrecondition)
	if !strings.Contains(err.Error(), "pvc@backup-1") || !strings.Contains(err.Error(), "pvc@copied") {
		t.Fatalf("refusal does not name the snapshots: %v", err)
	}
	if !h.Has(ds+"@backup-1") || !h.Has(ds) {
		t.Fatal("touched")
	}

	// reconcile leaves the volume too, saying why
	old, err := endpoint.New(h.Z, endpoint.Config{Parent: h.Parent, LockWait: time.Second,
		Now: func() time.Time { return time.Now().Add(48 * time.Hour) }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "keep"}); err != nil {
		t.Fatal(err)
	}
	p, err := old.Reconcile(ctx, endpoint.ReconcileRequest{DesiredVolumeIDs: []string{"keep"}})
	if err != nil || len(p.Actions) != 0 || len(p.Skipped) != 1 || p.Skipped[0].Target != "pvc" ||
		!strings.Contains(p.Skipped[0].Reason, "pvc@backup-1") {
		t.Fatalf("reconcile: %+v %v", p, err)
	}
	if !h.Has(ds + "@backup-1") {
		t.Fatal("reconcile destroyed a foreign snapshot")
	}

	// the operator's opt-in: -destroy-foreign-snapshots
	optIn, err := endpoint.New(h.Z, endpoint.Config{Parent: h.Parent, LockWait: time.Second, DestroyForeignSnapshots: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := optIn.DeleteVolume(ctx, "pvc"); err != nil {
		t.Fatal(err)
	}
	if h.Has(ds) {
		t.Fatal("not destroyed with the opt-in")
	}
}

// The endpoint's own internal clone-source snapshot is not foreign: a
// volume whose clone is gone deletes as before.
func TestDeleteVolumeAfterItsCloneIsGone(t *testing.T) {
	ctx := context.Background()
	ep, _ := setup(t)
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "src"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "dst", SourceVolumeID: "src"}); err != nil {
		t.Fatal(err)
	}
	if err := ep.DeleteVolume(ctx, "dst"); err != nil {
		t.Fatal(err)
	}
	if err := ep.DeleteVolume(ctx, "src"); err != nil {
		t.Fatal(err)
	}
}
