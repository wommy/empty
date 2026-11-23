package csi_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
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
	return endpointConnMode(t, false)
}

// sanityNFSServer is the one server an NFS-mode endpointConnMode advertises
// (-nfs-server); nothing mounts from it.
const sanityNFSServer = "nfs.example.com"

// endpointConnMode is endpointConn, in NFS mode (static export at create,
// ADR 0004) when nfs is set: FileExport with one advertised server, and
// the simulator's export table, or the kernel's (/var/lib/nfs/etab) on a
// real pool, which then needs root and a running nfs-server.
func endpointConnMode(t *testing.T, nfs bool) *grpc.ClientConn {
	t.Helper()
	ep, parent := newEndpoint(t, nfs)
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

// daemonConn is endpointConn served as zfs-endpoint serves it: by
// server.Daemon (request ids, one "rpc" audit line per call on log) on a
// unix socket with the CSI role.
func daemonConn(t *testing.T, log *slog.Logger) *grpc.ClientConn {
	t.Helper()
	ep, parent := newEndpoint(t, false)
	d := server.NewDaemon(&server.Server{EP: ep, Parent: parent}, log)
	d.SetServing(true)
	// unix socket paths are limited to ~108 bytes; t.TempDir can exceed that
	dir, err := os.MkdirTemp("", "csi")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "e.sock")
	lis, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	go d.Serve(lis, server.RoleCSI)
	t.Cleanup(d.GRPC.Stop)
	conn, err := grpc.NewClient("unix://"+sock, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// newEndpoint is the endpoint behind endpointConnMode, on the simulator
// or a fresh parent of ZFS_TEST_POOL, and its parent dataset.
func newEndpoint(t *testing.T, nfs bool) (*endpoint.Endpoint, string) {
	t.Helper()
	ctx := context.Background()
	var cfg endpoint.Config
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
			o := zfs.DestroyOptions{Recursive: true}
			err := z.Destroy(ctx, parent, o)
			// busy may be on any line of a recursive destroy's output
			for i := 0; i < 3 && err != nil && (errors.Is(err, zfs.ErrBusy) || strings.Contains(err.Error(), "dataset is busy")); i++ {
				if settle(ctx) != nil {
					break
				}
				err = z.Destroy(ctx, parent, o)
			}
			if err != nil {
				t.Errorf("cleanup %s: %v", parent, err)
			}
		})
	} else if os.Getenv("ZFS_REQUIRE_REAL") != "" {
		t.Fatal("ZFS_REQUIRE_REAL is set but ZFS_TEST_POOL is not: refusing the simulator fallback")
	} else {
		sim := zfstest.New("tank")
		z = zfs.New(sim)
		parent = "tank/vols"
		if nfs {
			cfg.Etab = sim
		}
	}
	if err := z.CreateFilesystem(ctx, parent, nil, true); err != nil {
		t.Fatal(err)
	}
	cfg.Parent, cfg.Settle = parent, settle
	if nfs {
		cfg.FileExport = true
		cfg.NFSServers = []string{sanityNFSServer}
	}
	ep, err := endpoint.New(z, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return ep, parent
}
