package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Item 9 (wave22): an -admin-listen that cannot be served (a regular file
// at its path) fails startup after the -listen socket was made: that
// socket is closed, so its path is gone and a restart can bind it.
func TestAdminListenFailureClosesTheCSISocket(t *testing.T) {
	t.Parallel()
	d, _ := simDeps(t)
	dir := t.TempDir()
	sock := filepath.Join(dir, "sock", "endpoint.sock")
	adminDir := filepath.Join(dir, "admin")
	if err := os.Mkdir(adminDir, 0o700); err != nil {
		t.Fatal(err)
	}
	admin := filepath.Join(adminDir, "endpoint.sock")
	if err := os.WriteFile(admin, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := run(context.Background(), []string{"-parent", "tank/k8s", "-parent-wait", "0",
		"-listen", "unix://" + sock, "-admin-listen", "unix://" + admin}, d)
	if err == nil || !strings.Contains(err.Error(), "is not a socket") {
		t.Fatalf("run: %v, want the admin socket refused", err)
	}
	if _, err := os.Lstat(sock); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("-listen socket left behind: %v", err)
	}
}

// secondSignal's channel closes on the next SIGTERM, which no longer kills
// the process. signal.Notify is process-wide and the parallel run tests
// listen for signals too, so the signal goes to a child: this test binary
// re-run with secondSignalChild set.
func TestSecondSignal(t *testing.T) {
	if os.Getenv("secondSignalChild") != "" {
		again := secondSignal()
		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		select {
		case <-again:
		case <-time.After(time.Minute): // a failure bound only
			t.Fatal("channel not closed by SIGTERM")
		}
		return
	}
	t.Parallel()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSecondSignal$", "-test.v")
	cmd.Env = append(os.Environ(), "secondSignalChild=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: TestSecondSignal") {
		t.Fatalf("child: %v\n%s", err, out)
	}
}
