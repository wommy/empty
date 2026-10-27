// Command zfs-endpoint serves the ZFS endpoint API on a unix socket (or TCP)
// for one parent dataset on this host.
package main

import (
	"context"
	"errors"
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
	drain := flag.Duration("drain", 30*time.Second, "max time to drain in-flight calls on shutdown")
	lockDir := flag.String("lock-dir", "/run/zfs-endpoint", "directory for the per-parent writer lock")
	flag.Parse()
	if *parent == "" {
		return fmt.Errorf("-parent is required")
	}

	// one endpoint per parent dataset on this host, whatever socket it
	// listens on: two would each hold their own resource locks. Across
	// hosts, exclusivity is the HA layer's job (zpool multihost + fencing;
	// docs/research/gaps/adv-fencing.md).
	unlock, err := lockParent(*lockDir, *parent)
	if err != nil {
		return err
	}
	defer unlock()

	z := zfs.New(zfs.LocalRunner{})
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	// refuse to start against a parent that does not exist: never create it
	if ok, err := z.Exists(ctx, *parent); err != nil {
		return fmt.Errorf("check parent: %w", err)
	} else if !ok {
		return fmt.Errorf("parent dataset %s does not exist", *parent)
	}
	// NFS exports need root: libzfs writes /etc/exports.d and runs exportfs
	// as the caller, and only root mounts. Without it the export calls stay
	// unadvertised and UNIMPLEMENTED.
	root := os.Geteuid() == 0
	ep, err := endpoint.New(z, endpoint.Config{Parent: *parent, PropertyPrefix: *prefix, LockWait: *lockWait, FileExport: root})
	if err != nil {
		return err
	}
	if !root {
		slog.Warn("not running as root: NFS file export disabled")
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
		// short is completed by the caller's retry. A suspended pool can
		// hang zfs uninterruptibly, so the drain is bounded and a second
		// signal stops at once.
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
	slog.Info("serving", "listen", *listen, "parent", *parent)
	return srv.Serve(lis)
}

func listener(addr string) (net.Listener, error) {
	path, ok := strings.CutPrefix(addr, "unix://")
	if !ok {
		return net.Listen("tcp", addr)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// a live socket means another endpoint serves this path; two daemons
	// would each hold their own locks over the same datasets
	if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
		c.Close()
		return nil, fmt.Errorf("%s is already being served", path)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	// owner-only from the moment it exists, until transport auth lands
	// (docs/research/edges2/transport-auth.md); umask is process-wide, and
	// nothing else runs yet
	old := syscall.Umask(0o177)
	l, err := net.Listen("unix", path)
	syscall.Umask(old)
	return l, err
}

// lockParent takes an exclusive, non-blocking flock on a file named after
// the parent dataset. The kernel releases it if the process dies.
func lockParent(dir, parent string) (func(), error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, strings.ReplaceAll(parent, "/", "%")+".lock")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("another endpoint already manages %s (lock %s)", parent, path)
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return func() { f.Close() }, nil
}
