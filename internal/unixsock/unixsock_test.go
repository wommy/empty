package unixsock_test

import (
	"github.com/wommy/empty/internal/unixsock"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
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

// A path bind cannot take (sun_path: 107 bytes and a NUL) is refused up
// front, by Listen and by Target alike; 107 bytes is served.
func TestSocketPathFitsSunPath(t *testing.T) {
	dir := t.TempDir()
	at := filepath.Join(dir, strings.Repeat("x", unixsock.MaxPathLen-len(dir)-1))
	if len(at) != unixsock.MaxPathLen {
		t.Fatalf("setup: %d bytes", len(at))
	}
	l, err := unixsock.Listen(at)
	if err != nil {
		t.Fatalf("%d bytes: %v", len(at), err)
	}
	l.Close()
	over := at + "x"
	if l, err := unixsock.Listen(over); err == nil || !strings.Contains(err.Error(), "108") {
		if l != nil {
			l.Close()
		}
		t.Errorf("%d bytes: %v", len(over), err)
	}
	if _, err := unixsock.Target("unix://" + over); err == nil {
		t.Errorf("Target accepted %d bytes", len(over))
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

// A front end's endpoint target is unix:///absolute/path only (was
// cmd/zfs-csi's TestEndpointTargetIsAnAbsoluteSocketPath; testing it here
// spares a grpc-linked test binary).
func TestTargetIsAnAbsoluteSocketPath(t *testing.T) {
	if p, err := unixsock.Target("unix:///run/zfs-endpoint/endpoint.sock"); err != nil || p != "/run/zfs-endpoint/endpoint.sock" {
		t.Fatalf("%q %v", p, err)
	}
	for _, bad := range []string{"unix://@zfs-endpoint", "unix:@zfs-endpoint", "unix://run/x.sock", "localhost:9000", "dns:///endpoint:9000"} {
		if _, err := unixsock.Target(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// ListenShared serves in a kubelet emptyDir (0777) that Listen refuses, and
// keeps every other refusal: the socket is 0600; a symlinked directory, a
// non-socket and a live socket are refused; nothing is deleted.
func TestListenSharedRelaxesOnlyTheDirectoryMode(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	open := filepath.Join(base, "csi")
	if err := os.Mkdir(open, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o777); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(open, "csi.sock")
	if l, err := unixsock.Listen(sock); err == nil {
		l.Close()
		t.Fatal("Listen accepted a 0777 directory")
	}
	l, err := unixsock.ListenShared(sock)
	if err != nil {
		t.Fatalf("ListenShared in a 0777 directory: %v", err)
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
	if fi, err := os.Lstat(sock); err != nil || fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket %v %v, want 0600", fi.Mode(), err)
	}
	if _, err := unixsock.ListenShared(sock); err == nil || !strings.Contains(err.Error(), "already being served") {
		t.Fatalf("live socket: %v", err)
	}

	file := filepath.Join(open, "file")
	if err := os.WriteFile(file, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(open, link); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{file, filepath.Join(link, "x.sock"), "@csi", "csi.sock"} {
		if l, err := unixsock.ListenShared(p); err == nil {
			l.Close()
			t.Errorf("%s: listened", p)
		}
	}
	if b, _ := os.ReadFile(file); string(b) != "keep" {
		t.Error("non-socket replaced")
	}
}

// ADR 0001 v2 D7: a socket group opens the socket (0660) and its own
// directory (0710, search only) to that group, and nothing else. A
// directory holding anything besides the socket is refused, unchanged.
func TestShareWithGroupOpensOnlyTheSocketAndItsDirectory(t *testing.T) {
	t.Parallel()
	gid := os.Getegid()
	if os.Geteuid() == 0 {
		gid = 65534 // root may give any group: prove the group changes
	}
	owned := func(p string, mode os.FileMode) {
		t.Helper()
		fi, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			t.Fatalf("%s: no owner", p)
		}
		if fi.Mode().Perm() != mode || int(st.Gid) != gid {
			t.Errorf("%s: %v gid %v, want %v gid %d", p, fi.Mode(), st.Gid, mode, gid)
		}
	}
	sock := filepath.Join(t.TempDir(), "sock", "endpoint.sock")
	l, err := unixsock.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := unixsock.ShareWithGroup(sock, gid); err != nil {
		t.Fatal(err)
	}
	owned(sock, 0o660)
	owned(filepath.Dir(sock), 0o710)
	if err := unixsock.SecureDir(filepath.Dir(sock)); err != nil {
		t.Errorf("a shared directory no longer passes SecureDir, so a restart fails: %v", err)
	}

	// a directory that is not the socket's own: nothing changes
	shared := filepath.Join(t.TempDir(), "run")
	other := filepath.Join(shared, "x.sock")
	l2, err := unixsock.Listen(other)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	if err := os.WriteFile(filepath.Join(shared, "lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := unixsock.ShareWithGroup(other, gid); err == nil || !strings.Contains(err.Error(), "directory of the socket's own") {
		t.Fatalf("shared directory: %v", err)
	}
	for p, mode := range map[string]os.FileMode{other: 0o600, shared: 0o700} {
		if fi, err := os.Lstat(p); err != nil || fi.Mode().Perm() != mode {
			t.Errorf("%s changed after a refusal: %v %v", p, fi.Mode(), err)
		}
	}
	if err := unixsock.ShareWithGroup(sock, -1); err == nil {
		t.Error("a negative gid was accepted")
	}
}

// The escaping is systemd-escape's (expected values are its output), so %i
// in zfs-endpoint@.service names the same socket as EndpointAddr.
func TestSystemdEscape(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"tank/k8s/vols": "tank-k8s-vols",
		"tank/my-vols":  `tank-my\x2dvols`,
		"tank/a b":      `tank-a\x20b`,
		"tank/x:y_z.w":  "tank-x:y_z.w",
		"tank/é":        `tank-\xc3\xa9`,
		".x/y":          `\x2ex-y`,
	} {
		if got := unixsock.SystemdEscape(in); got != want {
			t.Errorf("SystemdEscape(%q) = %q, want %q", in, got, want)
		}
	}
	if got := unixsock.EndpointAddr("tank/k8s/vols"); got != "unix:///run/zfs-endpoint/sock/tank-k8s-vols/endpoint.sock" {
		t.Errorf("EndpointAddr = %q", got)
	}
}

// The socket is renamed into place: it reports path, a client connects
// there, Close unlinks it, and no private bind directory is left behind.
func TestListenRenamesIntoPlace(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	path := filepath.Join(dir, "x.sock")
	l, err := unixsock.Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := l.Addr().String(); got != path {
		t.Errorf("Addr %q, want %q", got, path)
	}
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if ents, _ := os.ReadDir(dir); len(ents) != 1 || ents[0].Name() != "x.sock" {
		t.Errorf("directory holds %v, want only x.sock", ents)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("socket still there after Close: %v", err)
	}
}

// A path whose private bind name would not fit sun_path binds through
// /proc/self/fd and still lands 0600 at path.
func TestListenLongPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, strings.Repeat("d", unixsock.MaxPathLen-len(dir)-1))
	if len(path) != unixsock.MaxPathLen {
		t.Fatalf("path is %d bytes, want %d", len(path), unixsock.MaxPathLen)
	}
	l, err := unixsock.Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	fi, err := os.Stat(path)
	if err != nil || fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket %v %v, want 0600", fi.Mode(), err)
	}
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}

// Listen never touches the process umask: directories made beside it, as
// parallel tests' t.TempDir are, keep the mode the umask gives them. With
// a umask around bind, some came out 0600 and unsearchable.
func TestListenLeavesUmaskAlone(t *testing.T) {
	probe := func() os.FileMode {
		d, err := os.MkdirTemp(t.TempDir(), "")
		if err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(d)
		if err != nil {
			t.Fatal(err)
		}
		return fi.Mode().Perm()
	}
	want := probe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 300; i++ {
			l, err := unixsock.Listen(filepath.Join(t.TempDir(), "x.sock"))
			if err != nil {
				t.Error(err)
				return
			}
			l.Close()
		}
	}()
	for {
		select {
		case <-done:
			return
		default:
		}
		if got := probe(); got != want {
			t.Fatalf("a directory made beside Listen is %v, want %v: the umask changed", got, want)
		}
	}
}
