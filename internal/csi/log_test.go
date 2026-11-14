package csi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/wommy/empty/gen/zfsendpoint/v1"
	zcsi "github.com/wommy/empty/internal/csi"
	"github.com/wommy/empty/internal/server"
)

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

// records decodes buf's JSON lines whose msg is msg.
func records(t *testing.T, buf *syncBuffer, msg string) []map[string]any {
	t.Helper()
	buf.mu.Lock()
	text := strings.TrimSpace(buf.b.String())
	buf.mu.Unlock()
	var out []map[string]any
	if text == "" {
		return nil
	}
	for _, line := range strings.Split(text, "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q is not JSON: %v", line, err)
		}
		if rec["msg"] == msg {
			out = append(out, rec)
		}
	}
	return out
}

// csiServer serves the Identity and Controller services on ep's
// connection, behind zcsi.UnaryInterceptor logging to log, as zfs-csi
// does, and returns a client of each.
func csiServer(t *testing.T, conn *grpc.ClientConn, log *slog.Logger) (csi.ControllerClient, csi.IdentityClient) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(grpc.UnaryInterceptor(zcsi.UnaryInterceptor(log)))
	csi.RegisterControllerServer(srv, &zcsi.Controller{EP: pb.NewEndpointServiceClient(conn), EnableLocalFilesystem: true})
	csi.RegisterIdentityServer(srv, &zcsi.Identity{Health: healthpb.NewHealthClient(conn)})
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	cc, err := grpc.NewClient("passthrough:///csi",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cc.Close() })
	return csi.NewControllerClient(cc), csi.NewIdentityClient(cc)
}

// A failed CSI call leaves one zfs-csi line (method, code, volume id, the
// PVC external-provisioner names with --extra-create-metadata, and a
// request id), and the endpoint's "rpc" audit line for the same call
// carries the same request id: zfs-csi forwards it as x-request-id, the
// key the endpoint reads. A sane request id from the caller is kept; one
// the endpoint would refuse is replaced. Successful calls log nothing.
func TestFailedCallLogsShareRequestIDWithEndpoint(t *testing.T) {
	t.Parallel()
	var epBuf, csiBuf syncBuffer
	conn := daemonConn(t, slog.New(server.LogHandler(slog.NewJSONHandler(&epBuf, nil))))
	ctrl, _ := csiServer(t, conn, slog.New(slog.NewJSONHandler(&csiBuf, nil)))

	ctx := context.Background()
	req := func(size int64) *csi.CreateVolumeRequest {
		return &csi.CreateVolumeRequest{
			Name:          "pvc-0f1e2d3c",
			CapacityRange: &csi.CapacityRange{RequiredBytes: size},
			VolumeCapabilities: []*csi.VolumeCapability{{
				AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
				AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
			}},
			Parameters: map[string]string{
				"csi.storage.k8s.io/pvc/name":      "data-web-0",
				"csi.storage.k8s.io/pvc/namespace": "shop",
				"csi.storage.k8s.io/pv/name":       "pvc-0f1e2d3c",
			},
		}
	}
	if _, err := ctrl.CreateVolume(ctx, req(64<<20)); err != nil {
		t.Fatal(err)
	}
	if got := records(t, &csiBuf, "rpc failed"); len(got) != 0 {
		t.Fatalf("a successful call logged %v", got)
	}

	for _, tc := range []struct {
		name, incoming, want string // want "" = minted
	}{
		{"minted", "", ""},
		{"from the caller", "sidecar-7.a_b", "sidecar-7.a_b"},
		{"refused from the caller", "not a token", ""},
	} {
		before := len(records(t, &csiBuf, "rpc failed"))
		cctx := ctx
		if tc.incoming != "" {
			cctx = metadata.AppendToOutgoingContext(ctx, zcsi.RequestIDKey, tc.incoming)
		}
		// same name, another size: the endpoint refuses it
		_, err := ctrl.CreateVolume(cctx, req(128<<20))
		if status.Code(err) != codes.AlreadyExists {
			t.Fatalf("%s: conflicting create: %v, want AlreadyExists", tc.name, err)
		}
		lines := records(t, &csiBuf, "rpc failed")
		if len(lines) != before+1 {
			t.Fatalf("%s: %d zfs-csi lines for one failed call, want 1", tc.name, len(lines)-before)
		}
		l := lines[len(lines)-1]
		for k, v := range map[string]any{
			"level":         "WARN",
			"method":        "/csi.v1.Controller/CreateVolume",
			"code":          "AlreadyExists",
			"volume_id":     "pvc-0f1e2d3c",
			"pvc_name":      "data-web-0",
			"pvc_namespace": "shop",
		} {
			if l[k] != v {
				t.Errorf("%s: zfs-csi line %s=%v, want %v (%v)", tc.name, k, l[k], v, l)
			}
		}
		if e, _ := l["error"].(string); e == "" {
			t.Errorf("%s: zfs-csi line has no error: %v", tc.name, l)
		}
		id, _ := l["request_id"].(string)
		if id == "" || tc.want != "" && id != tc.want || tc.want == "" && id == tc.incoming {
			t.Errorf("%s: zfs-csi request_id %q (incoming %q)", tc.name, id, tc.incoming)
		}
		// every endpoint call the CSI call made carries its id (CreateVolume
		// asks GetCapabilities first), the failed CreateVolume once
		var creates []map[string]any
		for _, r := range records(t, &epBuf, "rpc") {
			if r["request_id"] == id && r["method"] == pb.EndpointService_CreateVolume_FullMethodName {
				creates = append(creates, r)
			}
		}
		if len(creates) != 1 || creates[0]["code"] != "AlreadyExists" {
			t.Errorf("%s: endpoint CreateVolume lines with request_id %q: %v, want the one that failed", tc.name, id, creates)
		}
	}
}

// Every call gets its own request id, a failed call without a volume id
// logs no empty volume or PVC fields, and a call refused before it reaches
// the endpoint is logged too.
func TestFailedCallLogFields(t *testing.T) {
	t.Parallel()
	var csiBuf syncBuffer
	ctrl, _ := csiServer(t, endpointConn(t), slog.New(slog.NewJSONHandler(&csiBuf, nil)))
	ctx := context.Background()
	for range 2 {
		if _, err := ctrl.DeleteVolume(ctx, &csi.DeleteVolumeRequest{}); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("DeleteVolume without an id: %v", err)
		}
	}
	_, err := ctrl.ControllerExpandVolume(ctx, &csi.ControllerExpandVolumeRequest{VolumeId: "pvc-gone", CapacityRange: &csi.CapacityRange{RequiredBytes: 1 << 30}})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expand a missing volume: %v, want NotFound", err)
	}
	if _, err := ctrl.CreateSnapshot(ctx, &csi.CreateSnapshotRequest{Name: "snap-1", SourceVolumeId: "pvc-gone"}); status.Code(err) == codes.OK {
		t.Fatal("snapshot of a missing volume succeeded")
	}
	lines := records(t, &csiBuf, "rpc failed")
	if len(lines) != 4 {
		t.Fatalf("%d lines, want 4: %v", len(lines), lines)
	}
	if lines[0]["request_id"] == lines[1]["request_id"] {
		t.Errorf("two calls share request id %v", lines[0]["request_id"])
	}
	for _, k := range []string{"volume_id", "snapshot_id", "pvc_name", "pvc_namespace"} {
		if _, ok := lines[0][k]; ok {
			t.Errorf("DeleteVolume without an id logged %s: %v", k, lines[0])
		}
	}
	if l := lines[2]; l["volume_id"] != "pvc-gone" || l["code"] != "NotFound" || l["method"] != "/csi.v1.Controller/ControllerExpandVolume" {
		t.Errorf("ControllerExpandVolume line %v", l)
	}
	if l := lines[3]; l["volume_id"] != "pvc-gone" || l["snapshot_name"] != "snap-1" || l["method"] != "/csi.v1.Controller/CreateSnapshot" {
		t.Errorf("CreateSnapshot line %v", l)
	}
}
