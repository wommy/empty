package zfs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

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
type LocalRunner struct {
	// WaitDelay is the grace period between SIGTERM and SIGKILL. Default 5s.
	WaitDelay time.Duration
}

func (r LocalRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = r.WaitDelay
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = 5 * time.Second
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
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
