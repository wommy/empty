package main

import (
	"bytes"
	"context"
	"log/slog"
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
	check := healthCheck(z, "tank/k8s", d.etab, slog.New(slog.NewTextHandler(&logs, nil)))
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
