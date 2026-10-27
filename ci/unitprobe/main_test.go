package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/server"
	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

// serve serves one simulator endpoint on a csi and an admin socket.
func serve(t *testing.T) (csi, admin string) {
	t.Helper()
	z := zfs.New(zfstest.New("tank"))
	if err := z.CreateFilesystem(context.Background(), "tank/k8s", nil, false); err != nil {
		t.Fatal(err)
	}
	ep, err := endpoint.New(z, endpoint.Config{Parent: "tank/k8s"})
	if err != nil {
		t.Fatal(err)
	}
	d := server.NewDaemon(&server.Server{EP: ep, Parent: "tank/k8s"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.SetServing(true)
	t.Cleanup(d.GRPC.Stop)
	dir := t.TempDir()
	for _, l := range []struct {
		path *string
		role server.Role
	}{{&csi, server.RoleCSI}, {&admin, server.RoleAdmin}} {
		*l.path = filepath.Join(dir, l.role.String()+".sock")
		lis, err := net.Listen("unix", *l.path)
		if err != nil {
			t.Fatal(err)
		}
		go d.Serve(lis, l.role)
	}
	return csi, admin
}

// What ci/real-zfs-unit.sh relies on: each op's code is reported by name,
// -want decides the exit status, a create prints its id, and a socket
// that refuses connect(2) is CONNECT, not an Unavailable after retries.
func TestProbeReportsTheCodeItGot(t *testing.T) {
	t.Parallel()
	csi, admin := serve(t)
	probe := func(want string, code int, args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if got := run(args, &out); got != code {
			t.Errorf("%q: exit %d, want %d:\n%s", args, got, code, out.String())
		}
		if want != "" && !strings.Contains(out.String(), want) {
			t.Errorf("%q: %q lacks %q", args, out.String(), want)
		}
		return out.String()
	}
	probe("health: OK", 0, "-addr", "unix://"+csi, "health")
	out := probe("create: OK: ", 0, "-addr", "unix://"+csi, "create", "unit-check")
	id := strings.TrimSpace(strings.TrimPrefix(out, "create: OK: "))
	if id == "" {
		t.Fatalf("create printed no id: %q", out)
	}
	probe("delete: OK", 0, "-addr", "unix://"+csi, "delete", id)
	probe("reconcile: PermissionDenied", 0, "-addr", "unix://"+csi, "-want", "PermissionDenied", "reconcile")
	probe("want PermissionDenied", 1, "-addr", "unix://"+admin, "-want", "PermissionDenied", "reconcile")
	probe("reconcile: OK", 0, "-addr", "unix://"+admin, "reconcile")
	probe("health: CONNECT", 0, "-addr", "unix://"+filepath.Join(t.TempDir(), "none.sock"), "-want", "CONNECT", "health")
	probe("usage", 2, "-addr", "tcp://x", "health")
}
