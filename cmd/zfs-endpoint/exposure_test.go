package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wommy/empty/internal/zfs"
)

// F5: `zfs set sharenfs=on tank` exports /tank rw,crossmnt to everyone,
// and with it every volume the endpoint would create under tank/k8s,
// whatever the volume's own sharenfs. Startup refuses (78: restarting
// cannot fix it) and names the line; an ancestor shared without crossmnt
// does not stop it.
// F5 after startup: a crossmnt export that comes to cover the parent
// while the endpoint serves is a WARN from the health probe, once per
// change, and health stays SERVING (the check returns nil): NOT_SERVING
// would refuse every CSI call, deletes included. A pool that stops
// answering still fails the check.
func TestHealthWarnsOfALaterCrossmntAncestor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d, sim := simDeps(t)
	z := zfs.New(sim)
	var logs bytes.Buffer
	check := healthCheck(z, "tank/k8s", d.etab, 0, slog.New(slog.NewTextHandler(&logs, nil)))
	step := func(share string, wantWarn, wantInfo int) {
		t.Helper()
		if share != "" {
			if err := z.Set(ctx, "tank", map[string]string{"sharenfs": share}); err != nil {
				t.Fatal(err)
			}
		}
		logs.Reset()
		if err := check(ctx); err != nil {
			t.Fatalf("sharenfs=%s: health check failed (NOT_SERVING): %v", share, err)
		}
		warn := strings.Count(logs.String(), "level=WARN")
		info := strings.Count(logs.String(), "level=INFO")
		if warn != wantWarn || info != wantInfo || wantWarn > 0 && !strings.Contains(logs.String(), "crossmnt") {
			t.Errorf("sharenfs=%s: %d warnings, %d infos, want %d, %d:\n%s", share, warn, info, wantWarn, wantInfo, logs.String())
		}
	}
	step("", 0, 0)    // as at startup: nothing covers it
	step("on", 1, 0)  // libshare: rw,crossmnt to "*"
	step("", 0, 0)    // unchanged: not repeated
	step("off", 0, 1) // gone
	step("crossmnt,ro=@10.9.0.0/16", 1, 0)

	sim.Fail = func(args []string) error { return &zfs.ExitError{Code: 1, Stderr: "pool I/O is currently suspended\n"} }
	if err := check(ctx); err == nil {
		t.Error("a pool that does not answer passed the health check")
	}
}

// With -min-free-bytes set, the health probe reads the parent's available
// space in its own zfs get and logs a WARN once when it falls below the
// floor and an INFO once it is back; health stays SERVING.
func TestHealthWarnsOfFreeSpaceBelowTheFloor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d, sim := simDeps(t)
	var calls int
	sim.Fail = func(args []string) error {
		calls++
		return nil
	}
	z := zfs.New(sim)
	var logs bytes.Buffer
	check := healthCheck(z, "tank/k8s", d.etab, 1<<30, slog.New(slog.NewJSONHandler(&logs, nil)))
	step := func(pool uint64, want string) {
		t.Helper()
		sim.PoolSize = pool
		logs.Reset()
		calls = 0
		if err := check(ctx); err != nil {
			t.Fatalf("pool %d: health check failed (NOT_SERVING): %v", pool, err)
		}
		if got := strings.TrimSpace(logs.String()); want == "" && got != "" || !strings.Contains(got, want) {
			t.Errorf("pool %d: logged %s, want %s", pool, got, want)
		}
		// the type and available get, and the exposure check's mountpoint
		if calls != 2 {
			t.Errorf("pool %d: %d zfs calls, want 2", pool, calls)
		}
	}
	step(1<<40, "")
	step(1<<29, `"level":"WARN","msg":"health: free space below -min-free-bytes`)
	step(1<<28, "") // still low: not repeated
	step(1<<40, `"level":"INFO","msg":"health: free space back above -min-free-bytes","parent":"tank/k8s","available":`)
	step(1<<40, "")
}

// F12: a parent mountpoint other users may search (o+x) lets every local
// user reach what NFS clients write to the volumes below it. Startup
// warns, naming the mode; it does not refuse. Owner- and group-only
// modes, a missing mountpoint and none/legacy are quiet.
func TestStartupWarnsOfATraversableMountpoint(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	for _, tc := range []struct {
		mode os.FileMode // 0: no such directory
		warn bool
	}{{0o755, true}, {0o701, true}, {0o750, false}, {0o700, false}, {0o710, false}, {0, false}} {
		dir := filepath.Join(base, tc.mode.String())
		if tc.mode != 0 {
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, tc.mode); err != nil {
				t.Fatal(err)
			}
		}
		var logs bytes.Buffer
		warnTraversable(slog.New(slog.NewTextHandler(&logs, nil)), "tank/k8s", dir)
		if got := strings.Contains(logs.String(), "level=WARN"); got != tc.warn || got && !strings.Contains(logs.String(), tc.mode.String()) {
			t.Errorf("mode %v: warned %v, want %v: %s", tc.mode, got, tc.warn, logs.String())
		}
	}
	for _, mount := range []string{"none", "legacy", "-"} {
		var logs bytes.Buffer
		warnTraversable(slog.New(slog.NewTextHandler(&logs, nil)), "tank/k8s", mount)
		if logs.Len() != 0 {
			t.Errorf("mountpoint %s: %s", mount, logs.String())
		}
	}
}

func TestStartupRefusesACrossmntAncestor(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		share   string
		refused bool
	}{
		{"on", true},               // libshare: rw,crossmnt to "*"
		{"rw=@10.0.0.0/24", false}, // our form: no crossmnt
		{"crossmnt,ro=@10.9.0.0/16", true},
	} {
		t.Run(tc.share, func(t *testing.T) {
			t.Parallel()
			d, sim := simDeps(t)
			if err := zfs.New(sim).Set(context.Background(), "tank", map[string]string{"sharenfs": tc.share}); err != nil {
				t.Fatal(err)
			}
			err := run(context.Background(), []string{"-parent", "tank/k8s", "-listen", notASocket(t), "-parent-wait", "0"}, d)
			refused := exitCode(err) == exitConfig && strings.Contains(err.Error(), "crossmnt NFS export") && strings.Contains(err.Error(), "/tank ")
			if refused != tc.refused {
				t.Errorf("exit %d %v; refused %v, want %v", exitCode(err), err, refused, tc.refused)
			}
		})
	}
}
