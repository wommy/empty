// Package unixsock opens the daemons' unix sockets.
package unixsock

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Listen listens on the unix socket path, owner-only:
//   - the path must be absolute (CheckPath): "@name" would be a Linux
//     abstract socket, which has no file permissions at all;
//   - its directory is created 0700 if missing; an existing one must pass
//     SecureDir, since whoever can write it can swap the socket;
//   - a socket something still answers on is refused, so two daemons never
//     serve one path;
//   - a stale socket is replaced, but anything that is not a socket (a
//     file, a directory, a symlink) is never deleted: a misconfigured
//     address must not remove what it names;
//   - the socket is created 0600 (umask 0177 around bind), so it is never
//     reachable by others, not even between bind and a chmod.
//
// umask is process-wide: call Listen during startup, before anything else
// creates files.
func Listen(path string) (net.Listener, error) {
	return listen(path, SecureDir)
}

// ListenShared is Listen for a socket in a directory the platform shares
// with the socket's only clients: the CSI socket zfs-csi serves, in the pod
// volume kubelet creates for it (an emptyDir at /csi, which kubelet makes
// 0777) and mounts into this container and its sidecars alone. It differs
// from Listen in one check: the directory may be writable by group and
// others (SharedDir). Everything else holds: a clean absolute path, a real
// directory (not a symlink) owned by this euid or root, never replacing a
// live socket or a non-socket, and a 0600 socket.
//
// Why that is safe for the CSI socket and not for the endpoint socket:
//   - who can write the directory: an emptyDir lives under
//     /var/lib/kubelet/pods/<uid>/volumes/, which only root on the host can
//     traverse; in the pod, only the containers that mount it reach it.
//     Those are the sidecars: the socket's clients, which already hold
//     every right it grants. Swapping the socket gains them nothing.
//   - what it fronts: zfs-csi holds no credential of its own; it forwards to
//     the endpoint socket, which stays in a 0700 directory under Listen. A
//     host user gains no path to the endpoint.
//   - the socket stays 0600, so only this uid connects (the sidecars run
//     as the same uid, root in deploy/k8s).
//
// Never use it for the endpoint socket or for a directory on the host's own
// filesystem: a 0777 directory there lets any local user unlink the socket
// and plant their own.
func ListenShared(path string) (net.Listener, error) {
	return listen(path, SharedDir)
}

func listen(path string, checkDir func(string) error) (net.Listener, error) {
	if err := CheckPath(path); err != nil {
		return nil, err
	}
	if err := checkDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	fi, err := os.Lstat(path)
	switch {
	case os.IsNotExist(err):
	case err != nil:
		return nil, err
	case fi.Mode()&os.ModeSocket == 0:
		return nil, fmt.Errorf("%s exists and is not a socket (%v): refusing to replace it", path, fi.Mode().Type())
	default:
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			c.Close()
			return nil, fmt.Errorf("%s is already being served", path)
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	old := syscall.Umask(0o177)
	l, err := net.Listen("unix", path)
	syscall.Umask(old)
	return l, err
}

// CheckPath accepts only an absolute filesystem path for a socket. Go
// treats a leading '@' as a Linux abstract socket: no inode, so no mode,
// no owner and no directory to protect it, and any local process can
// connect.
func CheckPath(path string) error {
	if strings.HasPrefix(path, "@") {
		return fmt.Errorf("socket %q: abstract sockets have no permissions; use an absolute path", path)
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("socket %q: want a clean absolute path", path)
	}
	return nil
}

// SecureDir creates dir 0700 if it is missing, and refuses an existing one
// another user could tamper with: it must be a real directory (not a
// symlink), owned by this process's euid or by root, and not writable by
// group or others. MkdirAll keeps an existing directory's mode, so without
// the check a pre-made 0777 directory would let anyone unlink the socket
// and plant their own.
func SecureDir(dir string) error {
	fi, err := ownedDir(dir)
	if err != nil {
		return err
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is writable by group or others (%v)", dir, fi.Mode().Perm())
	}
	return nil
}

// SharedDir is SecureDir without the write-permission check, for
// ListenShared only (see there for why). A missing directory is still
// created 0700, and a symlink or a directory another user owns is still
// refused.
func SharedDir(dir string) error {
	_, err := ownedDir(dir)
	return err
}

// ownedDir creates dir 0700 if it is missing, then requires a real
// directory (not a symlink) owned by this process's euid or by root.
func ownedDir(dir string) (os.FileInfo, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%s is not a directory (%v)", dir, fi.Mode().Type())
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, fmt.Errorf("%s: cannot read its owner", dir)
	}
	if euid := os.Geteuid(); int(st.Uid) != euid && st.Uid != 0 {
		return nil, fmt.Errorf("%s is owned by uid %d, not %d or root", dir, st.Uid, euid)
	}
	return fi, nil
}

// EndpointDir is where the endpoint sockets live: the unit's
// RuntimeDirectory (deploy/systemd/zfs-endpoint@.service).
const EndpointDir = "/run/zfs-endpoint"

// EndpointAddr is the one socket convention for an endpoint serving parent:
// unix:///run/zfs-endpoint/<systemd-escape parent>.sock. The unit
// zfs-endpoint@<escaped>.service serves %i.sock, zfs-endpoint's default
// -listen and zfs-csi's -parent derive the same address, and
// deploy_test.go pins all three.
func EndpointAddr(parent string) string {
	return "unix://" + EndpointDir + "/" + SystemdEscape(parent) + ".sock"
}

// SystemdEscape is `systemd-escape <s>` (not --path): '/' becomes '-', a
// byte outside [A-Za-z0-9:_.] (or a leading '.') becomes \xNN. It is what
// %i holds for zfs-endpoint@<escaped>.service.
func SystemdEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '/':
			b.WriteByte('-')
		case c == '.' && i == 0:
			fmt.Fprintf(&b, `\x%02x`, c)
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == ':', c == '_', c == '.':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, `\x%02x`, c)
		}
	}
	return b.String()
}
