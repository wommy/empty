package server_test

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/wommy/empty/gen/zfsendpoint/v1"
	"github.com/wommy/empty/internal/server"
)

// deadlineRunner records the deadline of the context each zfs call gets.
type deadlineRunner struct {
	had  atomic.Bool
	left atomic.Int64 // time.Duration to the deadline, when there is one
}

func (r *deadlineRunner) Run(ctx context.Context, _ string, args ...string) ([]byte, error) {
	dl, ok := ctx.Deadline()
	r.had.Store(ok)
	if ok {
		r.left.Store(int64(time.Until(dl)))
	}
	return availableJSON(args[len(args)-1]), nil
}

// H-04: a call whose client set no deadline gets the server's
// (DefaultCallTimeout), so a zfs hung on a suspended pool cannot hold its
// limiter slot, its volume lock and its goroutine until a restart. A
// client's own deadline is kept, never lengthened.
func TestCallWithoutDeadlineGetsOne(t *testing.T) {
	t.Parallel()
	r := &deadlineRunner{}
	c := daemon(t, r, nil)
	if _, err := c.GetCapacity(context.Background(), &pb.GetCapacityRequest{}); err != nil {
		t.Fatal(err)
	}
	if !r.had.Load() {
		t.Fatal("a call with no client deadline reaches zfs with no deadline")
	}
	if left := time.Duration(r.left.Load()); left <= 0 || left > server.DefaultCallTimeout {
		t.Errorf("deadline %v away, want within (0, %v]", left, server.DefaultCallTimeout)
	}

	ctx, cancel := context.WithTimeout(context.Background(), server.DefaultCallTimeout+time.Hour)
	defer cancel()
	if _, err := c.GetCapacity(ctx, &pb.GetCapacityRequest{}); err != nil {
		t.Fatal(err)
	}
	if left := time.Duration(r.left.Load()); left <= server.DefaultCallTimeout {
		t.Errorf("a client deadline %v away was cut to %v", server.DefaultCallTimeout+time.Hour, left)
	}
}

// H-09: the audit fields DeleteVolume and DeleteSnapshot log before
// validation, and the rpc line's error, are clamped: a 64 KiB id is
// neither logged nor returned whole.
func TestLoggedAndReturnedInputIsBounded(t *testing.T) {
	var buf syncBuffer
	log := slog.New(server.LogHandler(slog.NewJSONHandler(&buf, nil)))
	prev := slog.Default()
	slog.SetDefault(log)
	t.Cleanup(func() { slog.SetDefault(prev) })
	c := daemon(t, &deadlineRunner{}, log)
	big := strings.Repeat("v", 64<<10)
	ctx := context.Background()
	if _, err := c.DeleteVolume(ctx, &pb.DeleteVolumeRequest{VolumeId: big}); err != nil {
		t.Fatal(err) // never issued: done
	}
	if _, err := c.DeleteSnapshot(ctx, &pb.DeleteSnapshotRequest{SnapshotId: big}); err != nil {
		t.Fatal(err)
	}
	_, err := c.GetVolume(ctx, &pb.GetVolumeRequest{VolumeId: big + "/"})
	if n := len(status.Convert(err).Message()); err == nil || n > 1024 {
		t.Errorf("GetVolume error %d bytes for a %d-byte id: %.60v", n, len(big), err)
	}
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if len(line) > 4<<10 {
			t.Errorf("log line of %d bytes: %.120s", len(line), line)
		}
	}
	if !strings.Contains(buf.String(), `"msg":"delete volume","volume_id":"vvvv`) {
		t.Errorf("delete volume line lost its id: %.300s", buf.String())
	}
}

// panicOnce panics in its first zfs call (any latent parse bug on a
// handler path), then answers like zfs.
type panicOnce struct{ done atomic.Bool }

func (r *panicOnce) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	if !r.done.Swap(true) {
		var a []string
		_ = a[len(args)] // index out of range
	}
	return availableJSON(args[len(args)-1]), nil
}

// H-05: grpc-go does not recover handler panics, so one would kill the
// daemon, every call in flight and (Pdeathsig) their zfs children. A
// panicking handler returns Internal, is logged with its stack, and the
// daemon keeps serving: the next call succeeds and nothing stays in
// flight (Shutdown's drain finds it idle).
func TestHandlerPanicIsInternalAndTheDaemonServes(t *testing.T) {
	t.Parallel()
	var buf syncBuffer
	c := daemon(t, &panicOnce{}, slog.New(slog.NewJSONHandler(&buf, nil)))
	_, err := c.GetCapacity(context.Background(), &pb.GetCapacityRequest{})
	if status.Code(err) != codes.Internal {
		t.Fatalf("panicking handler: %v, want Internal", err)
	}
	if strings.Contains(status.Convert(err).Message(), "goroutine") {
		t.Errorf("the stack reached the caller: %.200s", status.Convert(err).Message())
	}
	if _, err := c.GetCapacity(context.Background(), &pb.GetCapacityRequest{}); err != nil {
		t.Fatalf("after a panic: %v", err)
	}
	logs := buf.String()
	for _, want := range []string{`"msg":"handler panic"`, `index out of range`, `"stack":"goroutine`, `"code":"Internal"`} {
		if !strings.Contains(logs, want) {
			t.Errorf("log lacks %s", want)
		}
	}
	done := make(chan struct{})
	go func() { c.d.Shutdown(time.Minute, time.Minute, nil); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Shutdown waits for a call that panicked")
	}
}
