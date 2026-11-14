package server

import (
	"net"
	"syscall"
)

// peerCred reads SO_PEERCRED from a unix socket connection.
func peerCred(c *net.UnixConn) (uid, gid, pid int, err error) {
	rc, err := c.SyscallConn()
	if err != nil {
		return 0, 0, 0, err
	}
	var cred *syscall.Ucred
	var serr error
	if err := rc.Control(func(fd uintptr) {
		cred, serr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return 0, 0, 0, err
	}
	if serr != nil {
		return 0, 0, 0, serr
	}
	return int(cred.Uid), int(cred.Gid), int(cred.Pid), nil
}
