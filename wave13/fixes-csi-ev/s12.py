import os
os.chdir('/home/user/empty/.claude/worktrees/agent-a131fcf70b9f3c4be')

def edit(p, pairs):
    s = open(p).read()
    for a, b in pairs:
        assert a in s, (p, a)
        s = s.replace(a, b, 1)
    open(p, 'w').write(s)

edit('internal/csi/harness_test.go', [
('''func endpointClient(t *testing.T) pb.EndpointServiceClient {
	t.Helper()''', '''func endpointClient(t *testing.T) pb.EndpointServiceClient {
	t.Helper()
	return pb.NewEndpointServiceClient(endpointConn(t))
}

// endpointConn is endpointClient's connection, which also serves the gRPC
// health service, SERVING, as the daemon does once ready.
func endpointConn(t *testing.T) *grpc.ClientConn {
	t.Helper()'''),
('''	pb.RegisterEndpointServiceServer(srv, &server.Server{EP: ep, Parent: parent})
''', '''	pb.RegisterEndpointServiceServer(srv, &server.Server{EP: ep, Parent: parent})
	hs := health.NewServer()
	hs.SetServingStatus(server.ServiceName, healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(srv, hs)
'''),
('''	t.Cleanup(func() { conn.Close() })
	return pb.NewEndpointServiceClient(conn)
}''', '''	t.Cleanup(func() { conn.Close() })
	return conn
}'''),
('''	"google.golang.org/grpc/credentials/insecure"
''', '''	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
'''),
])
edit('internal/csi/sanity_test.go', [
('''	ep := endpointClient(t)
''', '''	conn := endpointConn(t)
	ep := pb.NewEndpointServiceClient(conn)
'''),
('&zcsi.Identity{EP: ep}', '&zcsi.Identity{Health: healthpb.NewHealthClient(conn)}'),
('''	"google.golang.org/grpc/codes"
''', '''	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
'''),
('''	zcsi "github.com/wommy/empty/internal/csi"
''', '''	pb "github.com/wommy/empty/gen/zfsendpoint/v1"
	zcsi "github.com/wommy/empty/internal/csi"
'''),
])
