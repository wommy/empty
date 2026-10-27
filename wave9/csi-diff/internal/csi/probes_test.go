package csi_test

// Probes (wave9): one deterministic call per divergence class the fuzzer
// found, logged for csi-diff.md. They assert nothing; run with -v.
//
//   go test ./internal/csi -run TestCSIProbes -v

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/prototext"
)

func TestCSIProbes(t *testing.T) {
	ctx := context.Background()
	s, err := newStack(shortParent)
	if err != nil {
		t.Fatal(err)
	}
	defer s.stop()
	c := s.ctl
	show := func(what string, resp any, err error) {
		t.Helper()
		if err != nil {
			t.Logf("%-58s -> %v: %s", what, status.Code(err), status.Convert(err).Message())
			return
		}
		m := fmt.Sprint(resp)
		if p, ok := resp.(interface{ ProtoReflect() }); ok {
			_ = p
		}
		t.Logf("%-58s -> OK %s", what, strings.TrimSpace(m))
	}
	mnt := []*csi.VolumeCapability{mountCap(snw, "")}
	blk := []*csi.VolumeCapability{blockCap(snw)}
	gi := &csi.CapacityRange{RequiredBytes: 1 << 30}

	// names the spec allows (spec:849-851)
	for _, n := range []string{"πβ-volume", "with space", "a/b", "a@b", "tab\tname", "-leading", strings.Repeat("a", 128)} {
		r, err := c.CreateVolume(ctx, &csi.CreateVolumeRequest{Name: n, VolumeCapabilities: mnt, CapacityRange: gi})
		show(fmt.Sprintf("CreateVolume name %q", n), r.GetVolume().GetVolumeId(), err)
	}
	for _, n := range []string{"tab\tsnap", "volume-source-for-volume-x", "π-snap", strings.Repeat("c", 128)} {
		r, err := c.CreateSnapshot(ctx, &csi.CreateSnapshotRequest{Name: n, SourceVolumeId: "with space"})
		show(fmt.Sprintf("CreateSnapshot name %q", n), r.GetSnapshot().GetSnapshotId(), err)
	}
	_, err = c.CreateSnapshot(ctx, &csi.CreateSnapshotRequest{Name: "s128", SourceVolumeId: strings.Repeat("a", 128)})
	show("CreateSnapshot of a 128-byte volume id", nil, err)

	// long parent: a 128-byte name no longer fits zfs's 255-byte names
	ls, err := newStack(longParent)
	if err != nil {
		t.Fatal(err)
	}
	defer ls.stop()
	_, err = ls.ctl.CreateVolume(ctx, &csi.CreateVolumeRequest{Name: strings.Repeat("a", 128), VolumeCapabilities: mnt, CapacityRange: gi})
	show(fmt.Sprintf("long parent (%d bytes) + 128-byte name", len(longParent)), nil, err)

	// content source with an empty id
	r, err := c.CreateVolume(ctx, &csi.CreateVolumeRequest{Name: "src-empty", VolumeCapabilities: mnt, CapacityRange: gi,
		VolumeContentSource: &csi.VolumeContentSource{Type: &csi.VolumeContentSource_Snapshot{Snapshot: &csi.VolumeContentSource_SnapshotSource{}}}})
	show("CreateVolume source snapshot{snapshot_id:\"\"}", prototext.Format(r.GetVolume()), err)

	// mutable_parameters / accessibility_requirements are not checked
	_, err = c.CreateVolume(ctx, &csi.CreateVolumeRequest{Name: "mut", VolumeCapabilities: mnt, CapacityRange: gi, MutableParameters: map[string]string{"bogus": "1"}})
	show("CreateVolume mutable_parameters{bogus}", nil, err)
	_, err = c.CreateVolume(ctx, &csi.CreateVolumeRequest{Name: "mut2", VolumeCapabilities: mnt, CapacityRange: gi, Parameters: map[string]string{"bogus": "1"}})
	show("CreateVolume parameters{bogus}", nil, err)

	// block without capacity; filesystem without capacity then with one
	_, err = c.CreateVolume(ctx, &csi.CreateVolumeRequest{Name: "blk-nocap", VolumeCapabilities: blk})
	show("CreateVolume block, no capacity_range", nil, err)
	r, err = c.CreateVolume(ctx, &csi.CreateVolumeRequest{Name: "fs-nocap", VolumeCapabilities: mnt})
	show("CreateVolume mount, no capacity_range", r.GetVolume().GetCapacityBytes(), err)
	_, err = c.CreateVolume(ctx, &csi.CreateVolumeRequest{Name: "fs-nocap", VolumeCapabilities: mnt, CapacityRange: gi})
	show("  retry with required 1Gi (unlimited fs)", nil, err)

	// zvol: Validate confirms mount, CreateVolume with mount refuses
	z, _ := c.CreateVolume(ctx, &csi.CreateVolumeRequest{Name: "zv", VolumeCapabilities: blk, CapacityRange: gi})
	v, err := c.ValidateVolumeCapabilities(ctx, &csi.ValidateVolumeCapabilitiesRequest{VolumeId: z.GetVolume().GetVolumeId(), VolumeCapabilities: mnt})
	show("Validate zvol with mount", v.GetConfirmed() != nil, err)
	_, err = c.CreateVolume(ctx, &csi.CreateVolumeRequest{Name: "zv", VolumeCapabilities: mnt, CapacityRange: gi})
	show("  CreateVolume zv again with mount", nil, err)
	v, err = c.ValidateVolumeCapabilities(ctx, &csi.ValidateVolumeCapabilitiesRequest{VolumeId: z.GetVolume().GetVolumeId(), VolumeCapabilities: blk,
		VolumeContext: map[string]string{"dataset": "tank/elsewhere"}})
	show("Validate with a foreign volume_context", prototext.Format(v.GetConfirmed()), err)

	// expand: block capability on a filesystem, empty capability
	f, _ := c.CreateVolume(ctx, &csi.CreateVolumeRequest{Name: "fsx", VolumeCapabilities: mnt, CapacityRange: gi})
	e, err := c.ControllerExpandVolume(ctx, &csi.ControllerExpandVolumeRequest{VolumeId: f.GetVolume().GetVolumeId(), CapacityRange: &csi.CapacityRange{RequiredBytes: 2 << 30}, VolumeCapability: blockCap(snw)})
	show("Expand filesystem with block capability", e, err)
	e, err = c.ControllerExpandVolume(ctx, &csi.ControllerExpandVolumeRequest{VolumeId: f.GetVolume().GetVolumeId(), CapacityRange: &csi.CapacityRange{RequiredBytes: 2 << 30}, VolumeCapability: &csi.VolumeCapability{}})
	show("Expand with an empty VolumeCapability{}", e, err)
	e, err = c.ControllerExpandVolume(ctx, &csi.ControllerExpandVolumeRequest{VolumeId: f.GetVolume().GetVolumeId(), CapacityRange: &csi.CapacityRange{RequiredBytes: 3 << 30, LimitBytes: 1 << 30}})
	show("Expand required > limit", e, err)
	e, err = c.ControllerExpandVolume(ctx, &csi.ControllerExpandVolumeRequest{VolumeId: f.GetVolume().GetVolumeId(), CapacityRange: &csi.CapacityRange{RequiredBytes: 1 << 30, LimitBytes: 1 << 30}})
	show("Expand to 1Gi/1Gi a 2Gi volume", e, err)
	_, err = c.CreateVolume(ctx, &csi.CreateVolumeRequest{Name: "inv", VolumeCapabilities: mnt, CapacityRange: &csi.CapacityRange{RequiredBytes: 2 << 30, LimitBytes: 1 << 30}})
	show("CreateVolume required > limit (CSI layer)", nil, err)
	_, err = c.CreateVolume(ctx, &csi.CreateVolumeRequest{Name: "rnd", VolumeCapabilities: blk, CapacityRange: &csi.CapacityRange{RequiredBytes: 1<<30 + 1, LimitBytes: 1<<30 + 1}})
	show("CreateVolume block 1Gi+1/1Gi+1 (endpoint rounding)", nil, err)

	// GetCapacity ignores capabilities and parameters
	g1, _ := c.GetCapacity(ctx, &csi.GetCapacityRequest{})
	g2, err := c.GetCapacity(ctx, &csi.GetCapacityRequest{VolumeCapabilities: []*csi.VolumeCapability{mountCap(mnmw, "ext4")}, Parameters: map[string]string{"bogus": "1"}})
	show(fmt.Sprintf("GetCapacity plain=%d, unservable caps+params", g1.GetAvailableCapacity()), g2.GetAvailableCapacity(), err)

	// stale ListVolumes token: delete the volume the token names
	p1, _ := c.ListVolumes(ctx, &csi.ListVolumesRequest{MaxEntries: 1})
	tok := p1.GetNextToken()
	_, _ = c.DeleteVolume(ctx, &csi.DeleteVolumeRequest{VolumeId: tok})
	_, err = c.ListVolumes(ctx, &csi.ListVolumesRequest{MaxEntries: 1, StartingToken: tok})
	show(fmt.Sprintf("ListVolumes with token %q after deleting it", tok), nil, err)

	// snapshot ready_to_use and size of a filesystem snapshot
	sn, err := c.CreateSnapshot(ctx, &csi.CreateSnapshotRequest{Name: "fsx-snap", SourceVolumeId: f.GetVolume().GetVolumeId()})
	show("CreateSnapshot of a 2Gi filesystem", prototext.Format(sn.GetSnapshot()), err)
	sn2, err := c.CreateSnapshot(ctx, &csi.CreateSnapshotRequest{Name: "nocap-snap", SourceVolumeId: "fs-nocap"})
	show("CreateSnapshot of an unlimited filesystem", prototext.Format(sn2.GetSnapshot()), err)
	cl, err := c.CreateVolume(ctx, &csi.CreateVolumeRequest{Name: "restore-small", VolumeCapabilities: mnt, CapacityRange: &csi.CapacityRange{RequiredBytes: 1 << 30},
		VolumeContentSource: &csi.VolumeContentSource{Type: &csi.VolumeContentSource_Snapshot{Snapshot: &csi.VolumeContentSource_SnapshotSource{SnapshotId: sn.GetSnapshot().GetSnapshotId()}}}})
	show("restore 2Gi snapshot with required 1Gi", cl.GetVolume().GetCapacityBytes(), err)
	_, err = c.DeleteVolume(ctx, &csi.DeleteVolumeRequest{VolumeId: f.GetVolume().GetVolumeId()})
	show("DeleteVolume with a snapshot", nil, err)
}
