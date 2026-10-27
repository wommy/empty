package zfs_test

import (
	"os"
	"syscall"
	"testing"
)

// writeScript writes an executable stand-in for zfs at path. It holds
// syscall.ForkLock from open to close: a parallel test's fork would
// otherwise inherit the write descriptor, and exec of the script would
// fail with ETXTBSY until that child execs (golang/go#22315).
func writeScript(t testing.TB, path, body string) {
	t.Helper()
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatal(err)
	}
}
