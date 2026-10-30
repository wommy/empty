package csi_test

import (
	"cmp"
	"context"
	"embed"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-csi/csi-test/v5/pkg/sanity"
	. "github.com/onsi/ginkgo/v2"
	"github.com/onsi/ginkgo/v2/types"
	. "github.com/onsi/gomega"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	pb "github.com/wommy/empty/gen/zfsendpoint/v1"
	zcsi "github.com/wommy/empty/internal/csi"
)

// sanityFocus selects the Identity and Controller groups. The driver has no
// node plugin (ADR 0004: csi-driver-nfs is the node side), so csi-sanity's
// Node Service specs would only exercise the cleanupNode fixture below
// (unfocused they give 9 failures, 17 self-skips and 1 pass against it);
// the GroupController groups self-skip, as Identity does not advertise
// GROUP_CONTROLLER_SERVICE. Both are focused out. Ginkgo matches focus
// against "<suite description> <spec text>", so the pattern must not be
// anchored with ^.
const sanityFocus = `Identity Service|\[Controller Server\]`

// The passed specs of each mode, pinned: testdata/sanity-passed-<mode>.txt,
// one spec's full text per line, '#' lines comments. Embedded, so the test
// binary checks them wherever it runs (the real-ZFS VM's guest has no
// source tree).
//
//go:embed testdata/sanity-passed-*.txt
var sanityPinned embed.FS

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
//
// The mode, one per process (Ginkgo runs once):
//   - CSI_SANITY_ACCESS_TYPE=mount (default) or block: filesystems or zvols,
//     on an endpoint without NFS;
//   - CSI_SANITY_NFS=1: filesystems on an endpoint in NFS mode (static
//     export at create, one -nfs-server), StorageClass nfs.clients set.
//
// With the default focus, the names of the specs that passed must be
// exactly testdata/sanity-passed-<mode>.txt: a spec that starts skipping
// (a capability dropped) or a new one (a csi-test bump, a capability
// added) fails here, not only a failing spec. To accept a change, rewrite
// the list with CSI_SANITY_UPDATE=1 and review its diff.
// CSI_SANITY_PASSED_OUT=<file> also writes the passed names, sorted, for
// ci/real-zfs-vm.sh to compare on its side.
func TestSanity(t *testing.T) {
	// Ginkgo refuses -count>1 for the whole binary ("only -count=1 is
	// allowed"), which made `go test -count=N ./internal/csi` fail outright
	// and hid the package's other tests from flake hunting.
	if f := flag.Lookup("test.count"); f != nil && f.Value.String() != "1" {
		t.Skip("csi-sanity (Ginkgo) runs once per process; use -count=1")
	}
	// "mount" (the default) provisions filesystems, "block" zvols, which
	// needs -enable-block
	at := os.Getenv("CSI_SANITY_ACCESS_TYPE")
	if at != "" && at != "mount" && at != "block" {
		t.Fatalf("CSI_SANITY_ACCESS_TYPE=%q: want mount or block", at)
	}
	nfs := os.Getenv("CSI_SANITY_NFS") != ""
	mode := cmp.Or(at, "mount")
	if nfs {
		if at == "block" {
			t.Fatal("CSI_SANITY_NFS with CSI_SANITY_ACCESS_TYPE=block: NFS mode refuses block volumes")
		}
		mode = "nfs"
	}
	conn := endpointConnMode(t, nfs)
	ep := pb.NewEndpointServiceClient(conn)

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
	csi.RegisterIdentityServer(srv, &zcsi.Identity{Health: healthpb.NewHealthClient(conn)})
	// mount mode without NFS provisions node-local filesystems, which
	// are opt-in (ADR 0009)
	csi.RegisterControllerServer(srv, &zcsi.Controller{EP: ep, EnableBlock: at == "block", EnableLocalFilesystem: !nfs && at != "block"})
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
	if nfs {
		// required in NFS mode; the server is the advertised one
		cfg.TestVolumeParameters = map[string]string{zcsi.ParamNFSClients: "10.0.0.0/24"}
	}

	sc := sanity.GinkgoTest(&cfg)
	defer sc.Finalize()
	RegisterFailHandler(Fail)
	suite, reporter := GinkgoConfiguration()
	// only the default selection is pinned: -ginkgo.focus, -ginkgo.skip
	// or a label filter runs a subset on purpose
	pinned := len(suite.FocusStrings) == 0 && len(suite.SkipStrings) == 0 &&
		len(suite.FocusFiles) == 0 && len(suite.SkipFiles) == 0 && suite.LabelFilter == ""
	if pinned {
		suite.FocusStrings = []string{sanityFocus}
	}
	var passed []string
	reported := false
	ReportAfterSuite("pinned passed specs", func(r Report) {
		reported = true
		for _, s := range r.SpecReports {
			if s.LeafNodeType == types.NodeTypeIt && s.State == types.SpecStatePassed {
				passed = append(passed, s.FullText())
			}
		}
	})
	RunSpecs(t, "zfs-csi sanity", suite, reporter)
	slices.Sort(passed)

	if out := os.Getenv("CSI_SANITY_PASSED_OUT"); out != "" {
		if err := os.WriteFile(out, []byte(lines(passed)), 0o644); err != nil {
			t.Error(err)
		}
	}
	if !reported {
		t.Fatal("no Ginkgo suite report: the passed specs cannot be checked")
	}
	if !pinned {
		t.Logf("csi-sanity %s: %d specs passed; a custom focus, skip or label filter is not compared with the pinned list", mode, len(passed))
		return
	}
	checkPinnedSpecs(t, mode, passed)
}

// checkPinnedSpecs compares the passed specs with
// testdata/sanity-passed-<mode>.txt, or rewrites that file with
// CSI_SANITY_UPDATE=1 (go test runs in the package directory).
func checkPinnedSpecs(t *testing.T, mode string, passed []string) {
	t.Helper()
	name := "testdata/sanity-passed-" + mode + ".txt"
	regen := "CSI_SANITY_UPDATE=1 " + map[string]string{
		"mount": "", "block": "CSI_SANITY_ACCESS_TYPE=block ", "nfs": "CSI_SANITY_NFS=1 ",
	}[mode] + "go test -count=1 -run '^TestSanity$' ./internal/csi"
	if os.Getenv("CSI_SANITY_UPDATE") != "" {
		if t.Failed() {
			t.Fatalf("not rewriting %s: the run failed", name)
		}
		head := fmt.Sprintf("# The csi-sanity specs that pass in %s mode, compared exactly by TestSanity\n"+
			"# (internal/csi/sanity_test.go) on the simulator and on the real-ZFS VM\n"+
			"# (ci/real-zfs-vm.sh). Regenerate, then review the diff:\n#   %s\n", mode, regen)
		if err := os.WriteFile(name, []byte(head+lines(passed)), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("csi-sanity %s: wrote %d specs to %s", mode, len(passed), name)
		return
	}
	b, err := sanityPinned.ReadFile(name)
	if err != nil {
		t.Fatalf("csi-sanity %s: no pinned list (%v); create it with %s", mode, err, regen)
	}
	var want []string
	for l := range strings.SplitSeq(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			want = append(want, l)
		}
	}
	slices.Sort(want)
	var diff []string
	for _, s := range want {
		if _, ok := slices.BinarySearch(passed, s); !ok {
			diff = append(diff, "- "+s+"   (pinned, did not pass: failed, skipped or gone)")
		}
	}
	for _, s := range passed {
		if _, ok := slices.BinarySearch(want, s); !ok {
			diff = append(diff, "+ "+s+"   (passed, not pinned)")
		}
	}
	if d := slices.Compact(slices.Clone(want)); len(d) != len(want) {
		diff = append(diff, "  "+name+" lists a spec twice")
	}
	if len(diff) > 0 {
		t.Errorf("csi-sanity %s: the passed specs (%d) differ from %s (%d):\n%s\n"+
			"If the change is intended, rewrite the list and review its diff:\n  %s",
			mode, len(passed), name, len(want), strings.Join(diff, "\n"), regen)
		return
	}
	t.Logf("csi-sanity %s: the %d passed specs match %s exactly", mode, len(passed), name)
}

func lines(ss []string) string {
	var b strings.Builder
	for _, s := range ss {
		b.WriteString(s + "\n")
	}
	return b.String()
}
