// Command zfs-endpoint serves the ZFS endpoint API on a unix socket (or TCP)
// for one parent dataset on this host.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"

	pb "github.com/wommy/empty/gen/zfsendpoint/v1"
	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/server"
	"github.com/wommy/empty/internal/zfs"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "zfs-endpoint:", err)
		os.Exit(1)
	}
}

func run() error {
	parent := flag.String("parent", "", "parent dataset to manage, e.g. tank/k8s/vols (required)")
	listen := flag.String("listen", "unix:///run/zfs-endpoint/endpoint.sock", "unix:///path or host:port")
	prefix := flag.String("property-prefix", "democratic-csi", "user property namespace")
	lockWait := flag.Duration("lock-wait", 30*time.Second, "max wait for a busy resource")
	flag.Parse()
	if *parent == "" {
		return fmt.Errorf("-parent is required")
	}

	z := zfs.New(zfs.LocalRunner{})
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	// refuse to start against a parent that does not exist: never create it
	if ok, err := z.Exists(ctx, *parent); err != nil {
		return fmt.Errorf("check parent: %w", err)
	} else if !ok {
		return fmt.Errorf("parent dataset %s does not exist", *parent)
	}
	ep, err := endpoint.New(z, endpoint.Config{Parent: *parent, PropertyPrefix: *prefix, LockWait: *lockWait})
	if err != nil {
		return err
	}

	lis, err := listener(*listen)
	if err != nil {
		return err
	}
	srv := grpc.NewServer()
	pb.RegisterEndpointServiceServer(srv, &server.Server{EP: ep, Parent: *parent})

	go func() {
		<-ctx.Done()
		// drain in-flight operations; each is idempotent, so anything cut
		// short is completed by the caller's retry
		slog.Info("shutting down")
		srv.GracefulStop()
	}()
	slog.Info("serving", "listen", *listen, "parent", *parent)
	return srv.Serve(lis)
}

func listener(addr string) (net.Listener, error) {
	if path, ok := strings.CutPrefix(addr, "unix://"); ok {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		l, err := net.Listen("unix", path)
		if err != nil {
			return nil, err
		}
		// owner-only until transport auth lands (docs/research/edges2/transport-auth.md)
		return l, os.Chmod(path, 0o600)
	}
	return net.Listen("tcp", addr)
}
