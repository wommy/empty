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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	pb "github.com/wommy/empty/gen/zfsendpoint/v1"
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
	conn, err := grpc.NewClient("unix://"+sock, grpc.WithTransportCredentials(insecure.NewCredentials()))
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
