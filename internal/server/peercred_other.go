//go:build !linux

package server

import (
	"errors"
	"net"
)

// peerCred: SO_PEERCRED is Linux's; elsewhere no credentials are read.
func peerCred(*net.UnixConn) (uid, gid, pid int, err error) {
	return 0, 0, 0, errors.New("peer credentials are read on Linux only")
}
