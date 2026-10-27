package zfs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

// Runner executes a zfs (or zpool) command. It is the seam between the
// endpoint and the host: LocalRunner today, an SSH or libzfs_core runner
// later, a fake in tests.
type Runner interface {
	// Run executes name with args and returns stdout. A non-zero exit is
	// returned as *ExitError carrying stderr.
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
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
	cmd := exec.CommandContext(ctx, name, args...)
	// signal the child's own process group (childAttr sets Setpgid), not
	// just the leader: a grandchild holding a pipe otherwise outlives Run
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.SysProcAttr = childAttr()
	cmd.WaitDelay = r.WaitDelay
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = 5 * time.Second
	}
	var stdout bytes.Buffer
	stderr := headWriter{max: MaxStderr}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil && cmd.Process != nil {
		// WaitDelay's SIGKILL reaches only the leader; reap the rest
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if err != nil && !errors.As(err, new(*exec.ExitError)) {
		// never ran: binary missing, context cancelled before start, ...
		return nil, fmt.Errorf("run %s: %w", name, err)
	}
	if err != nil {
		var ee *exec.ExitError
		code := -1
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		return stdout.Bytes(), &ExitError{
			Args:     append([]string{name}, args...),
			Code:     code,
			Stderr:   stderr.String(),
			Underlay: err,
		}
	}
	return stdout.Bytes(), nil
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
