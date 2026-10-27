package server_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/wommy/empty/gen/zfsendpoint/v1"
	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/server"
	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

// gateRunner answers `zfs get available` once gate is closed, or fails
// when its context ends; it records concurrency and returns.
type gateRunner struct {
	gate                    chan struct{}
	cur, peak, total, ended atomic.Int64
	onCancel                time.Duration // time a killed zfs takes to exit
	// stuck, when set, is a killed zfs that does not exit (D state) until
	// the channel closes: no sleep stands in for "never".
	stuck chan struct{}
}

func (r *gateRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	n := r.cur.Add(1)
	defer r.cur.Add(-1)
	defer r.ended.Add(1)
	r.total.Add(1)
	for p := r.peak.Load(); n > p && !r.peak.CompareAndSwap(p, n); p = r.peak.Load() {
	}
	select {
	case <-r.gate:
	case <-ctx.Done():
		time.Sleep(r.onCancel)
		if r.stuck != nil {
			<-r.stuck
		}
		return nil, &zfs.ExitError{Args: args, Code: -1, Stderr: "signal: terminated"}
	}
	return availableJSON(args[len(args)-1]), nil
}

type daemonClient struct {
	pb.EndpointServiceClient
	health healthpb.HealthClient
	d      *server.Daemon
}

func daemon(t *testing.T, r zfs.Runner, log *slog.Logger) daemonClient {
	t.Helper()
	ep, err := endpoint.New(zfs.New(r), endpoint.Config{Parent: "tank/vols"})
	if err != nil {
		t.Fatal(err)
	}
	d := server.NewDaemon(&server.Server{EP: ep, Parent: "tank/vols"}, log)
	d.SetServing(true)
	lis := bufconn.Listen(1 << 20)
	go d.Serve(lis, server.RoleAdmin)
	t.Cleanup(d.GRPC.Stop)
	conn, err := grpc.NewClient("passthrough:///buf",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(server.MaxMsgSize), grpc.MaxCallSendMsgSize(server.MaxMsgSize)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return daemonClient{pb.NewEndpointServiceClient(conn), healthpb.NewHealthClient(conn), d}
}

// A retry storm forks at most -max-zfs zfs children at once.
func TestConcurrentZFSIsBounded(t *testing.T) {
	t.Parallel()
	r := &gateRunner{gate: make(chan struct{})}
	c := daemon(t, zfs.Limit(r, 4), nil)
	var wg sync.WaitGroup
	for range 300 {
		wg.Add(1)
		go func() { defer wg.Done(); c.GetCapacity(context.Background(), &pb.GetCapacityRequest{}) }()
	}
	// the limit is full (4 children running) and more calls are queued
	// behind it than it admits: then a broken limit would show a 5th
	waitFor(t, func() bool { return r.cur.Load() >= 4 })
	time.Sleep(5 * time.Millisecond)
	close(r.gate)
	wg.Wait()
	if r.total.Load() != 300 || r.peak.Load() > 4 {
		t.Fatalf("%d zfs runs, peak %d concurrent; want 300, peak <= 4", r.total.Load(), r.peak.Load())
	}
}

// Shutdown waits for a call in flight, refuses new ones with Unavailable,
// and reports NOT_SERVING while it drains.
func TestShutdownDrainsInFlightCalls(t *testing.T) {
	t.Parallel()
	r := &gateRunner{gate: make(chan struct{})}
	c := daemon(t, r, nil)
	ctx := context.Background()
	if h, err := c.health.Check(ctx, &healthpb.HealthCheckRequest{Service: server.ServiceName}); err != nil || h.Status != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("health before: %v %v", h, err)
	}
	res := make(chan error, 1)
	go func() { _, err := c.GetCapacity(ctx, &pb.GetCapacityRequest{}); res <- err }()
	waitFor(t, func() bool { return r.cur.Load() != 0 })
	stopped := make(chan struct{})
	go func() { c.d.Shutdown(10*time.Second, time.Second, nil); close(stopped) }()
	// draining turns health NOT_SERVING (or refuses the check) while the
	// call is still in flight
	waitFor(t, func() bool {
		h, err := c.health.Check(ctx, &healthpb.HealthCheckRequest{Service: server.ServiceName})
		return err != nil || h.Status != healthpb.HealthCheckResponse_SERVING
	})
	if _, err := c.GetCapacity(ctx, &pb.GetCapacityRequest{}); status.Code(err) != codes.Unavailable {
		t.Fatalf("new call while draining: %v", err)
	}
	select {
	case <-stopped:
		t.Fatal("shutdown did not wait for the call in flight")
	default:
	}
	close(r.gate)
	if err := <-res; err != nil {
		t.Fatalf("drained call: %v", err)
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not return once idle")
	}
}

// When the drain times out, the calls are cancelled and Shutdown waits
// (bounded) for their handlers, so the process never exits while a zfs
// it just told to stop is still running.
func TestShutdownWaitsForCancelledHandlers(t *testing.T) {
	t.Parallel()
	// the durations are injected and short: what is asserted is the order
	// (Shutdown returns only after the killed zfs has exited), not a time
	r := &gateRunner{gate: make(chan struct{}), onCancel: 20 * time.Millisecond}
	c := daemon(t, r, nil)
	go c.GetCapacity(context.Background(), &pb.GetCapacityRequest{})
	waitFor(t, func() bool { return r.cur.Load() != 0 })
	start := time.Now()
	c.d.Shutdown(10*time.Millisecond, 5*time.Second, nil)
	if r.ended.Load() != 1 {
		t.Fatalf("shutdown returned after %v with the zfs call still running", time.Since(start))
	}

	// and the wait is bounded: a zfs that never exits (D state) is still
	// running when Shutdown gives up after stopWait
	stuck := &gateRunner{gate: make(chan struct{}), stuck: make(chan struct{})}
	c2 := daemon(t, stuck, nil)
	t.Cleanup(func() { close(stuck.stuck) }) // after daemon's, so it runs first
	go c2.GetCapacity(context.Background(), &pb.GetCapacityRequest{})
	waitFor(t, func() bool { return stuck.cur.Load() != 0 })
	start = time.Now()
	c2.d.Shutdown(5*time.Millisecond, 20*time.Millisecond, nil)
	if d := time.Since(start); d > 3*time.Second || stuck.ended.Load() != 0 {
		t.Fatalf("stuck handler held shutdown for %v (ended %d)", d, stuck.ended.Load())
	}
}

// A second signal cuts the drain short.
func TestShutdownInterrupt(t *testing.T) {
	t.Parallel()
	r := &gateRunner{gate: make(chan struct{})}
	c := daemon(t, r, nil)
	go c.GetCapacity(context.Background(), &pb.GetCapacityRequest{})
	waitFor(t, func() bool { return r.cur.Load() != 0 })
	again := make(chan struct{})
	close(again)
	start := time.Now()
	c.d.Shutdown(time.Hour, 5*time.Second, again)
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("interrupt took %v", d)
	}
}

// Every call gets a request id (the caller's, or a minted one), returned
// in the response header and attached to every log line of that call,
// including the audit line of each zfs command that changes state.
func TestRequestIDReachesTheAuditLog(t *testing.T) {
	var buf syncBuffer
	log := slog.New(server.LogHandler(slog.NewJSONHandler(&buf, nil)))
	sim := zfstest.New("tank")
	if err := zfs.New(sim).CreateFilesystem(context.Background(), "tank/vols", nil, false); err != nil {
		t.Fatal(err)
	}
	c := daemon(t, zfs.Audit(sim, log), log)
	prev := slog.Default()
	slog.SetDefault(log)
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-request-id", "req-42")
	var hdr metadata.MD
	if _, err := c.CreateVolume(ctx, &pb.CreateVolumeRequest{Name: "pvc-a", CapacityBytes: 1 << 20}, grpc.Header(&hdr)); err != nil {
		t.Fatal(err)
	}
	if got := hdr.Get("x-request-id"); len(got) != 1 || got[0] != "req-42" {
		t.Fatalf("header %v", got)
	}
	if _, err := c.DeleteVolume(ctx, &pb.DeleteVolumeRequest{VolumeId: "pvc-a"}); err != nil {
		t.Fatal(err)
	}
	var minted metadata.MD
	if _, err := c.GetCapacity(metadata.AppendToOutgoingContext(context.Background(), "x-request-id", `bad id "quoted"`), &pb.GetCapacityRequest{}, grpc.Header(&minted)); err != nil {
		t.Fatal(err)
	}
	if got := minted.Get("x-request-id"); len(got) != 1 || len(got[0]) != 32 {
		t.Fatalf("minted id %v", got)
	}
	logs := buf.String()
	for _, want := range []string{
		`"msg":"zfs exec","argv":["zfs","create"`,
		`"msg":"delete volume","volume_id":"pvc-a","request_id":"req-42"`,
		`"msg":"zfs exec","argv":["zfs","destroy"`,
		`"method":"/zfsendpoint.v1.EndpointService/DeleteVolume","code":"OK"`,
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("log lacks %s", want)
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		if strings.Contains(line, `"zfs exec"`) && !strings.Contains(line, `"request_id":"req-42"`) {
			t.Errorf("audit line without the request id: %s", line)
		}
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// max_entries=0 is paged server-side, and a Reconcile desired set of 100k
// ids fits the receive limit.
func TestMessageSizes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sim := zfstest.New("tank")
	z := zfs.New(sim)
	if err := z.CreateFilesystem(ctx, "tank/vols", nil, false); err != nil {
		t.Fatal(err)
	}
	// the volumes are rows to page through: made directly as owned,
	// committed zvols (what CreateVolume leaves), since n CreateVolumes
	// each scan the parent, which is quadratic in n
	n := server.MaxPageSize + 5
	for i := range n {
		id := fmt.Sprintf("pvc-%05d", i)
		own := map[string]string{
			endpoint.DefaultPropertyPrefix + ":managed_resource":  "true",
			endpoint.DefaultPropertyPrefix + ":csi_volume_name":   id,
			endpoint.DefaultPropertyPrefix + ":provision_success": "true",
		}
		if err := z.CreateVolume(ctx, "tank/vols/"+id, 1<<20, true, own); err != nil {
			t.Fatal(err)
		}
	}
	c := daemon(t, sim, nil)
	seen := 0
	tok := ""
	for pages := 0; ; pages++ {
		r, err := c.ListVolumes(ctx, &pb.ListVolumesRequest{StartingToken: tok})
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Volumes) > server.MaxPageSize {
			t.Fatalf("page of %d", len(r.Volumes))
		}
		seen += len(r.Volumes)
		if tok = r.NextToken; tok == "" {
			if pages != 1 {
				t.Fatalf("%d pages, want 2", pages+1)
			}
			break
		}
	}
	if seen != n {
		t.Fatalf("listed %d of %d", seen, n)
	}
	if r, err := c.ListSnapshots(ctx, &pb.ListSnapshotsRequest{}); err != nil || r.NextToken != "" {
		t.Fatalf("list snapshots: %v %v", r, err)
	}

	ids := make([]string, 100_000)
	for i := range ids {
		ids[i] = fmt.Sprintf("pvc-%08x-1111-2222-3333-444455556666", i)
	}
	if _, err := c.Reconcile(ctx, &pb.ReconcileRequest{DesiredVolumeIds: ids, DryRun: true}); err != nil {
		t.Fatalf("reconcile with 100k desired ids: %v", err)
	}
}

// waitFor polls cond: the observable state a test waits for, never a
// fixed sleep standing in for it.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !cond(); time.Sleep(200 * time.Microsecond) {
		if time.Now().After(deadline) {
			t.Fatal("condition never held")
		}
	}
}

// availableJSON is `zfs get -j -p available name` for a filesystem with
// 1000 bytes available.
func availableJSON(name string) []byte {
	return zfstest.AppendGetJSON(nil, []zfstest.JSONDataset{{Name: name, Type: "filesystem", Createtxg: 1,
		Props: []zfstest.JSONProp{{Name: "available", Value: "1000", Source: "-"}}}})
}
