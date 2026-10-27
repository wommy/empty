// Command zfs-csi serves the CSI Identity and Controller services on a unix
// socket, translating every call onto a zfs-endpoint over its gRPC socket.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/wommy/empty/gen/zfsendpoint/v1"
	zcsi "github.com/wommy/empty/internal/csi"
	"github.com/wommy/empty/internal/server"
	"github.com/wommy/empty/internal/unixsock"
	"github.com/wommy/empty/internal/version"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "zfs-csi:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("zfs-csi", flag.ContinueOnError)
	parent := fs.String("parent", "", "parent dataset the endpoint serves, e.g. tank/k8s/vols: dials "+unixsock.EndpointAddr("<parent>")+" (systemd-escaped, as zfs-endpoint@.service serves it)")
	endpointAddr := fs.String("endpoint", "", "zfs-endpoint socket (unix:///absolute/path), instead of -parent")
	csiAddr := fs.String("csi-address", "unix:///csi/csi.sock", "CSI socket to serve (unix:///path), in the pod's emptyDir")
	name := fs.String("driver-name", zcsi.DefaultDriverName, "CSI driver name reported by GetPluginInfo")
	drain := fs.Duration("drain", 30*time.Second, "max time to drain in-flight calls on shutdown")
	enableBlock := fs.Bool("enable-block", false, "provision raw block volumes (zvols); off until a node plugin can attach them (ADR 0006)")
	showVersion := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *showVersion {
		_, err := fmt.Fprintln(stdout, version.Version)
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments %q", fs.Args())
	}
	addr, err := endpointAddress(*parent, *endpointAddr)
	if err != nil {
		return err
	}
	*endpointAddr = addr

	// the endpoint serves only a unix socket until transport auth (ADR
	// 0001); an "@name" abstract socket would have no permissions at all
	if err := endpointTarget(*endpointAddr); err != nil {
		return err
	}
	conn, err := grpc.NewClient(*endpointAddr, dialOptions()...)
	if err != nil {
		return fmt.Errorf("endpoint %s: %w", *endpointAddr, err)
	}
	defer conn.Close()
	ep := pb.NewEndpointServiceClient(conn)

	lis, err := listen(*csiAddr)
	if err != nil {
		return err
	}
	srv := grpc.NewServer()
	csi.RegisterIdentityServer(srv, &zcsi.Identity{EP: ep, Name: *name})
	csi.RegisterControllerServer(srv, &zcsi.Controller{EP: ep, EnableBlock: *enableBlock})

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	go func() {
		<-ctx.Done()
		// a call stuck on a hung endpoint holds the drain; it is bounded,
		// and a second signal stops at once
		slog.Info("shutting down")
		again := make(chan os.Signal, 1)
		signal.Notify(again, syscall.SIGINT, syscall.SIGTERM)
		done := make(chan struct{})
		go func() { srv.GracefulStop(); close(done) }()
		select {
		case <-done:
		case <-again:
			slog.Warn("second signal: stopping without drain")
			srv.Stop()
		case <-time.After(*drain):
			slog.Warn("drain timed out: stopping", "after", *drain)
			srv.Stop()
		}
	}()
	slog.Info("serving", "csi", *csiAddr, "endpoint", *endpointAddr, "driver", *name, "enable_block", *enableBlock, "version", version.Version)
	return srv.Serve(lis)
}

// endpointAddress picks the endpoint socket: -parent derives the one
// convention (unixsock.EndpointAddr, what zfs-endpoint@.service serves),
// -endpoint names a socket outright. Exactly one is required: a default
// would name some other parent's endpoint, or none.
func endpointAddress(parent, endpoint string) (string, error) {
	switch {
	case parent != "" && endpoint != "":
		return "", fmt.Errorf("-parent and -endpoint both set: give one")
	case parent != "":
		return unixsock.EndpointAddr(parent), nil
	case endpoint != "":
		return endpoint, nil
	}
	return "", fmt.Errorf("-parent (or -endpoint) is required: the endpoint for parent P serves %s", unixsock.EndpointAddr("P"))
}

// endpointTarget accepts only unix:///absolute/path (the endpoint serves
// no TCP until ADR 0001); unixsock.Target is the check, tested there.
func endpointTarget(addr string) error {
	if _, err := unixsock.Target(addr); err != nil {
		return fmt.Errorf("-endpoint: %w", err)
	}
	return nil
}

// dialOptions for the endpoint connection:
//   - reconnect backoff capped at 5s (gRPC's default grows to 120s, so
//     after an endpoint outage of a few minutes, e.g. a slow pool import,
//     calls kept failing Unavailable for up to 2 minutes after it was
//     back);
//   - message limits matching the endpoint's (server.MaxMsgSize), so a
//     large Reconcile desired set or list page is never refused here.
func dialOptions() []grpc.DialOption {
	return []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff:           backoff.Config{BaseDelay: 200 * time.Millisecond, Multiplier: 1.6, Jitter: 0.2, MaxDelay: 5 * time.Second},
			MinConnectTimeout: 5 * time.Second,
		}),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(server.MaxMsgSize),
			grpc.MaxCallSendMsgSize(server.MaxMsgSize),
		),
	}
}

// listen opens the CSI socket owner-only (the sidecars share the pod's
// user), replacing a stale one left by a previous run. Its directory is the
// emptyDir kubelet shares between this container and the sidecars, which
// kubelet creates 0777, so it is opened with unixsock.ListenShared: only
// the directory mode check is relaxed, and only for this socket (see
// ListenShared for why that is safe). The endpoint socket keeps the strict
// check in zfs-endpoint.
func listen(addr string) (net.Listener, error) {
	path, ok := strings.CutPrefix(addr, "unix://")
	if !ok {
		return nil, fmt.Errorf("-csi-address %q: want unix:///path", addr)
	}
	return unixsock.ListenShared(path)
}
