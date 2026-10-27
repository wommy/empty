package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	pb "github.com/wommy/empty/gen/zfsendpoint/v1"
)

// MaxMsgSize bounds a message either way. A Reconcile desired set of 100k
// volume ids is ~4.2 MB, past gRPC's 4 MiB default; front ends (zfs-csi)
// dial with the same limit.
const MaxMsgSize = 64 << 20

// MaxConcurrentStreams per connection. zfs execs are bounded separately
// (zfs.Limit); this bounds the goroutines and buffered requests queued in
// front of them.
const MaxConcurrentStreams = 64

// ServiceName is the health service name front ends watch.
var ServiceName = pb.EndpointService_ServiceDesc.ServiceName

// Daemon is the gRPC server around a Server, with what running it as a
// daemon needs: grpc health, a request id and one audit line per call,
// and a drain that waits for in-flight calls.
type Daemon struct {
	GRPC   *grpc.Server
	Health *health.Server
	log    *slog.Logger
	calls  inflight
}

// NewDaemon registers s and the health service. Health starts NOT_SERVING;
// call SetServing once the daemon is ready.
func NewDaemon(s *Server, log *slog.Logger) *Daemon {
	if log == nil {
		log = slog.Default()
	}
	d := &Daemon{Health: health.NewServer(), log: log}
	d.GRPC = grpc.NewServer(
		grpc.MaxConcurrentStreams(MaxConcurrentStreams),
		grpc.MaxRecvMsgSize(MaxMsgSize),
		grpc.MaxSendMsgSize(MaxMsgSize),
		grpc.UnaryInterceptor(d.unary),
	)
	pb.RegisterEndpointServiceServer(d.GRPC, s)
	healthpb.RegisterHealthServer(d.GRPC, d.Health)
	d.SetServing(false)
	return d
}

// SetServing sets the health of the endpoint service and of the server as
// a whole (""). Once Shutdown has begun, health stays NOT_SERVING.
func (d *Daemon) SetServing(ok bool) {
	st := healthpb.HealthCheckResponse_NOT_SERVING
	if ok {
		st = healthpb.HealthCheckResponse_SERVING
	}
	d.Health.SetServingStatus("", st)
	d.Health.SetServingStatus(ServiceName, st)
}

// Probe runs check every interval until ctx ends and reflects its result in
// health: a suspended or exported pool turns the endpoint NOT_SERVING.
func (d *Daemon) Probe(ctx context.Context, interval time.Duration, check func(context.Context) error) {
	t := time.NewTicker(interval)
	defer t.Stop()
	last := true
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		err := check(ctx)
		if ctx.Err() != nil {
			return
		}
		if ok := err == nil; ok != last {
			if ok {
				d.log.Info("health: serving")
			} else {
				d.log.Warn("health: not serving", "error", err)
			}
			last = ok
		}
		d.SetServing(err == nil)
	}
}

// Shutdown drains, then stops:
//   - health turns NOT_SERVING (for good) and new calls are refused with
//     Unavailable, so callers retry against the next instance;
//   - in-flight calls get up to drain to finish; a close of interrupt (a
//     second signal) cuts the drain short;
//   - then the server stops, which cancels the remaining calls' contexts:
//     each zfs child gets SIGTERM, then SIGKILL (zfs.LocalRunner);
//   - and Shutdown waits up to stopWait more for those handlers to return,
//     so the process does not exit under a zfs it has just told to stop.
//
// Every call is idempotent, so one cut short is completed by the caller's
// retry.
func (d *Daemon) Shutdown(drain, stopWait time.Duration, interrupt <-chan struct{}) {
	idle := d.calls.drain()
	d.Health.Shutdown()
	graceful := make(chan struct{})
	go func() { d.GRPC.GracefulStop(); close(graceful) }() // GOAWAY; stops accepting
	select {
	case <-idle:
		// the last handler has returned, but its response may still be
		// in flight; GracefulStop also waits for health watches, which
		// never end by themselves, so give it a moment, then cut them
		select {
		case <-graceful:
		case <-time.After(time.Second):
		}
		d.GRPC.Stop()
		return
	case <-interrupt:
		d.log.Warn("second signal: stopping without drain", "in_flight", d.calls.count())
	case <-time.After(drain):
		d.log.Warn("drain timed out: cancelling in-flight calls", "after", drain, "in_flight", d.calls.count())
	}
	// Stop closes the connections, which cancels the calls' contexts, but
	// then blocks until every handler has returned: run it aside, so a
	// handler stuck in an unkillable zfs cannot hold Shutdown past stopWait
	go d.GRPC.Stop()
	select {
	case <-idle:
	case <-time.After(stopWait):
		// a zfs in D state on a suspended pool cannot be killed; its
		// Pdeathsig and systemd's final SIGKILL are all that is left
		d.log.Error("handlers still running after stop: exiting anyway", "after", stopWait, "in_flight", d.calls.count())
	}
}

// unary assigns the request id, refuses calls once draining, counts calls
// in flight, and logs one line per call.
func (d *Daemon) unary(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	id := incomingRequestID(ctx)
	ctx = WithRequestID(ctx, id)
	_ = grpc.SetHeader(ctx, metadata.Pairs(requestIDKey, id))
	if !d.calls.begin() {
		return nil, status.Error(codes.Unavailable, "endpoint is shutting down")
	}
	defer d.calls.end()
	start := time.Now()
	resp, err := handler(ctx, req)
	if !strings.HasPrefix(info.FullMethod, "/grpc.health.") {
		attrs := []any{"method", info.FullMethod, "code", status.Code(err).String(), "duration", time.Since(start)}
		if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
			attrs = append(attrs, "peer", p.Addr.String())
		}
		if err != nil {
			attrs = append(attrs, "error", status.Convert(err).Message())
		}
		d.log.InfoContext(ctx, "rpc", attrs...)
	}
	return resp, err
}

// inflight counts unary calls and, once draining, refuses new ones.
type inflight struct {
	mu       sync.Mutex
	n        int
	draining bool
	idle     chan struct{}
}

func (f *inflight) begin() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.draining {
		return false
	}
	f.n++
	return true
}

func (f *inflight) end() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n--
	if f.draining && f.n == 0 {
		close(f.idle)
	}
}

// drain refuses new calls; the channel closes when none are in flight.
func (f *inflight) drain() <-chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.draining {
		f.draining = true
		f.idle = make(chan struct{})
		if f.n == 0 {
			close(f.idle)
		}
	}
	return f.idle
}

func (f *inflight) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

// request ids

const requestIDKey = "x-request-id"

type requestIDCtx struct{}

// WithRequestID returns ctx carrying the request id.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDCtx{}, id)
}

// RequestID returns the request id in ctx, or "".
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDCtx{}).(string)
	return id
}

// incomingRequestID takes the caller's x-request-id if it is a sane token
// (so a front end can correlate its own logs), or mints one.
func incomingRequestID(ctx context.Context) string {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if v := md.Get(requestIDKey); len(v) == 1 && validRequestID(v[0]) {
			return v[0]
		}
	}
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func validRequestID(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

// LogHandler wraps h so every record logged with a request's context
// carries its request_id.
func LogHandler(h slog.Handler) slog.Handler { return ctxHandler{h} }

type ctxHandler struct{ slog.Handler }

func (h ctxHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := RequestID(ctx); id != "" {
		r.AddAttrs(slog.String("request_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

func (h ctxHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return ctxHandler{h.Handler.WithAttrs(as)}
}

func (h ctxHandler) WithGroup(name string) slog.Handler {
	return ctxHandler{h.Handler.WithGroup(name)}
}
