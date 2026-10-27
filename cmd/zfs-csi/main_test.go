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

// -version prints the build version (set by -ldflags -X, see
// internal/version) and exits 0 before anything else is checked.
func TestVersionFlag(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := run([]string{"-version"}, &out); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != version.Version || got == "" {
		t.Fatalf("printed %q, want %q", got, version.Version)
	}
}
