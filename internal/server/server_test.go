package server_test

import (
	"context"
	"net"
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

func client(t *testing.T) pb.EndpointServiceClient {
	t.Helper()
	ctx := context.Background()
	z := zfs.New(zfstest.New("tank"))
	if err := z.CreateFilesystem(ctx, "tank/vols", nil, false); err != nil {
		t.Fatal(err)
	}
	ep, err := endpoint.New(z, endpoint.Config{Parent: "tank/vols"})
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
