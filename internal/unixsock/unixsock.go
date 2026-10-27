// Package unixsock opens the daemons' unix sockets.
package unixsock

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Listen listens on the unix socket path, owner-only:
//   - its directory is created 0700 if missing (an existing one is kept);
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
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
