package unixsock_test

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wommy/empty/internal/unixsock"
)

func TestListenIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "x.sock")
	l, err := unixsock.Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	fi, err := os.Stat(path)
	if err != nil || fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket %v %v, want 0600", fi.Mode(), err)
	}
	if di, _ := os.Stat(filepath.Dir(path)); di.Mode().Perm() != 0o700 {
		t.Fatalf("dir %v, want 0700", di.Mode())
	}
}

func TestListenRefusesALiveSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.sock")
	l, err := unixsock.Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	if _, err := unixsock.Listen(path); err == nil || !strings.Contains(err.Error(), "already being served") {
		t.Fatalf("second listen: %v", err)
	}
}

func TestListenReplacesAStaleSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close() // leaves the socket file, nobody answering
	l2, err := unixsock.Listen(path)
	if err != nil {
		t.Fatalf("stale socket: %v", err)
	}
	l2.Close()
}

// A misconfigured address must never delete a file that is not a socket.
func TestListenNeverDeletesANonSocket(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(file, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"file", "dir", "link"} {
		path := filepath.Join(dir, name)
		if l, err := unixsock.Listen(path); err == nil {
			l.Close()
			t.Errorf("%s: listened over it", name)
		}
		if _, err := os.Lstat(path); err != nil {
			t.Errorf("%s: deleted: %v", name, err)
		}
	}
	if b, _ := os.ReadFile(file); string(b) != "keep" {
		t.Error("file content changed")
	}
}

// "@name" would be a Linux abstract socket: no file, so no permissions.
// Only a clean absolute path is accepted.
func TestListenRefusesAbstractAndRelativePaths(t *testing.T) {
	for _, p := range []string{"@zfs-endpoint", "@/run/x.sock", "x.sock", "run/x.sock", "/run/../tmp/x.sock", ""} {
		if l, err := unixsock.Listen(p); err == nil {
			l.Close()
			t.Errorf("%q: listened", p)
		}
	}
}

// An existing socket directory others could write (or a symlink to one,
// or one another user owns) is refused: they could swap the socket.
func TestListenRefusesAnUnsafeDirectory(t *testing.T) {
	base := t.TempDir()
	open := filepath.Join(base, "open")
	if err := os.Mkdir(open, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o777); err != nil {
		t.Fatal(err)
	}
	safe := filepath.Join(base, "safe")
	if err := os.Mkdir(safe, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(safe, link); err != nil {
		t.Fatal(err)
	}
	dirs := []string{open, link}
	if os.Geteuid() == 0 {
		other := filepath.Join(base, "other")
		if err := os.Mkdir(other, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(other, 4321, 4321); err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, other)
	}
	for _, d := range dirs {
		if l, err := unixsock.Listen(filepath.Join(d, "x.sock")); err == nil {
			l.Close()
			t.Errorf("%s: listened in an unsafe directory", d)
		}
	}
	l, err := unixsock.Listen(filepath.Join(safe, "x.sock"))
	if err != nil {
		t.Fatalf("safe directory: %v", err)
	}
	l.Close()
}
