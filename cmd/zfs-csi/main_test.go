package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wommy/empty/internal/version"
)

// kubelet creates the emptyDir zfs-csi and its sidecars share 0777; the CSI
// socket must be served there, still 0600, and still never over a
// non-socket.
func TestListenInAKubeletEmptyDir(t *testing.T) {
	t.Parallel()
	// unix socket paths are limited to ~108 bytes; t.TempDir can exceed that
	base, err := os.MkdirTemp("", "zfs-csi")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	dir := filepath.Join(base, "csi")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil { // what kubelet makes
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "csi.sock")
	l, err := listen("unix://" + sock)
	if err != nil {
		t.Fatalf("listen in a 0777 emptyDir: %v", err)
	}
	defer l.Close()
	if fi, err := os.Lstat(sock); err != nil || fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket %v %v, want 0600", fi.Mode(), err)
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"unix://" + file, sock, "unix://@csi", "unix://csi/csi.sock"} {
		if l, err := listen(bad); err == nil {
			l.Close()
			t.Errorf("%q: listened", bad)
		}
	}
}

// -parent derives the endpoint socket the unit serves; -endpoint overrides;
// one of them is required, never both.
func TestEndpointAddress(t *testing.T) {
	t.Parallel()
	if got, err := endpointAddress("tank/k8s/vols", ""); err != nil || got != "unix:///run/zfs-endpoint/sock/tank-k8s-vols/endpoint.sock" {
		t.Errorf("-parent: %q %v", got, err)
	}
	if got, err := endpointAddress("", "unix:///x/y.sock"); err != nil || got != "unix:///x/y.sock" {
		t.Errorf("-endpoint: %q %v", got, err)
	}
	if _, err := endpointAddress("", ""); err == nil {
		t.Error("neither accepted")
	}
	if _, err := endpointAddress("tank/a", "unix:///x/y.sock"); err == nil {
		t.Error("both accepted")
	}
}

// The opt-in flags reach the controller, and both are off by default
// (ADR 0006 for block, ADR 0009 for node-local filesystems).
func TestControllerFlags(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		args           []string
		block, localFS bool
	}{
		{nil, false, false},
		{[]string{"-enable-block"}, true, false},
		{[]string{"-enable-local-filesystem"}, false, true},
		{[]string{"-enable-local-filesystem", "-enable-block"}, true, true},
	} {
		o, err := parseFlags(append([]string{"-parent", "tank/k8s/vols"}, tc.args...))
		if err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		c := o.controller(nil)
		if c.EnableBlock != tc.block || c.EnableLocalFilesystem != tc.localFS {
			t.Errorf("%v: block %v, local filesystem %v; want %v, %v", tc.args, c.EnableBlock, c.EnableLocalFilesystem, tc.block, tc.localFS)
		}
	}
}

// -version prints the build version (set by -ldflags -X, see
// internal/version) and exits 0 before anything else is checked.
func TestVersionFlag(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := run([]string{"-version"}, &out); err != nil {
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
