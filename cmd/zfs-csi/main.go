// Command zfs-csi serves the CSI Identity and Controller services on a unix
// socket, translating every call onto a zfs-endpoint over its gRPC socket.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/wommy/empty/gen/zfsendpoint/v1"
	zcsi "github.com/wommy/empty/internal/csi"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "zfs-csi:", err)
		os.Exit(1)
	}
}

func run() error {
	endpointAddr := flag.String("endpoint", "unix:///run/zfs-endpoint/endpoint.sock", "zfs-endpoint gRPC address (unix:///path or host:port)")
	csiAddr := flag.String("csi-address", "unix:///csi/csi.sock", "CSI socket to serve (unix:///path)")
	name := flag.String("driver-name", zcsi.DefaultDriverName, "CSI driver name reported by GetPluginInfo")
	drain := flag.Duration("drain", 30*time.Second, "max time to drain in-flight calls on shutdown")
	flag.Parse()

	conn, err := grpc.NewClient(*endpointAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
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
	csi.RegisterControllerServer(srv, &zcsi.Controller{EP: ep})

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	go func() {
		<-ctx.Done()
		slog.Info("shutting down")
		done := make(chan struct{})
		go func() { srv.GracefulStop(); close(done) }()
		select {
		case <-done:
		case <-time.After(*drain):
			slog.Warn("drain timed out: stopping", "after", *drain)
			srv.Stop()
		}
	}()
	slog.Info("serving", "csi", *csiAddr, "endpoint", *endpointAddr, "driver", *name)
	return srv.Serve(lis)
}

// listen opens the CSI socket, replacing a stale one left by a previous run.
func listen(addr string) (net.Listener, error) {
	path, ok := strings.CutPrefix(addr, "unix://")
	if !ok {
		return nil, fmt.Errorf("-csi-address %q: want unix:///path", addr)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
		c.Close()
		return nil, fmt.Errorf("%s is already being served", path)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return net.Listen("unix", path)
}
