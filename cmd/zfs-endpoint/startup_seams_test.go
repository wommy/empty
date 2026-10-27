package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	pb "github.com/wommy/empty/gen/zfsendpoint/v1"
	"github.com/wommy/empty/internal/unixsock"
	"github.com/wommy/empty/internal/zfs"
)

// notASocket returns a -listen address naming a regular file. Listen
// refuses it (exit 1), so a startup check that is missing shows as that
// exit instead of a daemon that serves until the test times out.
func notASocket(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return "unix://" + p
}

// Startup refuses, with 78 (RestartPreventExitStatus=78: restarting cannot
// fix it), what would otherwise fail later with 1, which
// Restart=on-failure and StartLimitIntervalSec=0 restart forever, or what
// would serve with a value the operator did not ask for.
func TestStartupConfigRefusals(t *testing.T) {
	t.Parallel()
	// sun_path is 108 bytes with its NUL
	long := "unix://" + filepath.Join(t.TempDir(), strings.Repeat("x", 120)+".sock")
	// 30 raw bytes under the limit, but each '-' is \x2d (4 bytes) in the
	// default socket name, as in the unit's %i.sock
	dashed := "tank/" + strings.Repeat("a-", 30)
	for _, tc := range []struct {
		name string
		args []string
		edit func(*testing.T, *deps)
		msg  string
	}{
		// S5: a negative wait is no meaning anyone asked for
		{"negative lock-wait", []string{"-lock-wait", "-1s"}, nil, "-lock-wait"},
		// S5: /0 would let PublishFileExport export to the world; it is
		// refused rather than silently read as the /16 default
		{"nfs-min-prefix 0", []string{"-nfs-min-prefix", "0"}, nil, "-nfs-min-prefix"},
		{"nfs-min-prefix 33", []string{"-nfs-min-prefix", "33"}, nil, "-nfs-min-prefix"},
		// S9/H-07: fails at bind, after the lock and the parent wait
		{"long socket path", []string{"-listen", long}, nil, "108"},
		{"long default socket", []string{"-parent", dashed, "-listen", ""}, nil, "108"},
		// a lock file name is NAME_MAX (255) at most
		{"long lock name", []string{"-parent", "tank/" + strings.Repeat("v", 250)}, nil, "255"},
		// S9: another endpoint holds the parent; a restart meets it again
		{"lock conflict", nil, func(t *testing.T, d *deps) {
			unlock, err := lockParent(d.lockDir, "tank/k8s")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(unlock)
		}, "already manages"},
		// H-13: a symlink at a lock path is never followed
		{"symlinked lock", nil, func(t *testing.T, d *deps) {
			victim := filepath.Join(t.TempDir(), "victim")
			if err := os.WriteFile(victim, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(victim, filepath.Join(d.lockDir, "tank.lock")); err != nil {
				t.Fatal(err)
			}
		}, "symlink"},
	} {
		d, _ := simDeps(t)
		if tc.edit != nil {
			tc.edit(t, &d)
		}
		args := append([]string{"-parent", "tank/k8s", "-listen", notASocket(t), "-parent-wait", "0"}, tc.args...)
		err := run(context.Background(), args, d)
		if exitCode(err) != exitConfig || !strings.Contains(err.Error(), tc.msg) {
			t.Errorf("%s: exit %d %v; want %d with %q", tc.name, exitCode(err), err, exitConfig, tc.msg)
		}
	}
}

// The unit's arithmetic: /run/zfs-endpoint/sock/ (23 bytes) + %i +
// /endpoint.sock (14) leaves 70 bytes of escaped name, and %i keeps \x2d
// literally.
func TestDefaultSocketLimit(t *testing.T) {
	t.Parallel()
	fits := "tank/" + strings.Repeat("v", 65) // escaped: 70 bytes
	if _, err := socketPath(listenAddr("", fits)); err != nil {
		t.Errorf("%d-byte parent: %v", len(fits), err)
	}
	if got := len(strings.TrimPrefix(unixsock.EndpointAddr(fits), "unix://")); got != 107 {
		t.Errorf("socket path %d bytes, want 107", got)
	}
	over := fits + "v"
	if _, err := socketPath(listenAddr("", over)); exitCode(err) != exitConfig {
		t.Errorf("%d-byte parent: exit %d %v", len(over), exitCode(err), err)
	}
	// one '-' is 4 bytes: 70 raw bytes with one dash overflow
	dash := "tank/" + strings.Repeat("v", 63) + "-v"
	if _, err := socketPath(listenAddr("", dash)); exitCode(err) != exitConfig {
		t.Errorf("%q: exit %d %v", dash, exitCode(err), err)
	}
}

// H-13: the lock open never follows a symlink, and leaves its target alone.
func TestLockParentDoesNotFollowSymlinks(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"tank.lock", "tank%k8s.lock"} {
		link := filepath.Join(dir, name)
		if err := os.Symlink(victim, link); err != nil {
			t.Fatal(err)
		}
		unlock, err := lockParent(dir, "tank/k8s")
		if err == nil {
			unlock()
			t.Errorf("%s: opened a symlink at a lock path", name)
		} else if exitCode(err) != exitConfig {
			t.Errorf("%s: exit %d %v", name, exitCode(err), err)
		}
		os.Remove(link)
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Errorf("victim now %q", b)
	}
}

// H-14: a stop (SIGTERM cancels ctx) while waiting for the pool, or while
// probing the OpenZFS version, is a normal stop, not a failed unit.
func TestStopDuringStartupIsClean(t *testing.T) {
	t.Parallel()
	t.Run("parent wait", func(t *testing.T) {
		t.Parallel()
		d, _ := simDeps(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		// the stop arrives during the first backoff; the timer never fires
		d.after = func(time.Duration) <-chan time.Time { cancel(); return nil }
		err := run(ctx, []string{"-parent", "tank/late", "-listen", notASocket(t), "-parent-wait", "30s"}, d)
		if err != nil {
			t.Errorf("exit %d %v; want a clean stop", exitCode(err), err)
		}
	})
	t.Run("version probe", func(t *testing.T) {
		t.Parallel()
		d, _ := simDeps(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		d.version = func(ctx context.Context, _ *zfs.Client) error {
			cancel()
			return tempErr("cannot read the OpenZFS version: %v", context.Canceled)
		}
		err := run(ctx, []string{"-parent", "tank/k8s", "-listen", notASocket(t)}, d)
		if err != nil {
			t.Errorf("exit %d %v; want a clean stop", exitCode(err), err)
		}
	})
}

// S10: the default prefix moved from democratic-csi to zfs-endpoint
// (7f5cb39). A parent whose volumes all carry democratic-csi's marker
// would serve as empty (List empty, Delete FAILED_PRECONDITION): refuse,
// naming the flag that adopts them.
func TestLegacyPrefixRefusal(t *testing.T) {
	t.Parallel()
	const (
		legacy = "democratic-csi:managed_resource"
		ours   = "zfs-endpoint:managed_resource"
	)
	for _, tc := range []struct {
		name   string
		props  []map[string]string // one child of tank/k8s each
		parent map[string]string   // set on tank/k8s, inherited below
		args   []string
		code   int // 0: passes the check (then fails at the non-socket, 1)
	}{
		{"legacy only", []map[string]string{{legacy: "true"}, {legacy: "true"}}, nil, nil, exitConfig},
		{"no volumes", nil, nil, nil, 0},
		{"ours only", []map[string]string{{ours: "true"}}, nil, nil, 0},
		{"both", []map[string]string{{legacy: "true"}, {ours: "true"}}, nil, nil, 0},
		{"legacy false", []map[string]string{{legacy: "false"}}, nil, nil, 0},
		// inherited is not owned (ADR 0003)
		{"legacy inherited", []map[string]string{nil}, map[string]string{legacy: "true"}, nil, 0},
		{"adopted", []map[string]string{{legacy: "true"}}, nil, []string{"-property-prefix", "democratic-csi"}, 0},
		// an explicit prefix is the operator's choice, legacy volumes or not
		{"explicit default", []map[string]string{{legacy: "true"}}, nil, []string{"-property-prefix", "zfs-endpoint"}, 0},
	} {
		d, sim := simDeps(t)
		z := zfs.New(sim)
		if tc.parent != nil {
			if err := z.Set(context.Background(), "tank/k8s", tc.parent); err != nil {
				t.Fatal(err)
			}
		}
		for i, p := range tc.props {
			if err := z.CreateFilesystem(context.Background(), "tank/k8s/v"+string(rune('a'+i)), p, false); err != nil {
				t.Fatal(err)
			}
		}
		args := append([]string{"-parent", "tank/k8s", "-listen", notASocket(t)}, tc.args...)
		err := run(context.Background(), args, d)
		switch {
		case tc.code == exitConfig:
			if exitCode(err) != exitConfig || !strings.Contains(err.Error(), "-property-prefix democratic-csi") {
				t.Errorf("%s: exit %d %v; want 78 naming -property-prefix democratic-csi", tc.name, exitCode(err), err)
			}
		case err == nil || !strings.Contains(err.Error(), "not a socket"):
			t.Errorf("%s: exit %d %v; want the check passed", tc.name, exitCode(err), err)
		}
	}
}

// gate blocks the first `zfs create` until released, holding that
// volume's lock.
type gate struct {
	zfs.Runner
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (g *gate) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "create" {
		first := false
		g.once.Do(func() { first = true })
		if first {
			close(g.entered)
			<-g.release
		}
	}
	return g.Runner.Run(ctx, name, args...)
}

// S5: -lock-wait 0 means try once, never wait (Endpoint.lock's TryAcquire
// path), not the 30s default: a call on a busy volume is Aborted while the
// holder still runs.
func TestLockWaitZeroTriesOnce(t *testing.T) {
	d, _ := simDeps(t)
	g := &gate{Runner: d.runner, entered: make(chan struct{}), release: make(chan struct{})}
	d.runner = g
	defer func() {
		select {
		case <-g.release:
		default:
			close(g.release)
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sock := filepath.Join(t.TempDir(), "s.sock")
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"-parent", "tank/k8s", "-listen", "unix://" + sock, "-lock-wait", "0"}, d)
	}()
	// a dial before the socket exists is retried after BaseDelay, not
	// grpc's default 1s
	conn, err := grpc.NewClient("unix://"+sock, grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithConnectParams(grpc.ConnectParams{Backoff: backoff.Config{BaseDelay: time.Millisecond, Multiplier: 2, MaxDelay: 10 * time.Millisecond}}))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	c := pb.NewEndpointServiceClient(conn)
	req := &pb.CreateVolumeRequest{Name: "pvc-a", CapacityBytes: 1 << 20}
	first := make(chan error, 1)
	// WaitForReady: the call waits for the socket, no polling
	go func() { _, err := c.CreateVolume(ctx, req, grpc.WaitForReady(true)); first <- err }()
	select {
	case <-g.entered:
	case err := <-done:
		t.Fatalf("run ended: %v", err)
	}
	// a bound on failure only: a waiting call would otherwise wait 30s
	bctx, bcancel := context.WithCancel(ctx)
	defer bcancel()
	waited := time.AfterFunc(5*time.Second, bcancel)
	_, err = c.CreateVolume(bctx, req, grpc.WaitForReady(true))
	if !waited.Stop() {
		t.Fatalf("the second call waited for the lock: %v", err)
	}
	if status.Code(err) != codes.Aborted {
		t.Fatalf("busy volume: %v, want Aborted", err)
	}
	close(g.release)
	if err := <-first; err != nil {
		t.Fatalf("holder: %v", err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
