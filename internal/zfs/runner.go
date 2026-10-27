package zfs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Limit returns a Runner that runs at most n commands of r at once (n < 1
// means 1). The kernel serialises most zfs ioctls on the pool namespace
// lock anyway, so more children buy no throughput, only PIDs and memory.
// A caller waiting for a slot gives up when its context ends: the error
// wraps ctx.Err(), and the command never started.
func Limit(r Runner, n int) Runner {
	return &limited{r: r, slots: make(chan struct{}, max(n, 1))}
}

type limited struct {
	r     Runner
	slots chan struct{}
}

func (l *limited) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("run %s: %w", name, err)
	}
	select {
	case l.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, fmt.Errorf("run %s: waiting for a free zfs slot: %w", name, ctx.Err())
	}
	defer func() { <-l.slots }()
	return l.r.Run(ctx, name, args...)
}

func (l *limited) Stream(ctx context.Context, read func(io.Reader) error, name string, args ...string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("run %s: %w", name, err)
	}
	select {
	case l.slots <- struct{}{}:
	case <-ctx.Done():
		return fmt.Errorf("run %s: waiting for a free zfs slot: %w", name, ctx.Err())
	}
	defer func() { <-l.slots }()
	return stream(ctx, l.r, read, name, args...)
}

// Runner executes a zfs (or zpool) command. It is the seam between the
// endpoint and the host: LocalRunner today, an SSH or libzfs_core runner
// later, a fake in tests.
type Runner interface {
	// Run executes name with args and returns stdout. A non-zero exit is
	// returned as *ExitError carrying stderr.
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Streamer is a Runner that can hand a command's stdout to a reader while
// the command runs, instead of collecting all of it first: a whole-parent
// scan then holds the decoder's window of the output, not the output.
//
// Stream runs name with args and calls read once with stdout. Whatever
// read leaves unread is drained, and the command is waited for. A
// non-zero exit is returned as *ExitError, as Run returns it, whatever
// read returned: the exit status, not where a decoder gave up, says what
// failed. Otherwise read's error is returned.
type Streamer interface {
	Stream(ctx context.Context, read func(io.Reader) error, name string, args ...string) error
}

// stream runs a command through r and hands its stdout to read: from the
// pipe when r is a Streamer, else from Run's output, which read is not
// called with when Run fails.
func stream(ctx context.Context, r Runner, read func(io.Reader) error, name string, args ...string) error {
	if s, ok := r.(Streamer); ok {
		return s.Stream(ctx, read, name, args...)
	}
	out, err := r.Run(ctx, name, args...)
	if err != nil {
		return err
	}
	// a *bytes.Buffer is parsed in place: jsontext copies any other reader
	return read(bytes.NewBuffer(out))
}

// ExitError is a command that ran and failed.
type ExitError struct {
	Args     []string
	Code     int
	Stderr   string
	Underlay error
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("%v: exit %d: %s", e.Args, e.Code, e.Stderr)
}

func (e *ExitError) Unwrap() error { return e.Underlay }

// LocalRunner runs commands on this host. Arguments are passed as argv,
// never through a shell.
//
// When ctx ends, the child gets SIGTERM, then after WaitDelay SIGKILL, and
// its pipes are closed, so neither a hung zfs (a suspended pool with
// failmode=wait) nor a grandchild holding stdout can block Run forever.
//
// Each child runs in its own process group, so a signal sent to the
// daemon's group (Ctrl-C in a terminal, kill -- -pgid, a cgroup-wide
// SIGTERM) does not cut a zfs operation the shutdown drain is waiting for:
// only the daemon decides when to stop one. On Linux the child also gets
// SIGKILL if the daemon dies first (Pdeathsig), so no zfs outlives it.
type LocalRunner struct {
	// WaitDelay is the grace period between SIGTERM and SIGKILL. Default 5s.
	WaitDelay time.Duration
}

func (r LocalRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := r.command(ctx, name, args...)
	var stdout bytes.Buffer
	stderr := headWriter{max: MaxStderr}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := r.wait(ctx, cmd, cmd.Run(), &stderr)
	if err != nil && !errors.As(err, new(*ExitError)) {
		return nil, err
	}
	return stdout.Bytes(), err
}

// Stream is Run with stdout read from the pipe as zfs writes it (see
// Streamer). The read is bounded like Wait: once ctx ends, read sees an
// error after WaitDelay at the latest, even if a grandchild outside the
// process group still holds the pipe open.
func (r LocalRunner) Stream(ctx context.Context, read func(io.Reader) error, name string, args ...string) error {
	cmd := r.command(ctx, name, args...)
	pr, pw, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("run %s: %w", name, err)
	}
	defer pr.Close()
	// an *os.File goes to the child as is: no copying goroutine, and the
	// parent's write end is closed once the child has its own
	cmd.Stdout = pw
	stderr := headWriter{max: MaxStderr}
	cmd.Stderr = &stderr
	err = cmd.Start()
	pw.Close()
	if err != nil {
		return r.wait(ctx, cmd, err, &stderr)
	}
	stop := context.AfterFunc(ctx, func() { _ = pr.SetReadDeadline(time.Now().Add(cmd.WaitDelay)) })
	rerr := read(pr)
	// a reader that gave up early must not leave zfs blocked on a full
	// pipe: drain it, so zfs exits and its status can be read
	_, _ = io.Copy(io.Discard, pr)
	stop()
	if err := r.wait(ctx, cmd, cmd.Wait(), &stderr); err != nil {
		return err
	}
	return rerr
}

func (r LocalRunner) command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	// signal the child's own process group (childAttr sets Setpgid), not
	// just the leader: a grandchild holding a pipe otherwise outlives Run
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.SysProcAttr = childAttr()
	// classify matches libzfs's English messages: the child never runs in
	// the daemon's locale (exec keeps the last of duplicate keys). C.UTF-8,
	// not C: zfs -j's printer stops at the first byte >= 0x80 under C and
	// still exits 0, so one non-ASCII value under the parent would fail
	// every scan; OpenZFS ships no translations, so messages stay English.
	// A libc without C.UTF-8 falls back to C.
	cmd.Env = append(os.Environ(), "LC_ALL=C.UTF-8", "LANGUAGE=C")
	cmd.WaitDelay = r.WaitDelay
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = 5 * time.Second
	}
	return cmd
}

// wait turns the outcome of Run, Start or Wait into Run's error: nil, an
// *ExitError carrying stderr, or "never ran".
func (r LocalRunner) wait(ctx context.Context, cmd *exec.Cmd, err error, stderr *headWriter) error {
	if ctx.Err() != nil && cmd.Process != nil {
		// WaitDelay's SIGKILL reaches only the leader; reap the rest
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if err == nil {
		return nil
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		// never ran: binary missing, context cancelled before start, ...
		return fmt.Errorf("run %s: %w", cmd.Args[0], err)
	}
	return &ExitError{
		Args:     cmd.Args,
		Code:     ee.ExitCode(),
		Stderr:   stderr.String(),
		Underlay: err,
	}
}

// MaxStderr bounds the stderr a failed command keeps. It goes into the
// error returned to the caller and logged, and a destroy refused for its
// clones lists every one of them. zfs prints the message classify reads
// first, so the head is kept.
const MaxStderr = 8 << 10

// headWriter keeps the first max bytes written to it and counts the rest.
type headWriter struct {
	buf     bytes.Buffer
	max     int
	dropped int
}

func (w *headWriter) Write(p []byte) (int, error) {
	if room := w.max - w.buf.Len(); room > 0 {
		w.buf.Write(p[:min(room, len(p))])
		w.dropped += max(len(p)-room, 0)
	} else {
		w.dropped += len(p)
	}
	return len(p), nil
}

func (w *headWriter) String() string {
	if w.dropped == 0 {
		return w.buf.String()
	}
	return fmt.Sprintf("%s...[%d bytes of stderr truncated]", strings.ToValidUTF8(w.buf.String(), ""), w.dropped)
}
