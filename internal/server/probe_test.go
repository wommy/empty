package server_test

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/wommy/empty/internal/server"
)

// Item 9 (wave22): Probe's loop. A failing check (a suspended pool) turns
// the endpoint NOT_SERVING, a passing one SERVING again, and a check the
// shutdown interrupts (ctx ended during it) changes nothing: its error is
// the shutdown's, not the pool's. The ticker runs in a synctest bubble, so
// each interval passes on the fake clock, with no wall-clock wait.
func TestProbeTransitions(t *testing.T) {
	d := server.NewDaemon(&server.Server{}, slog.New(slog.DiscardHandler))
	d.SetServing(true)
	status := func() healthpb.HealthCheckResponse_ServingStatus {
		t.Helper()
		r, err := d.Health.Check(context.Background(), &healthpb.HealthCheckRequest{Service: server.ServiceName})
		if err != nil {
			t.Fatal(err)
		}
		return r.Status
	}
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		const interval = 30 * time.Second
		var calls atomic.Int32
		check := func(context.Context) error {
			switch calls.Add(1) {
			case 1:
				return errors.New("pool suspended")
			case 2:
				return nil
			default:
				// the shutdown lands while the check runs
				cancel()
				return context.Canceled
			}
		}
		done := make(chan struct{})
		go func() { d.Probe(ctx, interval, check); close(done) }()

		synctest.Wait()
		if calls.Load() != 0 || status() != healthpb.HealthCheckResponse_SERVING {
			t.Fatalf("before the first tick: %d checks, %v", calls.Load(), status())
		}
		for i, want := range []healthpb.HealthCheckResponse_ServingStatus{
			healthpb.HealthCheckResponse_NOT_SERVING,
			healthpb.HealthCheckResponse_SERVING,
			healthpb.HealthCheckResponse_SERVING, // interrupted: unchanged
		} {
			time.Sleep(interval) // the fake clock: one tick
			synctest.Wait()
			if calls.Load() != int32(i+1) || status() != want {
				t.Fatalf("tick %d: %d checks, %v, want %v", i+1, calls.Load(), status(), want)
			}
		}
		<-done // Probe returned on the interrupted check
	})
}
