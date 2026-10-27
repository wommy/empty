package zfs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
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
type LocalRunner struct{}

func (LocalRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
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
