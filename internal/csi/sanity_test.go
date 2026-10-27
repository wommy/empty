package csi_test

import (
	"context"
	"flag"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-csi/csi-test/v5/pkg/sanity"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	zcsi "github.com/wommy/empty/internal/csi"
)

// sanityFocus selects the Identity and Controller groups. csi-sanity
// registers the Node Service group unconditionally and fails every Node spec
// in its BeforeEach on a controller-only driver (adv-csi-frontend.md B2), so
// it is focused out, along with the GroupController groups this driver does
// not serve. Ginkgo matches focus against "<suite description> <spec
// text>", so the pattern must not be anchored with ^.
const sanityFocus = `Identity Service|\[Controller Server\]`

// cleanupNode answers the two Node calls csi-sanity's resource cleanup makes
// on every controller test's volumes (NodeUnpublishVolume, then
// NodeGetCapabilities to decide on NodeUnstageVolume). It is a test fixture
// only: nothing was ever published, so there is nothing to unpublish. No
// Node spec runs against it.
type cleanupNode struct{ csi.UnimplementedNodeServer }

func (cleanupNode) NodeUnpublishVolume(context.Context, *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	return nil, status.Error(codes.NotFound, "never published")
}

func (cleanupNode) NodeGetCapabilities(context.Context, *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	return &csi.NodeGetCapabilitiesResponse{}, nil
}

// TestSanity runs csi-test's sanity suite in-process against the front end,
// itself backed by an endpoint on the simulator (or a real pool with
// ZFS_TEST_POOL). The CSI side is a real unix socket, which is what
// csi-sanity dials.
func TestSanity(t *testing.T) {
	// Ginkgo refuses -count>1 for the whole binary ("only -count=1 is
	// allowed"), which made `go test -count=N ./internal/csi` fail outright
	// and hid the package's other tests from flake hunting.
	if f := flag.Lookup("test.count"); f != nil && f.Value.String() != "1" {
		t.Skip("csi-sanity (Ginkgo) runs once per process; use -count=1")
	}
	ep := endpointClient(t)

	// unix socket paths are limited to ~108 bytes; t.TempDir can exceed that
	dir, err := os.MkdirTemp("", "zfs-csi")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "csi.sock")
	lis, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	csi.RegisterIdentityServer(srv, &zcsi.Identity{EP: ep})
	// "mount" (the default) provisions filesystems, "block" zvols, which
	// needs -enable-block
	at := os.Getenv("CSI_SANITY_ACCESS_TYPE")
	csi.RegisterControllerServer(srv, &zcsi.Controller{EP: ep, EnableBlock: at == "block"})
	csi.RegisterNodeServer(srv, cleanupNode{})
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)

	cfg := sanity.NewTestConfig()
	cfg.Address = "unix://" + sock
	cfg.TargetPath = filepath.Join(dir, "target")
	cfg.StagingPath = filepath.Join(dir, "staging")
	cfg.TestVolumeSize = 1 << 30 // a sparse 20G CI pool holds many of these
	if at != "" {
		cfg.TestVolumeAccessType = at
	}

	sc := sanity.GinkgoTest(&cfg)
	defer sc.Finalize()
	RegisterFailHandler(Fail)
	suite, reporter := GinkgoConfiguration()
	if len(suite.FocusStrings) == 0 {
		suite.FocusStrings = []string{sanityFocus}
	}
	RunSpecs(t, "zfs-csi sanity", suite, reporter)
}
