package deploy

import (
	"context"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
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
// on the simulator: what the shipped unit runs with the NFS drop-in.
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

// The shipped StorageClass is for NFS mode (ADR 0009), and every value an
// operator must choose is a placeholder the controller refuses, naming
// it: a PVC fails fast with ProvisioningFailed until each is written.
//   - nfs.clients: a plausible CIDR would parse and export to nobody (the
//     pod hangs at mount).
//   - nfs.uid, nfs.gid, nfs.mode: left out, the volume root is root:root
//     0755, and under root_squash with fsGroupPolicy None no pod, root
//     included, can write it (the pod starts, every write is EACCES).
//
// Each placeholder alone is refused for its value, and with all four
// replaced the same class provisions.
func TestStorageClassPlaceholders(t *testing.T) {
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
	b, err := os.ReadFile(filepath.Join("k8s", "classes.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	header, _, _ := strings.Cut(string(b), "\napiVersion:")
	// a value each parses to, as an operator would write it
	values := map[string]string{
		zcsi.ParamNFSClients: "10.20.0.0/24",
		zcsi.ParamNFSUID:     "1000",
		zcsi.ParamNFSGID:     "2000",
		zcsi.ParamNFSMode:    "2775",
	}
	for k := range values {
		placeholder, ok := params[k]
		if !ok || !strings.HasPrefix(placeholder, "<") {
			t.Fatalf("StorageClass %s=%q: want a <placeholder>", k, placeholder)
		}
		if !strings.Contains(header, placeholder) {
			t.Errorf("classes.yaml's comments do not tell the operator to replace %s", placeholder)
		}
	}
	if !strings.Contains(header, "replace "+params[zcsi.ParamNFSClients]) {
		t.Errorf("classes.yaml does not tell the operator to replace %s", params[zcsi.ParamNFSClients])
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
	if err := create("pvc-shipped", params); status.Code(err) != codes.InvalidArgument {
		t.Errorf("the shipped class: %v, want InvalidArgument", err)
	}
	for k := range values {
		p := maps.Clone(params)
		maps.Copy(p, values)
		p[k] = params[k]
		err := create("pvc-"+strings.ReplaceAll(k, ".", "-"), p)
		if status.Code(err) != codes.InvalidArgument || !strings.Contains(status.Convert(err).Message(), params[k]) {
			t.Errorf("the class with only %s left as shipped: %v, want InvalidArgument naming %q", k, err, params[k])
		}
	}
	set := maps.Clone(params)
	maps.Copy(set, values)
	if err := create("pvc-set", set); err != nil {
		t.Errorf("the class with every placeholder replaced: %v", err)
	}
}

// The shipped class mounts NFSv4.2 only (no v3 fallback, so no rpcbind or
// mountd to expose), hard, and nosuid and nodev: a setuid binary or device
// node on a volume grants nothing in a pod (ADR 0009). The real-ZFS VM
// checks that its nfsd serves 4.2 (ci/real-zfs-vm.sh).
func TestStorageClassMountOptions(t *testing.T) {
	t.Parallel()
	sc := object(t, k8sObjects(t), "StorageClass", className)
	got := strs(get(sc, "mountOptions"))
	if want := []string{"nfsvers=4.2", "hard", "nosuid", "nodev"}; !slices.Equal(got, want) {
		t.Errorf("StorageClass %s mountOptions %q, want %q", className, got, want)
	}
}
