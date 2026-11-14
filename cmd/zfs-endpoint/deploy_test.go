package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/wommy/empty/internal/version"
)

// -version prints the build version (set by -ldflags -X, see
// internal/version) and exits 0, before -parent or root are checked.
func TestVersionFlag(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := run(context.Background(), []string{"-version"}, deps{euid: 1000, stdout: &out}); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(out.String())
	if got != version.Version || got == "" {
		t.Fatalf("printed %q, want %q", got, version.Version)
	}
	// CI's test job links every test binary with -ldflags -X
	// <internal/version.Version>=$WANT_VERSION: -X ignores a symbol that
	// does not exist, so a renamed variable or package would leave release
	// builds saying "dev" with nothing failing
	if want := os.Getenv("WANT_VERSION"); want != "" && got != want {
		t.Fatalf("-version printed %q, want %q: -ldflags -X no longer sets internal/version.Version", got, want)
	}
}

// Without -listen the daemon serves the socket zfs-endpoint@.service names
// (%i.sock, deploy/deploy_test.go) and zfs-csi -parent dials.
func TestDefaultListenIsTheUnitSocket(t *testing.T) {
	t.Parallel()
	if got := listenAddr("", "tank/k8s/vols"); got != "unix:///run/zfs-endpoint/sock/tank-k8s-vols/endpoint.sock" {
		t.Errorf("default: %q", got)
	}
	if got := listenAddr("unix:///x/y.sock", "tank/k8s/vols"); got != "unix:///x/y.sock" {
		t.Errorf("explicit: %q", got)
	}
}
