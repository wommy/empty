package csi_test

import (
	"context"
	"slices"
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
	ctx := context.Background()
	c := &zcsi.Controller{EP: endpointClient(t)}
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
		{"zvol", nil, false},              // no capability given
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
	ctx := context.Background()
	ep := endpointClient(t)
	c := &zcsi.Controller{EP: ep}
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
