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
	"github.com/wommy/empty/internal/unixsock"
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
	prefix := flag.String("property-prefix", endpoint.DefaultPropertyPrefix, "user property namespace (democratic-csi: adopt that driver's volumes)")
	lockWait := flag.Duration("lock-wait", 30*time.Second, "max wait for a busy resource")
	drain := flag.Duration("drain", 30*time.Second, "max time to drain in-flight calls on shutdown")
	destroyForeign := flag.Bool("destroy-foreign-snapshots", false, "let DeleteVolume destroy snapshots the endpoint did not make along with their volume")
	flag.Parse()
	if *parent == "" {
		return fmt.Errorf("-parent is required")
	}

	// one endpoint per parent dataset on this host, whatever socket it
	// listens on: two would each hold their own resource locks. Across
	// hosts, exclusivity is the HA layer's job (zpool multihost + fencing;
	// docs/research/gaps/adv-fencing.md).
	unlock, err := lockParent(lockDir, *parent)
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
	ep, err := endpoint.New(z, endpoint.Config{Parent: *parent, PropertyPrefix: *prefix, LockWait: *lockWait, FileExport: root, DestroyForeignSnapshots: *destroyForeign})
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
	// owner-only until transport auth lands
	// (docs/research/edges2/transport-auth.md); a live socket means another
	// endpoint serves this path, and two daemons would each hold their own
	// locks over the same datasets
	return unixsock.Listen(path)
}

// lockDir holds the per-parent locks. It is fixed, so two endpoints can
// never miss each other by being given different directories, and local
// (tmpfs), so flock means this host only.
const lockDir = "/run/zfs-endpoint"

// lockParent claims parent's subtree for this process with non-blocking
// flocks on files named after datasets: shared on every proper ancestor,
// exclusive on parent itself. A second endpoint for the same parent, for
// an ancestor (whose subtree contains ours) or for a descendant (inside
// ours) conflicts; siblings only share their ancestors' locks and do not.
// The kernel releases the locks if the process dies.
//
// dir must be local: flock on NFS turns into cluster-wide byte-range
// locks, lost with the lease. The daemon uses the fixed lockDir under
// /run (tmpfs).
func lockParent(dir, parent string) (func(), error) {
	if err := unixsock.SecureDir(dir); err != nil {
		return nil, err
	}
	var files []*os.File
	unlock := func() {
		for _, f := range files {
			f.Close()
		}
	}
	parts := strings.Split(parent, "/")
	for i := range parts {
		name := strings.Join(parts[:i+1], "/")
		how := syscall.LOCK_SH
		if name == parent {
			how = syscall.LOCK_EX
		}
		path := filepath.Join(dir, strings.ReplaceAll(name, "/", "%")+".lock")
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			unlock()
			return nil, err
		}
		files = append(files, f)
		if err := syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB); err != nil {
			unlock()
			if errors.Is(err, syscall.EWOULDBLOCK) {
				if name != parent {
					return nil, fmt.Errorf("another endpoint already manages %s, which contains %s (lock %s)", name, parent, path)
				}
				return nil, fmt.Errorf("another endpoint already manages %s or a dataset inside it (lock %s)", parent, path)
			}
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
	}
	return unlock, nil
}
