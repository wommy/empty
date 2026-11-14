package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	pb "github.com/wommy/empty/gen/zfsendpoint/v1"
	"github.com/wommy/empty/internal/unixsock"
)

// F2: -listen (the socket a CSI pod mounts) serves the CSI role only:
// Reconcile, which collects every volume no desired set names, and the NFS
// export RPCs are refused there. -admin-listen, a socket in a directory of
// its own, serves them.
func TestAdminRPCsOnlyOnTheAdminSocket(t *testing.T) {
	t.Parallel()
	d, _ := simDeps(t)
	dir := t.TempDir()
	sock := filepath.Join(dir, "sock", "endpoint.sock")
	admin := filepath.Join(dir, "admin", "endpoint.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"-parent", "tank/k8s", "-listen", "unix://" + sock, "-admin-listen", "unix://" + admin}, d)
	}()
	waitSocket(t, sock, done)
	waitSocket(t, admin, done)

	call := func(path string) error {
		conn, err := grpc.NewClient("unix://"+path, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_, err = pb.NewEndpointServiceClient(conn).Reconcile(ctx, &pb.ReconcileRequest{DryRun: true})
		return err
	}
	if err := call(sock); status.Code(err) != codes.PermissionDenied {
		t.Errorf("Reconcile on -listen: %v, want PermissionDenied", err)
	}
	if err := call(admin); err != nil {
		t.Errorf("Reconcile on -admin-listen: %v", err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// An admin socket a CSI pod would also get (in or under the -listen
// socket's directory), or one in the lock directory, is a configuration
// error (78).
func TestAdminSocketRefusals(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		admin func(sockDir, lockDir string) string
		msg   string
	}{
		{"tcp", func(string, string) string { return "127.0.0.1:9000" }, "only unix"},
		{"relative", func(string, string) string { return "unix://run/a.sock" }, "absolute"},
		{"beside the csi socket", func(s, _ string) string { return "unix://" + filepath.Join(s, "admin.sock") }, "-listen socket's directory"},
		{"under the csi socket's directory", func(s, _ string) string { return "unix://" + filepath.Join(s, "admin", "a.sock") }, "-listen socket's directory"},
		{"in the lock directory", func(_, l string) string { return "unix://" + filepath.Join(l, "a.sock") }, "lock directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, _ := simDeps(t)
			sockDir := filepath.Join(t.TempDir(), "sock")
			args := []string{"-parent", "tank/k8s", "-parent-wait", "0",
				"-listen", "unix://" + filepath.Join(sockDir, "endpoint.sock"),
				"-admin-listen", tc.admin(sockDir, d.lockDir)}
			err := run(context.Background(), args, d)
			if exitCode(err) != exitConfig || !strings.Contains(err.Error(), tc.msg) {
				t.Errorf("exit %d %v; want %d with %q", exitCode(err), err, exitConfig, tc.msg)
			}
		})
	}
}

// ADR 0001 v2 D7: -socket-group opens the -listen socket to a group (0660)
// for a non-root controller; without it the socket stays 0600. A group
// that does not resolve, or a -listen directory shared with anything else,
// is a configuration error (78).
func TestSocketGroup(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		group string
		want  int
		ok    bool
	}{{"", noGroup, true}, {"0", 0, true}, {"4242", 4242, true}, {"-1", 0, false}, {"no-such-group.d7", 0, false}} {
		got, err := socketGID(tc.group)
		if tc.ok != (err == nil) || tc.ok && got != tc.want || !tc.ok && exitCode(err) != exitConfig {
			t.Errorf("socketGID(%q) = %d, %v (exit %d)", tc.group, got, err, exitCode(err))
		}
	}

	gid := os.Getegid()
	if os.Geteuid() == 0 {
		gid = 65534
	}
	for _, tc := range []struct {
		gid  int
		mode os.FileMode
	}{{noGroup, 0o600}, {gid, 0o660}} {
		sock := filepath.Join(t.TempDir(), "sock", "endpoint.sock")
		lis, err := listenCSI(sock, tc.gid)
		if err != nil {
			t.Fatal(err)
		}
		fi, err := os.Lstat(sock)
		lis.Close()
		if err != nil || fi.Mode().Perm() != tc.mode {
			t.Errorf("gid %d: socket %v %v, want %v", tc.gid, fi.Mode(), err, tc.mode)
		}
	}
	shared := filepath.Join(t.TempDir(), "run")
	if err := os.Mkdir(shared, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shared, "other"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if lis, err := listenCSI(filepath.Join(shared, "endpoint.sock"), gid); exitCode(err) != exitConfig {
		if lis != nil {
			lis.Close()
		}
		t.Errorf("a shared directory: exit %d %v, want %d", exitCode(err), err, exitConfig)
	}

	d, _ := simDeps(t)
	args := []string{"-parent", "tank/k8s", "-parent-wait", "0", "-socket-group", "no-such-group.d7",
		"-listen", "unix://" + filepath.Join(t.TempDir(), "sock", "endpoint.sock")}
	// cancelled: should the flag be ignored, run stops at its first
	// startup step and returns nil, never serving
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx, args, d); exitCode(err) != exitConfig || !strings.Contains(err.Error(), "-socket-group") {
		t.Errorf("unknown group: exit %d %v, want %d", exitCode(err), err, exitConfig)
	}
}

// The unit's admin socket for any parent is apart from its CSI socket's
// directory and from the lock directory.
func TestUnitAdminSocketIsApart(t *testing.T) {
	t.Parallel()
	for _, parent := range []string{"tank/k8s/vols", "tank/my-vols"} {
		sock, err := socketPath(listenAddr("", parent))
		if err != nil {
			t.Fatal(err)
		}
		got, err := adminSocketPath(unixsock.EndpointAdminAddr(parent), sock, unixsock.EndpointDir)
		if err != nil || got == "" {
			t.Errorf("%s: %q %v", parent, got, err)
		}
	}
}
