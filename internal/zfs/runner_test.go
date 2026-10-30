package zfs_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/wommy/empty/internal/zfs"
)

// peakRunner blocks each call until release is closed and records the
// peak number of concurrent calls.
type peakRunner struct {
	cur, peak, total atomic.Int64
	release          chan struct{}
}

func (r *peakRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	n := r.cur.Add(1)
	defer r.cur.Add(-1)
	r.total.Add(1)
	for p := r.peak.Load(); n > p && !r.peak.CompareAndSwap(p, n); p = r.peak.Load() {
	}
	select {
	case <-r.release:
	case <-time.After(time.Millisecond):
	}
	return nil, nil
}

func TestLimitBoundsConcurrentCommands(t *testing.T) {
	t.Parallel()
	synctest.Test(t, limitBoundsConcurrentCommands)
}

func limitBoundsConcurrentCommands(t *testing.T) {
	r := &peakRunner{release: make(chan struct{})}
	l := zfs.Limit(r, 4)
	var wg sync.WaitGroup
	for range 200 {
		wg.Add(1)
		go func() { defer wg.Done(); l.Run(context.Background(), "zfs", "get") }()
	}
	wg.Wait()
	if r.total.Load() != 200 || r.peak.Load() > 4 {
		t.Fatalf("ran %d, peak %d; want 200 runs, peak <= 4", r.total.Load(), r.peak.Load())
	}
}

// A caller waiting for a slot gives up with its context, and the command
// never starts; through the Client that is an interrupted call carrying
// the context's error, like a killed one.
func TestLimitWaiterRespectsContext(t *testing.T) {
	t.Parallel()
	synctest.Test(t, limitWaiterRespectsContext)
}

func limitWaiterRespectsContext(t *testing.T) {
	r := &peakRunner{release: make(chan struct{})}
	block := blockRunner{r: r, started: make(chan struct{}, 1), unblock: make(chan struct{})}
	l := zfs.Limit(block, 1)
	go l.Run(context.Background(), "zfs", "get")
	<-block.started
	defer close(block.unblock)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	c := &zfs.Client{Runner: l, Binary: "zfs"}
	start := time.Now()
	_, err := c.Get(ctx, "tank", "type")
	if time.Since(start) > 2*time.Second {
		t.Fatalf("waited %v", time.Since(start))
	}
	if !errors.Is(err, zfs.ErrInterrupted) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want interrupted by deadline", err)
	}
	if r.total.Load() != 0 {
		t.Fatalf("%d commands ran past the limit", r.total.Load())
	}

	done, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if _, err := zfs.Limit(r, 1).Run(done, "zfs", "get"); !errors.Is(err, context.Canceled) {
		t.Fatalf("done ctx on a free slot: %v", err)
	}
}

type blockRunner struct {
	r       zfs.Runner
	started chan struct{}
	unblock chan struct{}
}

func (b blockRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	b.started <- struct{}{}
	<-b.unblock
	return nil, nil
}

// A signal sent to the daemon's process group must not reach its zfs
// children: each runs in a group of its own.
func TestChildRunsInItsOwnProcessGroup(t *testing.T) {
	t.Parallel()
	bin := filepath.Join(t.TempDir(), "zfs")
	writeScript(t, bin, "#!/bin/sh\necho $$ $(cut -d' ' -f5 /proc/$$/stat)\n")
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no /proc")
	}
	out, err := zfs.LocalRunner{}.Run(context.Background(), bin)
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Fields(string(out))
	if len(f) != 2 {
		t.Fatalf("output %q", out)
	}
	pid, _ := strconv.Atoi(f[0])
	pgid, _ := strconv.Atoi(f[1])
	if pgid == syscall.Getpgrp() || pgid != pid {
		t.Fatalf("child pid %d pgid %d, daemon pgid %d: want a group of its own", pid, pgid, syscall.Getpgrp())
	}
}

// A cancelled call takes the child's whole process group with it: a
// grandchild (here a backgrounded sleep) must not outlive Run.
func TestCancelKillsTheChildsGrandchildren(t *testing.T) {
	t.Parallel()
	pidfile := filepath.Join(t.TempDir(), "pid")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := zfs.LocalRunner{WaitDelay: 100 * time.Millisecond}.Run(ctx, "sh", "-c",
			`sleep 30 & echo $! >"$0"; wait`, pidfile)
		done <- err
	}()
	var pid int
	for deadline := time.Now().Add(5 * time.Second); pid == 0; {
		if b, err := os.ReadFile(pidfile); err == nil && strings.HasSuffix(string(b), "\n") {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
		if time.Now().After(deadline) {
			t.Fatal("grandchild never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("cancelled call reported success")
	}
	// gone, or a zombie waiting for init to reap it
	for deadline := time.Now().Add(5 * time.Second); ; {
		b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil || strings.Contains(string(b), ") Z ") {
			return
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("grandchild %d outlived the cancelled call", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
