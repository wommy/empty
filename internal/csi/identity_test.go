package csi_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/test/bufconn"

	zcsi "github.com/wommy/empty/internal/csi"
	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/server"
	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

// Probe follows the endpoint daemon's gRPC health: ready while it is
// SERVING, not ready (and no error) once it is NOT_SERVING, which the
// daemon's own probe sets when the pool is suspended or exported. Asking
// GetCapabilities, a constant, said ready whatever the pool's state.
func TestProbeFollowsEndpointHealth(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	z := zfs.New(zfstest.New("tank"))
	if err := z.CreateFilesystem(ctx, "tank/vols", nil, true); err != nil {
		t.Fatal(err)
	}
	ep, err := endpoint.New(z, endpoint.Config{Parent: "tank/vols"})
	if err != nil {
		t.Fatal(err)
	}
	d := server.NewDaemon(&server.Server{EP: ep, Parent: "tank/vols"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	lis := bufconn.Listen(1 << 20)
	go d.GRPC.Serve(lis)
	t.Cleanup(d.GRPC.Stop)
	conn, err := grpc.NewClient("passthrough:///b", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	id := &zcsi.Identity{Health: healthpb.NewHealthClient(conn)}
	for _, serving := range []bool{true, false, true} {
		d.SetServing(serving)
		res, err := id.Probe(ctx, &csi.ProbeRequest{})
		if err != nil || res.GetReady().GetValue() != serving {
			t.Errorf("endpoint serving=%v: Probe ready=%v err=%v", serving, res.GetReady().GetValue(), err)
		}
	}
}
