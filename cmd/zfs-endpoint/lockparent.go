package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/wommy/empty/internal/unixsock"
)

// lockDir holds the per-parent locks. It is fixed, so two endpoints can
// never miss each other by being given different directories, and local
// (tmpfs), so flock means this host only.
const lockDir = "/run/zfs-endpoint"

// maxNameLen is NAME_MAX: a dataset name may have 255 bytes, its lock
// file name 5 more.
const maxNameLen = 255

// lockParent claims parent's subtree for this process with non-blocking
// flocks on files named after datasets: shared on every proper ancestor,
// exclusive on parent itself. A second endpoint for the same parent, for
// an ancestor (whose subtree contains ours) or for a descendant (inside
// ours) conflicts; siblings only share their ancestors' locks and do not.
// The kernel releases the locks if the process dies.
//
// dir must be local: flock on NFS turns into cluster-wide byte-range
// locks, lost with the lease. The daemon uses the fixed lockDir under
// /run (tmpfs).
func lockParent(dir, parent string) (func(), error) {
	if err := unixsock.SecureDir(dir); err != nil {
		return nil, err
	}
	var files []*os.File
	unlock := func() {
		for _, f := range files {
			f.Close()
		}
	}
	parts := strings.Split(parent, "/")
	for i := range parts {
		name := strings.Join(parts[:i+1], "/")
		how := syscall.LOCK_SH
		if name == parent {
			how = syscall.LOCK_EX
		}
		base := strings.ReplaceAll(name, "/", "%") + ".lock"
		if len(base) > maxNameLen {
			unlock()
			return nil, configErr("parent %s: lock file name %s is %d bytes, over the %d a file name may have", parent, base, len(base), maxNameLen)
		}
		path := filepath.Join(dir, base)
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			unlock()
			return nil, err
		}
		files = append(files, f)
		if err := syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB); err != nil {
			unlock()
			// a conflict is configuration: the other endpoint is still
			// there when systemd restarts this one, so exit 78, which
			// stops the restart loop (systemd itself waits for a stopping
			// instance before it starts the next)
			if errors.Is(err, syscall.EWOULDBLOCK) {
				if name != parent {
					return nil, configErr("another endpoint already manages %s, which contains %s (lock %s)", name, parent, path)
				}
				return nil, configErr("another endpoint already manages %s or a dataset inside it (lock %s)", parent, path)
			}
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
	}
	return unlock, nil
}
