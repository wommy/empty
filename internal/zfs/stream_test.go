package zfs_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/wommy/empty/internal/zfs"
)

// LocalRunner.Stream hands stdout over as zfs writes it: the reader sees
// the first line while the command is still running, which a buffered Run
// could never show.
func TestStreamReadsWhileTheCommandRuns(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var got []byte
	err := zfs.LocalRunner{WaitDelay: 100 * time.Millisecond}.Stream(ctx, func(r io.Reader) error {
		got = make([]byte, len("first\n"))
		_, err := io.ReadFull(r, got)
		// the command runs until cancelled: only a stream gets here
		cancel()
		return err
	}, "sh", "-c", "echo first; exec sleep 30")
	if string(got) != "first\n" {
		t.Fatalf("reader saw %q before the command ended, want %q", got, "first\n")
	}
	if err == nil {
		t.Fatal("a cancelled command reported success")
	}
}

// A non-zero exit wins over the reader's verdict, a reader's error is
// returned for a clean exit, and a reader that stops early does not
// leave zfs blocked on a full pipe.
func TestStreamExitStatusAndReaderError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bad := errors.New("reader refused")
	stop := func(io.Reader) error { return bad }
	// 1 MiB, far past a pipe's buffer, then exit 0
	err := zfs.LocalRunner{}.Stream(ctx, stop, "sh", "-c", "head -c 1048576 /dev/zero")
	if !errors.Is(err, bad) {
		t.Fatalf("clean exit: %v, want the reader's error", err)
	}
	err = zfs.LocalRunner{}.Stream(ctx, stop, "sh", "-c", "head -c 1048576 /dev/zero; echo 'cannot open: dataset does not exist' >&2; exit 1")
	var ee *zfs.ExitError
	if !errors.As(err, &ee) || ee.Code != 1 || !strings.Contains(ee.Stderr, "does not exist") || ee.Args[0] != "sh" {
		t.Fatalf("exit 1: %v, want an ExitError with stderr", err)
	}
	var all []byte
	err = zfs.LocalRunner{}.Stream(ctx, func(r io.Reader) (err error) {
		all, err = io.ReadAll(r)
		return err
	}, "sh", "-c", "printf 'a\\nb\\n'")
	if err != nil || string(all) != "a\nb\n" {
		t.Fatalf("read %q, %v", all, err)
	}
	if err := (zfs.LocalRunner{}).Stream(ctx, stop, filepath.Join(t.TempDir(), "missing")); err == nil || errors.As(err, &ee) {
		t.Fatalf("missing binary: %v, want a never-ran error", err)
	}
}

// A grandchild that left the process group (setsid) keeps stdout open
// after the group is killed: the read still ends WaitDelay after the
// context does, as Run's Wait does.
func TestStreamReadIsBoundedAfterCancel(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("no setsid")
	}
	pidfile := filepath.Join(t.TempDir(), "pid")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := zfs.LocalRunner{WaitDelay: 100 * time.Millisecond}.Stream(ctx, func(r io.Reader) error {
		_, err := io.ReadAll(r)
		return err
	}, "sh", "-c", `setsid sleep 30 & echo $! >"$0"; wait`, pidfile)
	if b, rerr := os.ReadFile(pidfile); rerr == nil {
		if pid, _ := strconv.Atoi(strings.TrimSpace(string(b))); pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("Stream took %v", took)
	}
	if err == nil {
		t.Fatal("a cancelled command reported success")
	}
}

// streamer records whether it was asked to stream.
type streamer struct {
	streamed bool
	out      []byte
}

func (s *streamer) Run(context.Context, string, ...string) ([]byte, error) { return s.out, nil }

func (s *streamer) Stream(_ context.Context, read func(io.Reader) error, _ string, _ ...string) error {
	s.streamed = true
	return read(bytes.NewReader(s.out))
}

// Limit and Audit keep the stream: the daemon's runner is
// Limit(Audit(LocalRunner)), and a wrapper that only had Run would buffer
// every scan again.
func TestWrappersForwardStream(t *testing.T) {
	t.Parallel()
	s := &streamer{out: golden(t, "basic.json")}
	r := zfs.Limit(zfs.Audit(s, slog.New(slog.DiscardHandler)), 1)
	if _, err := zfs.New(r).ListColumns(context.Background(), "tank/csi", zfs.ListOptions{Props: basicProps}); err != nil {
		t.Fatal(err)
	}
	if !s.streamed {
		t.Fatal("Limit(Audit(r)) ran the scan with Run, not Stream")
	}
}
