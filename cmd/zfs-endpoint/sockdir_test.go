package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wommy/empty/internal/unixsock"
)

// F1: a pod is given an endpoint's socket by mounting its directory. If
// that directory is the lock directory, or contains it, the pod also gets
// every instance's lock files (flock one and that endpoint cannot restart)
// and every other instance's socket (unlink it and serve a fake). Such a
// -listen is a configuration error (78); a directory of its own, even
// one under the lock directory, is fine.
func TestSocketNeverSharesTheLockDirectory(t *testing.T) {
	t.Parallel()
	// a regular file stands at each path: past the check, Listen refuses
	// it (exit 1), so nothing ever serves
	file := func(t *testing.T, p string) string {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		return "unix://" + p
	}
	for _, tc := range []struct {
		name    string
		sock    func(lockDir string) string
		refused bool
	}{
		{"in the lock directory", func(l string) string { return filepath.Join(l, "x.sock") }, true},
		{"in a directory containing it", func(l string) string { return filepath.Join(filepath.Dir(l), "x.sock") }, true},
		{"in its own directory under it", func(l string) string { return filepath.Join(l, "sock", "tank-k8s", "endpoint.sock") }, false},
		{"in a sibling directory", func(l string) string { return filepath.Join(l+"-sock", "x.sock") }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, _ := simDeps(t)
			d.lockDir = filepath.Join(t.TempDir(), "run", "zfs-endpoint")
			err := run(context.Background(), []string{"-parent", "tank/k8s", "-listen", file(t, tc.sock(d.lockDir)), "-parent-wait", "0"}, d)
			if got := exitCode(err) == exitConfig && strings.Contains(err.Error(), "lock directory"); got != tc.refused {
				t.Errorf("exit %d %v; refused %v, want %v", exitCode(err), err, got, tc.refused)
			}
		})
	}
}

// The default socket (the unit's) is in a directory of its own, per
// instance, under the lock directory: never the lock directory itself.
func TestDefaultSocketHasItsOwnDirectory(t *testing.T) {
	t.Parallel()
	for _, parent := range []string{"tank/k8s/vols", "tank/a", "tank/my-vols"} {
		sock, err := socketPath(listenAddr("", parent))
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Dir(sock)
		if want := filepath.Join(unixsock.EndpointDir, "sock", unixsock.SystemdEscape(parent)); dir != want {
			t.Errorf("%s: socket directory %s, want %s", parent, dir, want)
		}
		if err := sockOutsideLocks(sock, lockDir); err != nil {
			t.Errorf("%s: %v", parent, err)
		}
	}
	if a, b := listenAddr("", "tank/a"), listenAddr("", "tank/b"); filepath.Dir(a) == filepath.Dir(b) {
		t.Errorf("two parents share a socket directory: %s, %s", a, b)
	}
}
