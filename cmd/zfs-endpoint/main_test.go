package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	pb "github.com/wommy/empty/gen/zfsendpoint/v1"
	"github.com/wommy/empty/internal/server"
	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

func simDeps(t *testing.T) (deps, *zfstest.Sim) {
	t.Helper()
	sim := zfstest.New("tank")
	z := zfs.New(sim)
	ctx := context.Background()
	if err := z.CreateFilesystem(ctx, "tank/k8s", nil, false); err != nil {
		t.Fatal(err)
	}
	if err := z.CreateVolume(ctx, "tank/zvol", 1<<20, true, nil); err != nil {
		t.Fatal(err)
	}
	return deps{runner: sim, lockDir: t.TempDir(), euid: 0, etab: sim}, sim
}

// Startup refuses what it cannot serve safely, with an exit code the unit
// acts on: 78 (do not restart) for configuration, 75 (retry) for a parent
// that is not there yet.
func TestStartupRefusals(t *testing.T) {
	sock := "unix://" + filepath.Join(t.TempDir(), "s.sock")
	for _, tc := range []struct {
		name string
		args []string
		edit func(*deps)
		code int
		msg  string
	}{
		{"tcp", []string{"-parent", "tank/k8s", "-listen", "0.0.0.0:9000"}, nil, exitConfig, "TCP is refused"},
		{"tcp loopback", []string{"-parent", "tank/k8s", "-listen", "127.0.0.1:9000"}, nil, exitConfig, "TCP is refused"},
		{"abstract socket", []string{"-parent", "tank/k8s", "-listen", "unix://@zfs-endpoint"}, nil, exitConfig, "abstract"},
		{"relative socket", []string{"-parent", "tank/k8s", "-listen", "unix://run/x.sock"}, nil, exitConfig, "absolute"},
		{"no parent", []string{"-listen", sock}, nil, exitConfig, "-parent is required"},
		{"dotdot parent", []string{"-parent", "tank/../x", "-listen", sock}, nil, exitConfig, `".." component`},
		{"dot parent", []string{"-parent", "tank/./x", "-listen", sock}, nil, exitConfig, `"." component`},
		{"overlong parent", []string{"-parent", "tank/" + strings.Repeat("p", 122), "-listen", sock, "-parent-wait", "0"}, nil, exitConfig, "over 126"},
		{"non-root", []string{"-parent", "tank/k8s", "-listen", sock}, func(d *deps) { d.euid = 1000 }, exitConfig, "must run as root"},
		{"zvol parent", []string{"-parent", "tank/zvol", "-listen", sock}, nil, exitConfig, "is a volume, not a filesystem"},
		{"missing parent", []string{"-parent", "tank/nope", "-listen", sock, "-parent-wait", "0"}, nil, exitTempFail, "not available"},
		{"wrong openzfs", []string{"-parent", "tank/k8s", "-listen", sock}, func(d *deps) {
			d.version = func(context.Context, *zfs.Client) error { return configErr("OpenZFS 2.4.x is required") }
		}, exitConfig, "2.4"},
		{"bad max-zfs", []string{"-parent", "tank/k8s", "-listen", sock, "-max-zfs", "0"}, nil, exitConfig, "-max-zfs"},
		// wave13 Q1: an address csi-driver-nfs would mount, or nothing
		{"nfs-server path", []string{"-parent", "tank/k8s", "-listen", sock, "-nfs-server", "nfs:/x"}, nil, exitConfig, "-nfs-server"},
		{"nfs-server octal", []string{"-parent", "tank/k8s", "-listen", sock, "-nfs-server", "192.0.2.010"}, nil, exitConfig, "-nfs-server"},
		{"nfs-server twice", []string{"-parent", "tank/k8s", "-listen", sock, "-nfs-server", "192.0.2.10", "-nfs-server", "192.0.2.10"}, nil, exitConfig, "twice"},
		{"nfs-server non-root", []string{"-parent", "tank/k8s", "-listen", sock, "-allow-non-root", "-nfs-server", "192.0.2.10"},
			func(d *deps) { d.euid = 1000 }, exitConfig, "-nfs-server needs root"},
	} {
		d, _ := simDeps(t)
		if tc.edit != nil {
			tc.edit(&d)
		}
		err := run(context.Background(), tc.args, d)
		if err == nil || exitCode(err) != tc.code || !strings.Contains(err.Error(), tc.msg) {
			t.Errorf("%s: exit %d %v; want %d with %q", tc.name, exitCode(err), err, tc.code, tc.msg)
		}
	}
}

// A missing parent is retried with backoff for -parent-wait, so a pool
// imported a moment after the daemon starts is picked up without a restart.
func TestStartupWaitsForTheParent(t *testing.T) {
	d, sim := simDeps(t)
	// the pool is imported during the first backoff: the injected timer
	// creates the parent and fires at once, so no wall time passes
	var backoffs []time.Duration
	d.after = func(b time.Duration) <-chan time.Time {
		backoffs = append(backoffs, b)
		if len(backoffs) == 2 {
			zfs.New(sim).CreateFilesystem(context.Background(), "tank/late", nil, false)
		}
		c := make(chan time.Time, 1)
		c <- time.Time{}
		return c
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sock := filepath.Join(t.TempDir(), "s.sock")
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"-parent", "tank/late", "-listen", "unix://" + sock, "-parent-wait", "30s"}, d)
	}()
	waitSocket(t, sock, done)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// backoff starts at 1s and doubles
	if !slices.Equal(backoffs, []time.Duration{time.Second, 2 * time.Second}) {
		t.Fatalf("backoffs %v", backoffs)
	}
}

func waitSocket(t *testing.T, sock string, done chan error) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(sock); err == nil {
			return
		}
		select {
		case err := <-done:
			t.Fatalf("run ended before serving: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("never served")
		}
	}
}

func TestCheckVersion(t *testing.T) {
	ctx := context.Background()
	if err := checkVersion(ctx, zfs.New(versionRunner(`{"zfs_version":{"userland":"zfs-2.4.0-1","kernel":"zfs-kmod-2.4.0-1"}}`))); err != nil {
		t.Fatalf("2.4: %v", err)
	}
	err := checkVersion(ctx, zfs.New(versionRunner(`{"zfs_version":{"userland":"zfs-2.4.0-1","kernel":"zfs-kmod-2.3.4-1"}}`)))
	if exitCode(err) != exitConfig || !strings.Contains(err.Error(), "zfs-kmod-2.3.4-1") {
		t.Fatalf("2.3 module: exit %d %v", exitCode(err), err)
	}
	err = checkVersion(ctx, &zfs.Client{Runner: zfs.LocalRunner{}, Binary: filepath.Join(t.TempDir(), "no-zfs")})
	if exitCode(err) != exitConfig || !strings.Contains(err.Error(), "cannot run zfs") {
		t.Fatalf("missing binary: exit %d %v", exitCode(err), err)
	}
}

type versionRunner string

func (v versionRunner) Run(context.Context, string, ...string) ([]byte, error) { return []byte(v), nil }

// The daemon serves, reports health and readiness, and on cancellation
// drains and returns cleanly.
func TestServeHealthNotifyAndStop(t *testing.T) {
	d, _ := simDeps(t)
	dir := t.TempDir()
	notifySock := filepath.Join(dir, "notify")
	nl, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: notifySock, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer nl.Close()
	t.Setenv("NOTIFY_SOCKET", notifySock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sock := filepath.Join(dir, "run", "s.sock")
	done := make(chan error, 1)
	go func() { done <- run(ctx, []string{"-parent", "tank/k8s", "-listen", "unix://" + sock}, d) }()
	waitSocket(t, sock, done)
	if fi, err := os.Stat(filepath.Dir(sock)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("socket dir %v %v", fi.Mode(), err)
	}

	conn, err := grpc.NewClient("unix://"+sock, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := pb.NewEndpointServiceClient(conn).CreateVolume(ctx, &pb.CreateVolumeRequest{Name: "pvc-a", CapacityBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	h, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{Service: server.ServiceName})
	if err != nil || h.Status != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("health %v %v", h, err)
	}
	if got := readNotify(t, nl); !strings.Contains(got, "READY=1") {
		t.Fatalf("notify %q", got)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("did not stop")
	}
	if got := readNotify(t, nl); !strings.Contains(got, "STOPPING=1") {
		t.Fatalf("notify %q", got)
	}
}

func readNotify(t *testing.T, c *net.UnixConn) string {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	b := make([]byte, 4096)
	n, err := c.Read(b)
	if err != nil {
		t.Fatal(err)
	}
	return string(b[:n])
}

func TestLockParentIsExclusive(t *testing.T) {
	dir := t.TempDir()
	unlock, err := lockParent(dir, "tank/k8s/vols")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockParent(dir, "tank/k8s/vols"); err == nil || !strings.Contains(err.Error(), "already manages") {
		t.Fatalf("second lock: %v", err)
	}
	if u2, err := lockParent(dir, "tank/other"); err != nil {
		t.Fatalf("other parent: %v", err)
	} else {
		u2()
	}
	unlock()
	u3, err := lockParent(dir, "tank/k8s/vols")
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	u3()
}

// Nested parents conflict both ways; siblings do not.
func TestLockParentRefusesNestedParents(t *testing.T) {
	dir := t.TempDir()
	unlock, err := lockParent(dir, "tank/k8s")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"tank/k8s/vols", "tank/k8s/vols/deep", "tank"} {
		if u, err := lockParent(dir, p); err == nil || !strings.Contains(err.Error(), "already manages") {
			if u != nil {
				u()
			}
			t.Errorf("%s beside tank/k8s: %v", p, err)
		}
	}
	unlock()

	a, err := lockParent(dir, "tank/k8s/a")
	if err != nil {
		t.Fatal(err)
	}
	defer a()
	b, err := lockParent(dir, "tank/k8s/b")
	if err != nil {
		t.Fatalf("sibling: %v", err)
	}
	defer b()
	if u, err := lockParent(dir, "tank/k8s"); err == nil {
		u()
		t.Fatal("ancestor of running endpoints admitted")
	}
	// a failed attempt leaves nothing held: a's subtree is still locked
	// and tank/other is still free
	if _, err := lockParent(dir, "tank/k8s/a"); err == nil {
		t.Fatal("second endpoint for tank/k8s/a")
	}
	o, err := lockParent(dir, "tank/other")
	if err != nil {
		t.Fatalf("unrelated parent: %v", err)
	}
	o()
}
