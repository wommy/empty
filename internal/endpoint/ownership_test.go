package endpoint_test

import (
	"context"
	"testing"

	"github.com/wommy/empty/internal/endpoint"
)

// ADR 0003, "never touch what you don't own": a clone source must be a
// committed volume the endpoint owns, and a snapshot source a CSI snapshot
// of one. Anything else names no volume or snapshot the endpoint issued,
// so it is NotFound, and nothing is snapshotted or cloned.
func TestCloneSourcesMustBeOwned(t *testing.T) {
	ctx := context.Background()
	ep, h := setup(t)
	if err := h.Z.CreateFilesystem(ctx, h.Parent+"/foreign", map[string]string{"refquota": "1048576"}, false); err != nil {
		t.Fatal(err)
	}
	if err := h.Z.Snapshot(ctx, h.Parent+"/foreign@admin", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "v", CapacityBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	// an administrator's snapshot of an owned volume is not a CSI snapshot
	if err := h.Z.Snapshot(ctx, h.Parent+"/v@admin", nil); err != nil {
		t.Fatal(err)
	}
	// a clone from v leaves the internal clone-source snapshot behind
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "c0", SourceVolumeID: "v"}); err != nil {
		t.Fatal(err)
	}
	// an uncommitted volume: an interrupted provision, never returned
	if err := h.Z.CreateFilesystem(ctx, h.Parent+"/half", map[string]string{
		"democratic-csi:managed_resource": "true", "democratic-csi:csi_volume_name": "half",
	}, false); err != nil {
		t.Fatal(err)
	}
	for _, r := range []endpoint.CreateVolumeRequest{
		{Name: "c1", SourceVolumeID: "foreign"},
		{Name: "c2", SourceSnapshotID: "foreign@admin"},
		{Name: "c3", SourceSnapshotID: "v@admin"},
		{Name: "c4", SourceSnapshotID: "v@volume-source-for-volume-c0"},
		{Name: "c5", SourceVolumeID: "half"},
	} {
		_, err := ep.CreateVolume(ctx, r)
		if endpoint.CodeOf(err) != endpoint.NotFound {
			t.Errorf("%s from %q%q: %v, want NotFound", r.Name, r.SourceVolumeID, r.SourceSnapshotID, err)
		}
		if h.Has(h.Parent + "/" + r.Name) {
			t.Errorf("%s was cloned", r.Name)
		}
	}
	for _, s := range []string{"foreign@volume-source-for-volume-c1", "half@volume-source-for-volume-c5"} {
		if h.Has(h.Parent + "/" + s) {
			t.Errorf("snapshot %s cut on a dataset the endpoint does not own", s)
		}
	}
}

// A backup copy received next to the original (`zfs send -R v@x | zfs recv
// v-copy`) carries every user property with source "received". It is not
// the endpoint's: not listed, not deletable, and its snapshots do not
// shadow the original's CSI snapshot names.
func TestReceivedCopyIsNotOwned(t *testing.T) {
	ctx := context.Background()
	ep, h := setup(t)
	sim := h.Sim(t)
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "pvc-1", CapacityBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	if _, err := ep.CreateSnapshot(ctx, "snapA", "pvc-1"); err != nil {
		t.Fatal(err)
	}
	cp := h.Parent + "/pvc-1-copy"
	if err := h.Z.CreateFilesystem(ctx, cp, nil, false); err != nil {
		t.Fatal(err)
	}
	if err := h.Z.Snapshot(ctx, cp+"@snapA", nil); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"managed_resource": "true", "provision_success": "true", "csi_volume_name": "pvc-1"} {
		sim.Receive(cp, "democratic-csi:"+k, v)
	}
	for k, v := range map[string]string{"managed_resource": "true", "csi_snapshot_name": "snapA", "csi_snapshot_source_volume_id": "pvc-1"} {
		sim.Receive(cp+"@snapA", "democratic-csi:"+k, v)
	}

	vols, _, err := ep.ListVolumes(ctx, 0, "")
	if err != nil || len(vols) != 1 || vols[0].ID != "pvc-1" {
		t.Errorf("ListVolumes: %+v %v", vols, err)
	}
	snaps, _, err := ep.ListSnapshots(ctx, "", "", 0, "")
	if err != nil || len(snaps) != 1 || snaps[0].ID != "pvc-1@snapA" {
		t.Errorf("ListSnapshots: %+v %v", snaps, err)
	}
	if _, err := ep.CreateSnapshot(ctx, "snapA", "pvc-1"); err != nil {
		t.Errorf("CreateSnapshot retry: %v", err)
	}
	if _, err := ep.GetVolume(ctx, "pvc-1-copy"); endpoint.CodeOf(err) != endpoint.NotFound {
		t.Errorf("GetVolume copy: %v", err)
	}
	if err := ep.DeleteVolume(ctx, "pvc-1-copy"); endpoint.CodeOf(err) != endpoint.FailedPrecondition {
		t.Errorf("DeleteVolume copy: %v", err)
	}
	if err := ep.DeleteSnapshot(ctx, "pvc-1-copy@snapA"); endpoint.CodeOf(err) != endpoint.FailedPrecondition {
		t.Errorf("DeleteSnapshot copy: %v", err)
	}
	if !h.Has(cp) || !h.Has(cp+"@snapA") {
		t.Error("received copy destroyed")
	}
}
