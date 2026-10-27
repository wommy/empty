package server_test

import (
	"context"
	"net"
	"slices"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/wommy/empty/gen/zfsendpoint/v1"
	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/server"
	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

func client(t *testing.T) pb.EndpointServiceClient { return clientWith(t, false) }

func clientWith(t *testing.T, fileExport bool) pb.EndpointServiceClient {
	t.Helper()
	ctx := context.Background()
	sim := zfstest.New("tank")
	z := zfs.New(sim)
	if err := z.CreateFilesystem(ctx, "tank/vols", nil, false); err != nil {
		t.Fatal(err)
	}
	ep, err := endpoint.New(z, endpoint.Config{Parent: "tank/vols", FileExport: fileExport, Etab: sim})
	if err != nil {
		t.Fatal(err)
	}
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pb.RegisterEndpointServiceServer(srv, &server.Server{EP: ep, Parent: "tank/vols"})
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return pb.NewEndpointServiceClient(conn)
}

func TestRoundTripAndCodes(t *testing.T) {
	ctx := context.Background()
	c := client(t)

	caps, err := c.GetCapabilities(ctx, &pb.GetCapabilitiesRequest{})
	if err != nil || caps.Parent != "tank/vols" || len(caps.Capabilities) == 0 {
		t.Fatalf("caps %v %v", caps, err)
	}
	req := &pb.CreateVolumeRequest{Name: "pvc-a", Kind: pb.VolumeKind_VOLUME_KIND_BLOCK, CapacityBytes: 1 << 30}
	v, err := c.CreateVolume(ctx, req)
	if err != nil || v.Volume.Id != "pvc-a" || v.Volume.DevicePath != "/dev/zvol/tank/vols/pvc-a" {
		t.Fatalf("create %v %v", v, err)
	}
	if _, err := c.CreateVolume(ctx, req); err != nil {
		t.Fatalf("idempotent create: %v", err)
	}
	req.CapacityBytes = 2 << 30
	if _, err := c.CreateVolume(ctx, req); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("incompatible create: %v", err)
	}
	if !slices.Contains(caps.Capabilities, pb.Capability_CAPABILITY_PROPERTIES) {
		t.Fatalf("caps %v lack PROPERTIES", caps.Capabilities)
	}
	preq := &pb.CreateVolumeRequest{Name: "pvc-p", CapacityBytes: 1 << 20,
		Properties: map[string]string{"compression": "zstd", "recordsize": "16K"}}
	if _, err := c.CreateVolume(ctx, preq); err != nil {
		t.Fatalf("create with properties: %v", err)
	}
	if _, err := c.CreateVolume(ctx, preq); err != nil {
		t.Fatalf("idempotent create with properties: %v", err)
	}
	preq.Properties["compression"] = "lz4"
	if _, err := c.CreateVolume(ctx, preq); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("property mismatch: %v", err)
	}
	if _, err := c.CreateVolume(ctx, &pb.CreateVolumeRequest{Name: "pvc-m",
		Properties: map[string]string{"mountpoint": "/etc"}}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("denied property: %v", err)
	}
	if _, err := c.CreateSnapshot(ctx, &pb.CreateSnapshotRequest{Name: "s1", SourceVolumeId: "pvc-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DeleteVolume(ctx, &pb.DeleteVolumeRequest{VolumeId: "pvc-a"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("delete with snapshot: %v", err)
	}
	if _, err := c.DeleteVolume(ctx, &pb.DeleteVolumeRequest{VolumeId: "missing"}); err != nil {
		t.Fatalf("delete missing: %v", err)
	}
	if _, err := c.ListVolumes(ctx, &pb.ListVolumesRequest{MaxEntries: -1}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("negative max_entries: %v", err)
	}
	if _, err := c.ListVolumes(ctx, &pb.ListVolumesRequest{StartingToken: "nope"}); status.Code(err) != codes.Aborted {
		t.Fatalf("bad token: %v", err)
	}
	if _, err := c.GetVolume(ctx, &pb.GetVolumeRequest{VolumeId: "nope"}); status.Code(err) != codes.NotFound {
		t.Fatalf("get missing: %v", err)
	}
}

func TestReconcile(t *testing.T) {
	ctx := context.Background()
	c := client(t)
	caps, err := c.GetCapabilities(ctx, &pb.GetCapabilitiesRequest{})
	if err != nil || !slices.Contains(caps.Capabilities, pb.Capability_CAPABILITY_RECONCILE) {
		t.Fatalf("caps %v %v", caps, err)
	}
	if _, err := c.CreateVolume(ctx, &pb.CreateVolumeRequest{Name: "pvc-a", CapacityBytes: 1 << 30}); err != nil {
		t.Fatal(err)
	}
	// just created: too young under the default minimum age
	if _, err := c.CreateVolume(ctx, &pb.CreateVolumeRequest{Name: "pvc-c", CapacityBytes: 1 << 30}); err != nil {
		t.Fatal(err)
	}
	r, err := c.Reconcile(ctx, &pb.ReconcileRequest{DesiredVolumeIds: []string{"pvc-b", "pvc-c"}, DryRun: true})
	if err != nil || len(r.Actions) != 0 || len(r.Skipped) != 2 ||
		r.Skipped[0].Target != "pvc-b" || r.Skipped[0].Reason != endpoint.SkipDesiredMissing ||
		r.Skipped[1].Target != "pvc-a" || r.Skipped[1].Reason != endpoint.SkipTooYoung {
		t.Fatalf("reconcile %v %v", r, err)
	}
	if r.PlanDigest == "" {
		t.Fatal("dry run without a plan digest")
	}
	if _, err := c.Reconcile(ctx, &pb.ReconcileRequest{MaxDeletions: 1, UnlimitedDeletions: true}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("both caps: %v", err)
	}
	if _, err := c.Reconcile(ctx, &pb.ReconcileRequest{MinAgeSeconds: 60}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("min age below the floor: %v", err)
	}
	// an id the endpoint never issues names nothing and is skipped; an
	// empty one is malformed
	if _, err := c.Reconcile(ctx, &pb.ReconcileRequest{DesiredVolumeIds: []string{"a/b"}, DryRun: true}); err != nil {
		t.Fatalf("never-issued id: %v", err)
	}
	if _, err := c.Reconcile(ctx, &pb.ReconcileRequest{DesiredVolumeIds: []string{""}}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty id: %v", err)
	}
	if _, err := c.Reconcile(ctx, &pb.ReconcileRequest{MinAgeSeconds: 1 << 62}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("huge age: %v", err)
	}
}

func TestFileExport(t *testing.T) {
	ctx := context.Background()

	// not root: not advertised, and the calls are UNIMPLEMENTED
	off := client(t)
	caps, err := off.GetCapabilities(ctx, &pb.GetCapabilitiesRequest{})
	if err != nil || slices.Contains(caps.Capabilities, pb.Capability_CAPABILITY_FILE_EXPORT) {
		t.Fatalf("caps %v %v", caps, err)
	}
	if _, err := off.PublishFileExport(ctx, &pb.PublishFileExportRequest{VolumeId: "v", Client: "10.0.0.1"}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("publish without capability: %v", err)
	}

	c := clientWith(t, true)
	caps, err = c.GetCapabilities(ctx, &pb.GetCapabilitiesRequest{})
	if err != nil || !slices.Contains(caps.Capabilities, pb.Capability_CAPABILITY_FILE_EXPORT) {
		t.Fatalf("caps %v %v", caps, err)
	}
	if _, err := c.CreateVolume(ctx, &pb.CreateVolumeRequest{Name: "fs"}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		r, err := c.PublishFileExport(ctx, &pb.PublishFileExportRequest{VolumeId: "fs", Client: "10.0.0.1", ReadOnly: true})
		if err != nil || r.ExportPath != "/tank/vols/fs" {
			t.Fatalf("publish %v %v", r, err)
		}
	}
	if _, err := c.PublishFileExport(ctx, &pb.PublishFileExportRequest{VolumeId: "fs", Client: "10.0.0.1"}); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("other mode: %v", err)
	}
	if _, err := c.PublishFileExport(ctx, &pb.PublishFileExportRequest{VolumeId: "fs", Client: "*"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("wildcard: %v", err)
	}
	if _, err := c.UnpublishFileExport(ctx, &pb.UnpublishFileExportRequest{VolumeId: "fs", Client: "10.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.UnpublishFileExport(ctx, &pb.UnpublishFileExportRequest{VolumeId: "gone", Client: "10.0.0.1"}); err != nil {
		t.Fatalf("unpublish missing: %v", err)
	}
}
