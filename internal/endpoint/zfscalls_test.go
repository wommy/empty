package endpoint

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/wommy/empty/internal/zfs"
)

// Call accounting for the real-pool tests. On the VM every zfs(8) call is
// a fork, and forks, not ZFS I/O, are the suite's CPU (ADR 0005). With
// ZFS_TEST_CALLS set, CountCalls wraps a test's runner so that each call
// is attributed to its caller:
//
//   - endpoint: the call came from the endpoint's own (non-test) code, the
//     thing under test;
//   - harness: the test or one of its helpers issued it (setup, teardown,
//     verification), named by the innermost _test.go function.
//
// Each wrapped test logs "CALLS harness=<n> endpoint=<m>" when it ends.
// When ZFS_TEST_CALLS is a path rather than "1", every call is also
// appended to it as a tab-separated line (test, class, function, zfs
// subcommand) for aggregation:
//
//	ZFS_TEST_CALLS=$PWD/calls.tsv go test ./internal/endpoint
//	cut -f1 calls.tsv | cut -d/ -f1 | sort | uniq -c | sort -rn | head
//
// Unset, CountCalls returns the runner unchanged.
func CountCalls(tb testing.TB, r zfs.Runner) zfs.Runner {
	if os.Getenv("ZFS_TEST_CALLS") == "" {
		return r
	}
	c := &callCounter{Runner: r, test: tb.Name()}
	tb.Cleanup(func() {
		tb.Logf("CALLS harness=%d endpoint=%d", c.harness.Load(), c.endpoint.Load())
	})
	if s, ok := r.(zfs.Streamer); ok {
		return &streamCounter{callCounter: c, s: s}
	}
	return c
}

type callCounter struct {
	zfs.Runner
	test              string
	harness, endpoint atomic.Int64
}

func (c *callCounter) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	c.count(name, args)
	return c.Runner.Run(ctx, name, args...)
}

type streamCounter struct {
	*callCounter
	s zfs.Streamer
}

func (c *streamCounter) Stream(ctx context.Context, read func(io.Reader) error, name string, args ...string) error {
	c.count(name, args)
	return c.s.Stream(ctx, read, name, args...)
}

var callLog struct {
	sync.Mutex
	f *os.File
}

func (c *callCounter) count(name string, args []string) {
	class, fn := callerOf()
	if class == "endpoint" {
		c.endpoint.Add(1)
	} else {
		c.harness.Add(1)
	}
	path := os.Getenv("ZFS_TEST_CALLS")
	if path == "1" {
		return
	}
	sub := name
	if len(args) > 0 {
		sub += " " + args[0]
	}
	callLog.Lock()
	defer callLog.Unlock()
	if callLog.f == nil {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
		if err != nil {
			panic(err)
		}
		callLog.f = f
	}
	fmt.Fprintf(callLog.f, "%s\t%s\t%s\t%s\n", c.test, class, fn, sub)
}

// callerOf classifies the current call: "endpoint" with the innermost
// endpoint function when the endpoint's non-test code is on the stack,
// else "harness" with the innermost function of a _test.go file.
func callerOf() (class, fn string) {
	pc := make([]uintptr, 64)
	frames := runtime.CallersFrames(pc[:runtime.Callers(3, pc)])
	const pkg = "github.com/wommy/empty/internal/endpoint."
	for {
		f, more := frames.Next()
		test := strings.HasSuffix(f.File, "_test.go")
		if strings.HasPrefix(f.Function, pkg) && !test {
			return "endpoint", strings.TrimPrefix(f.Function, pkg)
		}
		// a runner wrapper (this counter, a fault injector) is not the caller
		wrapper := strings.HasSuffix(f.Function, ").Run") || strings.HasSuffix(f.Function, ").Stream")
		if test && fn == "" && !wrapper {
			fn = f.Function[strings.LastIndex(f.Function, "/")+1:]
		}
		if !more {
			break
		}
	}
	return "harness", fn
}
