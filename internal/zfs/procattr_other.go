//go:build !linux

package zfs

import "syscall"

// childAttr: own process group (no Pdeathsig outside Linux).
func childAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
