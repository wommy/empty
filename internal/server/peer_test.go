package server_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/wommy/empty/gen/zfsendpoint/v1"
	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/server"
	"github.com/wommy/empty/internal/zfs"
)

// unixDaemon serves a Daemon on a real unix socket in a temp directory
// and returns a client dialled over it.
func unixDaemon(t *testing.T, r zfs.Runner, log *slog.Logger) pb.EndpointServiceClient {
	t.Helper()
	ep, err := endpoint.New(zfs.New(r), endpoint.Config{Parent: "tank/vols"})
	if err != nil {
		t.Fatal(err)
	}
	d := server.NewDaemon(&server.Server{EP: ep, Parent: "tank/vols"}, log)
	d.SetServing(true)
	sock := filepath.Join(t.TempDir(), "s.sock")
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
	return pb.NewEndpointServiceClient(conn)
}

// rpcLines returns the decoded "rpc" log records in buf.
func rpcLines(t *testing.T, buf *syncBuffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		if rec["msg"] == "rpc" {
			out = append(out, rec)
		}
	}
	return out
}

// F7: a unix peer has no address, so the audit line said nothing about
// who called. Every call's line carries the caller's SO_PEERCRED (uid,
// gid and pid, as at connect(2)).
func TestPeerCredentialsAreLogged(t *testing.T) {
	t.Parallel()
	var buf syncBuffer
	c := unixDaemon(t, &deadlineRunner{}, slog.New(slog.NewJSONHandler(&buf, nil)))
	if _, err := c.GetCapacity(context.Background(), &pb.GetCapacityRequest{}); err != nil {
		t.Fatal(err)
	}
	lines := rpcLines(t, &buf)
	if len(lines) != 1 {
		t.Fatalf("%d rpc lines: %s", len(lines), buf.String())
	}
	if r := lines[0]["role"]; r != "csi" {
		t.Errorf("role %v, want csi", r)
	}
	for k, want := range map[string]int{"peer_uid": os.Getuid(), "peer_gid": os.Getgid(), "peer_pid": os.Getpid()} {
		if got, ok := lines[0][k].(float64); !ok || int(got) != want {
			t.Errorf("%s = %v, want %d (line %v)", k, lines[0][k], want, lines[0])
		}
	}
}
