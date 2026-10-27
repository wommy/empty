package deploy

import (
	"context"
	"maps"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/wommy/empty/gen/zfsendpoint/v1"
	zcsi "github.com/wommy/empty/internal/csi"
	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/server"
	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

// nfsController is zfs-csi over an endpoint in NFS mode (one -nfs-server)
// on the simulator: what the shipped unit runs with the nfs.conf drop-in.
func nfsController(t *testing.T) *zcsi.Controller {
	t.Helper()
	ctx := context.Background()
	sim := zfstest.New("tank")
	z := zfs.New(sim)
	const parent = "tank/k8s/vols"
	if err := z.CreateFilesystem(ctx, parent, nil, true); err != nil {
		t.Fatal(err)
	}
	ep, err := endpoint.New(z, endpoint.Config{Parent: parent, FileExport: true, Etab: sim, NFSServers: []string{"nfs.example.com"}})
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
	return &zcsi.Controller{EP: pb.NewEndpointServiceClient(conn)}
}

// The shipped StorageClass is for NFS mode (ADR 0009) and its nfs.clients
// is a placeholder the controller refuses, naming the value: a PVC fails
// fast with ProvisioningFailed until the operator writes the node network.
// A plausible CIDR would parse and export to nobody (the pod hangs at
// mount). With a real network in its place the same class provisions.
func TestStorageClassClientsPlaceholder(t *testing.T) {
	t.Parallel()
	sc := object(t, k8sObjects(t), "StorageClass", className)
	params := map[string]string{}
	for k, v := range get(sc, "parameters").(map[string]any) {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("parameter %s=%v is not a string", k, v)
		}
		params[k] = s
	}
	placeholder, ok := params[zcsi.ParamNFSClients]
	if !ok || !strings.Contains(placeholder, "<") {
		t.Fatalf("StorageClass %s=%q: want a <placeholder>", zcsi.ParamNFSClients, placeholder)
	}
	b, err := os.ReadFile(filepath.Join("k8s", "classes.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "replace "+placeholder) {
		t.Errorf("classes.yaml does not tell the operator to replace %s", placeholder)
	}

	ctl := nfsController(t)
	create := func(name string, p map[string]string) error {
		_, err := ctl.CreateVolume(context.Background(), &csi.CreateVolumeRequest{
			Name: name, Parameters: p, CapacityRange: &csi.CapacityRange{RequiredBytes: 1 << 30},
			VolumeCapabilities: []*csi.VolumeCapability{{
				AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER},
				AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
			}},
		})
		return err
	}
	err = create("pvc-shipped", params)
	if status.Code(err) != codes.InvalidArgument || !strings.Contains(status.Convert(err).Message(), placeholder) {
		t.Errorf("the shipped class: %v, want InvalidArgument naming %q", err, placeholder)
	}
	set := maps.Clone(params)
	set[zcsi.ParamNFSClients] = "10.20.0.0/24"
	if err := create("pvc-set", set); err != nil {
		t.Errorf("the class with nfs.clients set: %v", err)
	}
}
