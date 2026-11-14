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
	assertCapabilities(t, &zcsi.Controller{EP: endpointClient(t)})
}

// The sidecars read the controller capabilities once, at startup, and exit
// on an error (klog.Fatalf in csi-provisioner, csi-snapshotter and
// csi-resizer). The set does not depend on the endpoint, so a sidecar
// started while the endpoint is down (a restart, a crash backoff) gets it
// instead of crash-looping.
func TestControllerCapabilitiesWithoutEndpoint(t *testing.T) {
	t.Parallel()
	assertCapabilities(t, &zcsi.Controller{EP: pb.NewEndpointServiceClient(unreachableConn(t))})
}

// assertCapabilities asserts the controller capabilities: every RPC the
// endpoint serves.
func assertCapabilities(t *testing.T, c *zcsi.Controller) {
	t.Helper()
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
	c := &zcsi.Controller{EP: endpointClient(t), EnableBlock: true, EnableLocalFilesystem: true}
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
	c := &zcsi.Controller{EP: ep, EnableBlock: true, EnableLocalFilesystem: true}
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
	off := &zcsi.Controller{EP: ep, EnableLocalFilesystem: true}
	on := &zcsi.Controller{EP: ep, EnableBlock: true, EnableLocalFilesystem: true}
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
	c := &zcsi.Controller{EP: endpointClient(t), EnableLocalFilesystem: true}
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

// A volume_capability on Expand is checked for its access type only, before
// anything is resized: none, or block access on a filesystem, is
// InvalidArgument and the capacity is unchanged.
func TestExpandValidatesCapability(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ep := endpointClient(t)
	c := &zcsi.Controller{EP: ep, EnableBlock: true, EnableLocalFilesystem: true}
	if _, err := c.CreateVolume(ctx, &csi.CreateVolumeRequest{Name: "fs", VolumeCapabilities: []*csi.VolumeCapability{capability(false)},
		CapacityRange: &csi.CapacityRange{RequiredBytes: 1 << 30}}); err != nil {
		t.Fatal(err)
	}
	for name, vc := range map[string]*csi.VolumeCapability{
		"block access on a filesystem volume": capability(true),
		"no access type":                      {AccessMode: capability(false).AccessMode},
	} {
		res, err := c.ControllerExpandVolume(ctx, &csi.ControllerExpandVolumeRequest{
			VolumeId: "fs", CapacityRange: &csi.CapacityRange{RequiredBytes: 2 << 30}, VolumeCapability: vc,
		})
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: %+v %v, want InvalidArgument", name, res, err)
		}
	}
	if v, err := ep.GetVolume(ctx, &pb.GetVolumeRequest{VolumeId: "fs"}); err != nil || v.GetVolume().GetCapacityBytes() != 1<<30 {
		t.Fatalf("resized by a refused expand: %v %v", v, err)
	}
	// the access mode is not the expand's business (CSI: the capability
	// tells block from mount): a mode this mode would not create still
	// expands, as does a supported one; a missing volume is NotFound
	multi := capability(false)
	multi.AccessMode.Mode = csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER
	for i, vc := range []*csi.VolumeCapability{multi, capability(false)} {
		want := int64(2+i) << 30
		res, err := c.ControllerExpandVolume(ctx, &csi.ControllerExpandVolumeRequest{
			VolumeId: "fs", CapacityRange: &csi.CapacityRange{RequiredBytes: want}, VolumeCapability: vc,
		})
		if err != nil || res.GetCapacityBytes() != want {
			t.Fatalf("expand with %v: %+v %v", vc.GetAccessMode().GetMode(), res, err)
		}
	}
	if _, err := c.ControllerExpandVolume(ctx, &csi.ControllerExpandVolumeRequest{
		VolumeId: "gone", CapacityRange: &csi.CapacityRange{RequiredBytes: 2 << 30}, VolumeCapability: capability(false),
	}); status.Code(err) != codes.NotFound {
		t.Fatalf("missing volume: %v", err)
	}
}

// A zvol made while block was enabled still grows with block access after
// -enable-block is turned off: the flag gates new volumes and confirmations,
// and an expand refused for it is final for csi-resizer.
func TestExpandBlockAfterBlockDisabled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ep := endpointClient(t)
	on := &zcsi.Controller{EP: ep, EnableBlock: true}
	if _, err := on.CreateVolume(ctx, &csi.CreateVolumeRequest{Name: "zvol", VolumeCapabilities: []*csi.VolumeCapability{capability(true)},
		CapacityRange: &csi.CapacityRange{RequiredBytes: 1 << 30}}); err != nil {
		t.Fatal(err)
	}
	off := &zcsi.Controller{EP: ep}
	res, err := off.ControllerExpandVolume(ctx, &csi.ControllerExpandVolumeRequest{
		VolumeId: "zvol", CapacityRange: &csi.CapacityRange{RequiredBytes: 2 << 30}, VolumeCapability: capability(true),
	})
	if err != nil || res.GetCapacityBytes() != 2<<30 || res.GetNodeExpansionRequired() {
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
	c := &zcsi.Controller{EP: ep, EnableLocalFilesystem: true}
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

// Validate confirms only the volume_context CreateVolume issued for the
// volume (or a subset of it, plus keys Kubernetes adds itself): another
// volume's dataset, another kind or an unknown key is not confirmed.
// Parameters are not compared with the volume (deferred: the endpoint API
// carries no sparse/reserve), so that half is not asserted here.
func TestValidateComparesIssuedContext(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := &zcsi.Controller{EP: endpointClient(t), EnableLocalFilesystem: true}
	res, err := c.CreateVolume(ctx, &csi.CreateVolumeRequest{Name: "v", VolumeCapabilities: []*csi.VolumeCapability{capability(false)},
		CapacityRange: &csi.CapacityRange{RequiredBytes: 1 << 30}})
	if err != nil {
		t.Fatal(err)
	}
	issued := res.GetVolume().GetVolumeContext()
	with := func(k, v string) map[string]string {
		m := map[string]string{}
		for ik, iv := range issued {
			m[ik] = iv
		}
		m[k] = v
		return m
	}
	for _, tc := range []struct {
		name    string
		vctx    map[string]string
		confirm bool
	}{
		{"issued", issued, true},
		{"none", nil, true},
		{"subset", map[string]string{zcsi.CtxKind: "filesystem"}, true},
		{"provisioner identity", with("storage.kubernetes.io/csiProvisionerIdentity", "123-zfs"), true},
		{"other dataset", with(zcsi.CtxDataset, "tank/other/vol"), false},
		{"other kind", with(zcsi.CtxKind, "block"), false},
		{"unknown key", with("junk", "1"), false},
	} {
		got, err := c.ValidateVolumeCapabilities(ctx, &csi.ValidateVolumeCapabilitiesRequest{
			VolumeId: "v", VolumeCapabilities: []*csi.VolumeCapability{capability(false)}, VolumeContext: tc.vctx,
		})
		if err != nil {
			t.Fatal(err)
		}
		if (got.GetConfirmed() != nil) != tc.confirm {
			t.Errorf("%s: confirmed=%v (%q), want %v", tc.name, got.GetConfirmed() != nil, got.GetMessage(), tc.confirm)
		}
	}
}

// Without NFS no node plugin can mount a filesystem volume (ADR 0009):
// off (the default), CreateVolume refuses mount access with
// InvalidArgument naming both ways out and creates nothing, and
// ValidateVolumeCapabilities never confirms mount access, not even on a
// volume made with the flag on (nor on a zvol). An existing volume still
// expands and deletes. On, mount access is admitted as before.
func TestMountRefusedWithoutNFS(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ep := endpointClient(t)
	off := &zcsi.Controller{EP: ep, EnableBlock: true}
	on := &zcsi.Controller{EP: ep, EnableBlock: true, EnableLocalFilesystem: true}
	create := func(c *zcsi.Controller, name string, vc *csi.VolumeCapability) error {
		_, err := c.CreateVolume(ctx, &csi.CreateVolumeRequest{
			Name: name, VolumeCapabilities: []*csi.VolumeCapability{vc}, CapacityRange: &csi.CapacityRange{RequiredBytes: 1 << 30},
		})
		return err
	}
	validate := func(c *zcsi.Controller, vol string) (bool, string) {
		res, err := c.ValidateVolumeCapabilities(ctx, &csi.ValidateVolumeCapabilitiesRequest{VolumeId: vol, VolumeCapabilities: []*csi.VolumeCapability{capability(false)}})
		if err != nil {
			t.Fatal(err)
		}
		return res.GetConfirmed() != nil, res.GetMessage()
	}

	err := create(off, "fs", capability(false))
	if msg := status.Convert(err).Message(); status.Code(err) != codes.InvalidArgument ||
		!strings.Contains(msg, "-nfs-server") || !strings.Contains(msg, "-enable-local-filesystem") {
		t.Fatalf("mount without NFS, local filesystems off: %v", err)
	}
	if _, err := ep.GetVolume(ctx, &pb.GetVolumeRequest{VolumeId: "fs"}); status.Code(err) != codes.NotFound {
		t.Fatalf("refused mount volume exists: %v", err)
	}
	// block access is not mount access
	if err := create(off, "zvol", capability(true)); err != nil {
		t.Fatalf("block with local filesystems off: %v", err)
	}
	if err := create(on, "fs", capability(false)); err != nil {
		t.Fatalf("mount with local filesystems on: %v", err)
	}
	for _, vol := range []string{"fs", "zvol"} {
		if ok, msg := validate(off, vol); ok || !strings.Contains(msg, "-enable-local-filesystem") {
			t.Errorf("%s: mount confirmed with local filesystems off: %q", vol, msg)
		}
		if ok, msg := validate(on, vol); !ok {
			t.Errorf("%s: mount not confirmed with local filesystems on: %q", vol, msg)
		}
	}
	// the flag gates new volumes and confirmations, not an existing one
	res, err := off.ControllerExpandVolume(ctx, &csi.ControllerExpandVolumeRequest{
		VolumeId: "fs", CapacityRange: &csi.CapacityRange{RequiredBytes: 2 << 30}, VolumeCapability: capability(false),
	})
	if err != nil || res.GetCapacityBytes() != 2<<30 {
		t.Fatalf("expand with local filesystems off: %+v %v", res, err)
	}
	if _, err := off.DeleteVolume(ctx, &csi.DeleteVolumeRequest{VolumeId: "fs"}); err != nil {
		t.Fatalf("delete with local filesystems off: %v", err)
	}
}
