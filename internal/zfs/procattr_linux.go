package zfs

import "syscall"

// childAttr: own process group, and SIGKILL when the daemon dies.
//
// Pdeathsig fires when the thread that forked the child exits, not the
// process (golang/go#27505). Go ends an OS thread only when a goroutine
// exits while locked to it; nothing in the daemon calls
// runtime.LockOSThread, so in practice it fires when the daemon dies.
func childAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}
