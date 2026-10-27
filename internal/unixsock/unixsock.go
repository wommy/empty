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
	if err := CheckPath(path); err != nil {
		return nil, err
	}
	if err := SecureDir(filepath.Dir(path)); err != nil {
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

// Target checks a gRPC target that must name a unix socket by absolute
// path, unix:///absolute/path, and returns the path. Anything else (TCP,
// dns:///, unix://relative, unix:@abstract) is refused.
func Target(addr string) (string, error) {
	path, ok := strings.CutPrefix(addr, "unix://")
	if !ok {
		return "", fmt.Errorf("%q: want unix:///absolute/path", addr)
	}
	if err := CheckPath(path); err != nil {
		return "", err
	}
	return path, nil
}

// SecureDir creates dir 0700 if it is missing, and refuses an existing one
// another user could tamper with: it must be a real directory (not a
// symlink), owned by this process's euid or by root, and not writable by
// group or others. MkdirAll keeps an existing directory's mode, so without
// the check a pre-made 0777 directory would let anyone unlink the socket
// and plant their own.
func SecureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory (%v)", dir, fi.Mode().Type())
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s: cannot read its owner", dir)
	}
	if euid := os.Geteuid(); int(st.Uid) != euid && st.Uid != 0 {
		return fmt.Errorf("%s is owned by uid %d, not %d or root", dir, st.Uid, euid)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is writable by group or others (%v)", dir, fi.Mode().Perm())
	}
	return nil
}
