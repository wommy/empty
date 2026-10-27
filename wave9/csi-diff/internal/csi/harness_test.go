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
	ctx := context.Background()
	var z *zfs.Client
	var parent string
	if pool := os.Getenv("ZFS_TEST_POOL"); pool != "" {
		z = zfs.New(zfs.LocalRunner{})
		parent = fmt.Sprintf("%s/csi-%d", pool, time.Now().UnixNano())
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
	ep, err := endpoint.New(z, endpoint.Config{Parent: parent})
	if err != nil {
		t.Fatal(err)
	}
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pb.RegisterEndpointServiceServer(srv, &server.Server{EP: ep, Parent: parent})
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
