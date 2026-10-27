// Package leakcheck fails a test binary that ends with goroutines it did not
// start with: a leaked gRPC server, a parked runner never released, a zfs
// child whose Wait never returned. It is goleak's idea without the
// dependency: stacks are compared by their top function after m.Run.
package leakcheck

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// ignored are goroutines the runtime, testing or a library keeps for the
// life of the process.
var ignored = []string{
	"testing.(*M).",
	"testing.tRunner",
	"testing.runFuzzing",
	"runtime.goexit",
	"os/signal.signal_recv",
	"internal/poll.runtime_pollWait", // the test binary's own stdio
	"google.golang.org/grpc/internal/grpcsync.(*CallbackSerializer).run", // grpc global
	"github.com/onsi/ginkgo/v2/internal/interrupt_handler",
}

func goroutines() []string {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	var out []string
	for _, g := range strings.Split(string(buf), "\n\n") {
		if strings.HasPrefix(g, "goroutine ") && !strings.Contains(g, "leakcheck.goroutines") {
			keep := true
			for _, ig := range ignored {
				if strings.Contains(g, ig) && !strings.Contains(g, "created by github.com/wommy") {
					keep = false
				}
			}
			if keep {
				out = append(out, g)
			}
		}
	}
	return out
}

// Main runs the tests, then waits up to 2s for every goroutine a test
// started to end; any left is reported and fails the binary.
func Main(m *testing.M) {
	code := m.Run()
	if code == 0 {
		var left []string
		for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(20 * time.Millisecond) {
			if left = goroutines(); len(left) == 0 || time.Now().After(deadline) {
				break
			}
		}
		if len(left) > 0 {
			fmt.Fprintf(os.Stderr, "leakcheck: %d goroutine(s) outlived the tests:\n\n%s\n", len(left), strings.Join(left, "\n\n"))
			code = 1
		}
	}
	os.Exit(code)
}
