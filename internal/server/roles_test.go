package server_test

import (
	"context"
	"net"
	"slices"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/wommy/empty/gen/zfsendpoint/v1"
	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/server"
	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

// csiMethods are the RPCs zfs-csi calls (internal/csi: c.EP.*), and so
// what the csi role serves.
var csiMethods = []string{
	"GetCapabilities", "CreateVolume", "DeleteVolume", "ExpandVolume", "GetVolume",
	"ListVolumes", "CreateSnapshot", "DeleteSnapshot", "ListSnapshots", "GetCapacity",
}

// F2: every method of the service has a role: csi is exactly what zfs-csi
// calls, admin is everything. A method added to the proto without a row
// fails here, and an unknown method is allowed to nobody.
func TestEveryMethodHasARole(t *testing.T) {
	t.Parallel()
	desc := pb.EndpointService_ServiceDesc
	if len(desc.Streams) != 0 {
		t.Errorf("streams %v: give each a role (the stream interceptor denies them)", desc.Streams)
	}
	for _, m := range desc.Methods {
		full := "/" + desc.ServiceName + "/" + m.MethodName
		if !server.Allowed(server.RoleAdmin, full) {
			t.Errorf("admin may not call %s", m.MethodName)
		}
		if got, want := server.Allowed(server.RoleCSI, full), slices.Contains(csiMethods, m.MethodName); got != want {
			t.Errorf("csi may call %s: %v, want %v", m.MethodName, got, want)
		}
	}
	for _, r := range []server.Role{server.RoleCSI, server.RoleAdmin, 0} {
		if server.Allowed(r, "/"+desc.ServiceName+"/Unknown") {
			t.Errorf("role %v may call an unknown method", r)
		}
	}
	if server.Allowed(0, "/"+desc.ServiceName+"/GetCapabilities") {
		t.Error("no role may call GetCapabilities")
	}
}

// F2 over the wire: one daemon, a csi listener and an admin listener. The
// csi socket refuses Reconcile and the export RPCs with PERMISSION_DENIED
// (a compromised CSI controller cannot mass-collect or export a volume to
// itself); the admin socket serves them; health is served on both; a
// listener served without a role serves nothing but health.
func TestRolesPerListener(t *testing.T) {
	t.Parallel()
	z := zfs.New(zfstest.New("tank"))
	if err := z.CreateFilesystem(context.Background(), "tank/vols", nil, false); err != nil {
		t.Fatal(err)
	}
	ep, err := endpoint.New(z, endpoint.Config{Parent: "tank/vols"})
	if err != nil {
		t.Fatal(err)
	}
	d := server.NewDaemon(&server.Server{EP: ep, Parent: "tank/vols"}, nil)
	d.SetServing(true)
	t.Cleanup(d.GRPC.Stop)
	dial := func(serve func(net.Listener) error) *grpc.ClientConn {
		lis := bufconn.Listen(1 << 20)
		go serve(lis)
		conn, err := grpc.NewClient("passthrough:///buf",
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
			grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		return conn
	}
	conns := map[string]*grpc.ClientConn{
		"csi":   dial(func(l net.Listener) error { return d.Serve(l, server.RoleCSI) }),
		"admin": dial(func(l net.Listener) error { return d.Serve(l, server.RoleAdmin) }),
		"none":  dial(d.GRPC.Serve),
	}
	ctx := context.Background()
	calls := map[string]func(pb.EndpointServiceClient) error{
		"GetCapabilities": func(c pb.EndpointServiceClient) error {
			_, e := c.GetCapabilities(ctx, &pb.GetCapabilitiesRequest{})
			return e
		},
		"CreateVolume": func(c pb.EndpointServiceClient) error {
			_, e := c.CreateVolume(ctx, &pb.CreateVolumeRequest{})
			return e
		},
		"DeleteVolume": func(c pb.EndpointServiceClient) error {
			_, e := c.DeleteVolume(ctx, &pb.DeleteVolumeRequest{})
			return e
		},
		"ExpandVolume": func(c pb.EndpointServiceClient) error {
			_, e := c.ExpandVolume(ctx, &pb.ExpandVolumeRequest{})
			return e
		},
		"GetVolume":   func(c pb.EndpointServiceClient) error { _, e := c.GetVolume(ctx, &pb.GetVolumeRequest{}); return e },
		"ListVolumes": func(c pb.EndpointServiceClient) error { _, e := c.ListVolumes(ctx, &pb.ListVolumesRequest{}); return e },
		"CreateSnapshot": func(c pb.EndpointServiceClient) error {
			_, e := c.CreateSnapshot(ctx, &pb.CreateSnapshotRequest{})
			return e
		},
		"DeleteSnapshot": func(c pb.EndpointServiceClient) error {
			_, e := c.DeleteSnapshot(ctx, &pb.DeleteSnapshotRequest{})
			return e
		},
		"ListSnapshots": func(c pb.EndpointServiceClient) error {
			_, e := c.ListSnapshots(ctx, &pb.ListSnapshotsRequest{})
			return e
		},
		"GetCapacity": func(c pb.EndpointServiceClient) error { _, e := c.GetCapacity(ctx, &pb.GetCapacityRequest{}); return e },
		"Reconcile": func(c pb.EndpointServiceClient) error {
			_, e := c.Reconcile(ctx, &pb.ReconcileRequest{DryRun: true})
			return e
		},
		"PublishFileExport": func(c pb.EndpointServiceClient) error {
			_, e := c.PublishFileExport(ctx, &pb.PublishFileExportRequest{})
			return e
		},
		"UnpublishFileExport": func(c pb.EndpointServiceClient) error {
			_, e := c.UnpublishFileExport(ctx, &pb.UnpublishFileExportRequest{})
			return e
		},
	}
	for _, m := range pb.EndpointService_ServiceDesc.Methods {
		if calls[m.MethodName] == nil {
			t.Errorf("no call for %s", m.MethodName)
		}
	}
	for name, conn := range conns {
		c := pb.NewEndpointServiceClient(conn)
		for m, call := range calls {
			code := status.Code(call(c))
			var want codes.Code
			switch {
			case name == "none":
				want = codes.Unauthenticated
			case name == "csi" && !slices.Contains(csiMethods, m):
				want = codes.PermissionDenied
			}
			if (want == 0 && (code == codes.PermissionDenied || code == codes.Unauthenticated)) || (want != 0 && code != want) {
				t.Errorf("%s listener, %s: %v, want %v", name, m, code, map[bool]string{true: "allowed", false: want.String()}[want == 0])
			}
		}
		h, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{Service: server.ServiceName})
		if err != nil || h.Status != healthpb.HealthCheckResponse_SERVING {
			t.Errorf("%s listener: health %v %v", name, h, err)
		}
	}
}
