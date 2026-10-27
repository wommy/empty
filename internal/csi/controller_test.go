package csi_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/wommy/empty/gen/zfsendpoint/v1"
	zcsi "github.com/wommy/empty/internal/csi"
)

func capability(block bool) *csi.VolumeCapability {
	vc := &csi.VolumeCapability{AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER}}
	if block {
		vc.AccessType = &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}}
	} else {
		vc.AccessType = &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}}
	}
	return vc
}

func TestCapabilitiesAdvertised(t *testing.T) {
	t.Parallel()
	c := &zcsi.Controller{EP: endpointClient(t)}
	res, err := c.ControllerGetCapabilities(context.Background(), &csi.ControllerGetCapabilitiesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var got []csi.ControllerServiceCapability_RPC_Type
	for _, cp := range res.GetCapabilities() {
		got = append(got, cp.GetRpc().GetType())
	}
	want := []csi.ControllerServiceCapability_RPC_Type{
		csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
		csi.ControllerServiceCapability_RPC_CREATE_DELETE_SNAPSHOT,
		csi.ControllerServiceCapability_RPC_LIST_VOLUMES,
		csi.ControllerServiceCapability_RPC_LIST_SNAPSHOTS,
		csi.ControllerServiceCapability_RPC_GET_CAPACITY,
		csi.ControllerServiceCapability_RPC_CLONE_VOLUME,
		csi.ControllerServiceCapability_RPC_EXPAND_VOLUME,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("capabilities %v, want %v", got, want)
	}
}

// Only a zvol carrying a filesystem needs the node to grow it; democratic-csi
// also asks for node expansion on raw block, which no node can act on.
func TestExpandNodeExpansionRequired(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := &zcsi.Controller{EP: endpointClient(t), EnableBlock: true}
	for _, block := range []bool{true, false} {
		name := "fs"
		if block {
			name = "zvol"
		}
		res, err := c.CreateVolume(ctx, &csi.CreateVolumeRequest{
			Name: name, VolumeCapabilities: []*csi.VolumeCapability{capability(block)},
			CapacityRange: &csi.CapacityRange{RequiredBytes: 1 << 30},
		})
		if err != nil {
			t.Fatal(err)
		}
		wantKind := "filesystem"
		if block {
			wantKind = "block"
		}
		if k := res.GetVolume().GetVolumeContext()[zcsi.CtxKind]; k != wantKind {
			t.Fatalf("%s: kind %q, want %q", name, k, wantKind)
		}
	}
	for _, tc := range []struct {
		vol  string
		vc   *csi.VolumeCapability
		node bool
	}{
		{"zvol", capability(true), false}, // raw block
		{"zvol", capability(false), true}, // filesystem on a zvol
		{"zvol", nil, true},               // unknown: the zvol may carry a filesystem
		{"fs", capability(false), false},  // refquota is all there is
		{"fs", nil, false},
	} {
		res, err := c.ControllerExpandVolume(ctx, &csi.ControllerExpandVolumeRequest{
			VolumeId: tc.vol, CapacityRange: &csi.CapacityRange{RequiredBytes: 2 << 30}, VolumeCapability: tc.vc,
		})
		if err != nil {
			t.Fatal(err)
		}
		if res.GetNodeExpansionRequired() != tc.node || res.GetCapacityBytes() < 2<<30 {
			t.Errorf("%s with %v: %+v", tc.vol, tc.vc, res)
		}
	}
}

func TestParametersAndAccessChecks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ep := endpointClient(t)
	c := &zcsi.Controller{EP: ep, EnableBlock: true}
	create := func(name string, block bool, params map[string]string) error {
		_, err := c.CreateVolume(ctx, &csi.CreateVolumeRequest{
			Name: name, VolumeCapabilities: []*csi.VolumeCapability{capability(block)},
			CapacityRange: &csi.CapacityRange{RequiredBytes: 1 << 30}, Parameters: params,
		})
		return err
	}
	if err := create("thin", true, map[string]string{"sparse": "true", "csi.storage.k8s.io/pvc/name": "x"}); err != nil {
		t.Fatal(err)
	}
	if v, err := ep.GetVolume(ctx, &pb.GetVolumeRequest{VolumeId: "thin"}); err != nil || v.GetVolume().GetKind() != pb.VolumeKind_VOLUME_KIND_BLOCK {
		t.Fatalf("thin: %v %v", v, err)
	}
	// the endpoint compares sparse: a thick repeat is incompatible
	if err := create("thin", true, nil); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("thick repeat: %v", err)
	}
	if err := create("res", false, map[string]string{"reserve": "true"}); err != nil {
		t.Fatal(err)
	}
	for name, params := range map[string]map[string]string{
		"unknown":  {"compression": "zstd"},
		"notabool": {"sparse": "maybe"},
	} {
		if err := create(name, true, params); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	multi := capability(false)
	multi.AccessMode.Mode = csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER
	if _, err := c.CreateVolume(ctx, &csi.CreateVolumeRequest{Name: "m", VolumeCapabilities: []*csi.VolumeCapability{multi}}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("multi-node: %v", err)
	}

	validate := func(vol string, vc *csi.VolumeCapability) bool {
		res, err := c.ValidateVolumeCapabilities(ctx, &csi.ValidateVolumeCapabilitiesRequest{VolumeId: vol, VolumeCapabilities: []*csi.VolumeCapability{vc}})
		if err != nil {
			t.Fatal(err)
		}
		return res.GetConfirmed() != nil
	}
	if !validate("thin", capability(true)) || !validate("thin", capability(false)) || !validate("res", capability(false)) {
		t.Error("supported capability not confirmed")
	}
	if validate("res", capability(true)) || validate("res", multi) {
		t.Error("unsupported capability confirmed")
	}
}

// Block is opt-in until a node plugin exists (ADR 0006). Off (the default),
// CreateVolume refuses block access with InvalidArgument and creates
// nothing, and ValidateVolumeCapabilities never confirms block access, not
// even on an existing zvol; mount access is unaffected. On, both admit it.
func TestBlockIsOptIn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ep := endpointClient(t)
	off := &zcsi.Controller{EP: ep}
	on := &zcsi.Controller{EP: ep, EnableBlock: true}
	create := func(c *zcsi.Controller, name string, caps ...*csi.VolumeCapability) error {
		_, err := c.CreateVolume(ctx, &csi.CreateVolumeRequest{
			Name: name, VolumeCapabilities: caps, CapacityRange: &csi.CapacityRange{RequiredBytes: 1 << 30},
		})
		return err
	}
	validate := func(c *zcsi.Controller, vol string, vc *csi.VolumeCapability) (bool, string) {
		res, err := c.ValidateVolumeCapabilities(ctx, &csi.ValidateVolumeCapabilitiesRequest{VolumeId: vol, VolumeCapabilities: []*csi.VolumeCapability{vc}})
		if err != nil {
			t.Fatal(err)
		}
		return res.GetConfirmed() != nil, res.GetMessage()
	}

	err := create(off, "blk", capability(true))
	if status.Code(err) != codes.InvalidArgument || !strings.Contains(status.Convert(err).Message(), "-enable-block") {
		t.Fatalf("block with block off: %v", err)
	}
	if _, err := ep.GetVolume(ctx, &pb.GetVolumeRequest{VolumeId: "blk"}); status.Code(err) != codes.NotFound {
		t.Fatalf("refused block volume exists: %v", err)
	}
	if err := create(off, "fs", capability(false)); err != nil {
		t.Fatalf("mount with block off: %v", err)
	}
	if err := create(on, "zvol", capability(true)); err != nil {
		t.Fatalf("block with block on: %v", err)
	}
	if ok, msg := validate(off, "zvol", capability(true)); ok || !strings.Contains(msg, "-enable-block") {
		t.Errorf("block confirmed with block off: %q", msg)
	}
	if ok, _ := validate(off, "zvol", capability(false)); !ok {
		t.Error("mount on a zvol not confirmed with block off")
	}
	if ok, msg := validate(on, "zvol", capability(true)); !ok {
		t.Errorf("block not confirmed with block on: %q", msg)
	}
}

// Ids are opaque: one this driver could never have issued names nothing.
// DeleteVolume of a missing volume MUST be OK (CSI); the other RPCs report
// NOT_FOUND, never INVALID_ARGUMENT (final for the resizer).
func TestNeverIssuedIDs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := &zcsi.Controller{EP: endpointClient(t)}
	long := strings.Repeat("x", 200)
	for _, id := range []string{"tank/vols/pvc-1", "a@b", long} {
		if _, err := c.DeleteVolume(ctx, &csi.DeleteVolumeRequest{VolumeId: id}); err != nil {
			t.Errorf("DeleteVolume %q: %v", id, err)
		}
		_, err := c.ValidateVolumeCapabilities(ctx, &csi.ValidateVolumeCapabilitiesRequest{VolumeId: id, VolumeCapabilities: []*csi.VolumeCapability{capability(false)}})
		if status.Code(err) != codes.NotFound {
			t.Errorf("Validate %q: %v", id, err)
		}
		_, err = c.ControllerExpandVolume(ctx, &csi.ControllerExpandVolumeRequest{VolumeId: id, CapacityRange: &csi.CapacityRange{RequiredBytes: 1 << 30}})
		if status.Code(err) != codes.NotFound {
			t.Errorf("Expand %q: %v", id, err)
		}
		_, err = c.CreateSnapshot(ctx, &csi.CreateSnapshotRequest{Name: "s", SourceVolumeId: id})
		if status.Code(err) != codes.NotFound {
			t.Errorf("CreateSnapshot from %q: %v", id, err)
		}
	}
}

// capacity_bytes is REQUIRED in the expand response and means the capacity
// after expansion: an unlimited filesystem satisfies any request, so it
// reports the request, never 0.
func TestExpandUnlimitedFilesystem(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := &zcsi.Controller{EP: endpointClient(t)}
	if _, err := c.CreateVolume(ctx, &csi.CreateVolumeRequest{Name: "free", VolumeCapabilities: []*csi.VolumeCapability{capability(false)}}); err != nil {
		t.Fatal(err)
	}
	res, err := c.ControllerExpandVolume(ctx, &csi.ControllerExpandVolumeRequest{
		VolumeId: "free", CapacityRange: &csi.CapacityRange{RequiredBytes: 5 << 30, LimitBytes: 6 << 30},
	})
	if err != nil || res.GetCapacityBytes() != 5<<30 {
		t.Fatalf("%+v %v", res, err)
	}
}

// A content source with an empty id is InvalidArgument, and nothing is
// provisioned: the endpoint reads an empty oneof id back as "no source", so
// a restore or clone would create an empty volume and report success.
func TestCreateEmptyContentSourceID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ep := endpointClient(t)
	c := &zcsi.Controller{EP: ep}
	for name, src := range map[string]*csi.VolumeContentSource{
		"snapshot": {Type: &csi.VolumeContentSource_Snapshot{Snapshot: &csi.VolumeContentSource_SnapshotSource{}}},
		"volume":   {Type: &csi.VolumeContentSource_Volume{Volume: &csi.VolumeContentSource_VolumeSource{}}},
	} {
		_, err := c.CreateVolume(ctx, &csi.CreateVolumeRequest{
			Name: "empty-" + name, VolumeCapabilities: []*csi.VolumeCapability{capability(false)},
			CapacityRange: &csi.CapacityRange{RequiredBytes: 1 << 20}, VolumeContentSource: src,
		})
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: %v, want InvalidArgument", name, err)
		}
		if _, err := ep.GetVolume(ctx, &pb.GetVolumeRequest{VolumeId: "empty-" + name}); status.Code(err) != codes.NotFound {
			t.Errorf("%s: a volume was provisioned: %v", name, err)
		}
	}
}
