import sys
p = sys.argv[1] + '/internal/server/server_test.go'
s = open(p).read()
old = '''func clientWith(t *testing.T, fileExport bool) pb.EndpointServiceClient {
	t.Helper()
	ctx := context.Background()
	sim := zfstest.New("tank")
	z := zfs.New(sim)
	if err := z.CreateFilesystem(ctx, "tank/vols", nil, false); err != nil {
		t.Fatal(err)
	}
	ep, err := endpoint.New(z, endpoint.Config{Parent: "tank/vols", FileExport: fileExport, Etab: sim})
'''
new = '''func clientWith(t *testing.T, fileExport bool) pb.EndpointServiceClient {
	t.Helper()
	return clientConfig(t, endpoint.Config{FileExport: fileExport})
}

// clientConfig serves an endpoint with cfg on a simulated tank/vols.
func clientConfig(t *testing.T, cfg endpoint.Config) pb.EndpointServiceClient {
	t.Helper()
	ctx := context.Background()
	sim := zfstest.New("tank")
	z := zfs.New(sim)
	if err := z.CreateFilesystem(ctx, "tank/vols", nil, false); err != nil {
		t.Fatal(err)
	}
	cfg.Parent, cfg.Etab = "tank/vols", sim
	ep, err := endpoint.New(z, cfg)
'''
assert old in s
s = s.replace(old, new, 1)
s += '''
// wave13 Q1: the advertised servers, the export at create and the volume's
// recorded export cross the wire field for field.
func TestNFSExportAtCreate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := clientConfig(t, endpoint.Config{FileExport: true, NFSServers: []string{"192.0.2.10", "nfs.example.com"}})
	caps, err := c.GetCapabilities(ctx, &pb.GetCapabilitiesRequest{})
	if err != nil || !slices.Equal(caps.NfsServers, []string{"192.0.2.10", "nfs.example.com"}) {
		t.Fatalf("caps %v %v", caps, err)
	}
	r, err := c.CreateVolume(ctx, &pb.CreateVolumeRequest{Name: "fs", NfsExport: &pb.NFSExport{
		Clients: []string{"10.20.1.0/24", "10.20.0.0/24"}, ReadOnly: true, Server: "nfs.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	n := r.Volume.GetNfs()
	if n.GetServer() != "nfs.example.com" || !n.GetReadOnly() || !slices.Equal(n.GetClients(), []string{"10.20.0.0/24", "10.20.1.0/24"}) {
		t.Fatalf("volume export %v", n)
	}
	if _, err := c.CreateVolume(ctx, &pb.CreateVolumeRequest{Name: "fs", NfsExport: &pb.NFSExport{
		Clients: []string{"10.20.0.0/24", "10.20.1.0/24"}, ReadOnly: true, Server: "192.0.2.10"}}); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("other server: %v", err)
	}
	plain, err := c.CreateVolume(ctx, &pb.CreateVolumeRequest{Name: "plain"})
	if err != nil || plain.Volume.Nfs != nil {
		t.Fatalf("plain volume: %v %v", plain, err)
	}
	if caps, err := client(t).GetCapabilities(ctx, &pb.GetCapabilitiesRequest{}); err != nil || len(caps.NfsServers) > 0 {
		t.Fatalf("no servers: %v %v", caps, err)
	}
}
'''
open(p, 'w').write(s)
