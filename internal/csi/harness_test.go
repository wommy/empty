package csi_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/wommy/empty/gen/zfsendpoint/v1"
	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/server"
	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

// endpointClient serves an endpoint over bufconn, as internal/server's tests
// do. It runs on the simulator, or on a fresh parent dataset of a real pool
// when ZFS_TEST_POOL is set (CI: a file-backed pool, as root).
func endpointClient(t *testing.T) pb.EndpointServiceClient {
	t.Helper()
	return pb.NewEndpointServiceClient(endpointConn(t))
}

// endpointConn is endpointClient's connection, which also serves the gRPC
// health service, SERVING, as the daemon does once ready.
func endpointConn(t *testing.T) *grpc.ClientConn {
	t.Helper()
	ctx := context.Background()
	var z *zfs.Client
	var parent string
	var settle func(context.Context) error
	if pool := os.Getenv("ZFS_TEST_POOL"); pool != "" {
		z = zfs.New(zfs.LocalRunner{})
		parent = fmt.Sprintf("%s/csi-%d", pool, time.Now().UnixNano())
		// as cmd/zfs-endpoint: csi-sanity deletes a zvol right after
		// creating it, while udev may still be probing the new device
		settle = endpoint.UdevSettle
		t.Cleanup(func() {
			if err := z.Destroy(ctx, parent, zfs.DestroyOptions{Recursive: true}); err != nil {
				t.Errorf("cleanup %s: %v", parent, err)
			}
		})
	} else if os.Getenv("ZFS_REQUIRE_REAL") != "" {
		t.Fatal("ZFS_REQUIRE_REAL is set but ZFS_TEST_POOL is not: refusing the simulator fallback")
	} else {
		z = zfs.New(zfstest.New("tank"))
		parent = "tank/vols"
	}
	if err := z.CreateFilesystem(ctx, parent, nil, true); err != nil {
		t.Fatal(err)
	}
	ep, err := endpoint.New(z, endpoint.Config{Parent: parent, Settle: settle})
	if err != nil {
		t.Fatal(err)
	}
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pb.RegisterEndpointServiceServer(srv, &server.Server{EP: ep, Parent: parent})
	hs := health.NewServer()
	hs.SetServingStatus(server.ServiceName, healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(srv, hs)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}
