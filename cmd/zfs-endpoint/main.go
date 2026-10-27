// Command zfs-endpoint serves the ZFS endpoint API on a unix socket for one
// parent dataset on this host.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/server"
	"github.com/wommy/empty/internal/unixsock"
	"github.com/wommy/empty/internal/zfs"
)

func main() {
	slog.SetDefault(slog.New(server.LogHandler(slog.NewJSONHandler(os.Stderr, nil))))
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], deps{
		runner:    zfs.LocalRunner{},
		version:   checkVersion,
		lockDir:   lockDir,
		euid:      os.Geteuid(),
		interrupt: secondSignal,
		settle:    endpoint.UdevSettle,
	})
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "zfs-endpoint:", err)
		os.Exit(exitCode(err))
	}
}

// deps are what tests replace.
type deps struct {
	runner zfs.Runner
	// version checks the OpenZFS version (checkVersion); nil skips it, for
	// the simulator, which has no version.
	version func(context.Context, *zfs.Client) error
	lockDir string
	euid    int
	// interrupt returns a channel closed by a second signal; nil: none.
	interrupt func() <-chan struct{}
	// settle is Config.Settle (endpoint.UdevSettle); nil for the
	// simulator, which has no devices.
	settle func(context.Context) error
	// after is the startup backoff timer; nil: time.After. Tests inject
	// it so a parent wait costs no wall time.
	after func(time.Duration) <-chan time.Time
}

const (
	// probeTimeout bounds each startup and health probe: a suspended pool
	// (failmode=wait) blocks zfs indefinitely.
	probeTimeout   = 10 * time.Second
	healthInterval = 30 * time.Second
)

func run(ctx context.Context, args []string, d deps) error {
	fs := flag.NewFlagSet("zfs-endpoint", flag.ContinueOnError)
	parent := fs.String("parent", "", "parent dataset to manage, e.g. tank/k8s/vols (required)")
	listen := fs.String("listen", "unix:///run/zfs-endpoint/endpoint.sock", "unix:///absolute/path to serve on (TCP is refused until transport auth, ADR 0001)")
	prefix := fs.String("property-prefix", endpoint.DefaultPropertyPrefix, "user property namespace (democratic-csi: adopt that driver's volumes)")
	lockWait := fs.Duration("lock-wait", 30*time.Second, "max wait for a busy resource")
	drain := fs.Duration("drain", 30*time.Second, "max time to drain in-flight calls on shutdown")
	stopWait := fs.Duration("stop-wait", 10*time.Second, "after the drain, max wait for cancelled calls (and their zfs) to end")
	parentWait := fs.Duration("parent-wait", 60*time.Second, "at startup, how long to wait for the parent dataset (pool import) before exiting 75")
	maxZFS := fs.Int("max-zfs", 8, "max zfs commands running at once")
	allowNonRoot := fs.Bool("allow-non-root", false, "run without root (delegated zfs allow); filesystem volumes cannot be mounted and NFS export is off")
	nfsMinPrefix := fs.Int("nfs-min-prefix", endpoint.DefaultNFSMinPrefix, "broadest IPv4 client network PublishFileExport accepts (prefix length, 1-32)")
	destroyForeign := fs.Bool("destroy-foreign-snapshots", false, "let DeleteVolume destroy snapshots the endpoint did not make along with their volume")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return configErr("%v", err)
	}
	if fs.NArg() > 0 {
		return configErr("unexpected arguments %q", fs.Args())
	}
	if *parent == "" {
		return configErr("-parent is required")
	}
	if err := validParent(*parent); err != nil {
		return err
	}
	sock, err := socketPath(*listen)
	if err != nil {
		return err
	}
	if *maxZFS < 1 {
		return configErr("-max-zfs must be at least 1")
	}
	// zfs create mounts filesystems and NFS export runs exportfs and
	// writes /etc/exports.d: both need root. Delegated (zfs allow) use is
	// possible but must be asked for.
	root := d.euid == 0
	if !root && !*allowNonRoot {
		return configErr("must run as root (euid is %d); pass -allow-non-root to run with delegated zfs permissions, NFS export off", d.euid)
	}

	// one endpoint per parent subtree on this host, whatever socket it
	// listens on: two would each hold their own resource locks. Across
	// hosts, exclusivity is the HA layer's job (zpool multihost + fencing;
	// docs/research/gaps/adv-fencing.md).
	unlock, err := lockParent(d.lockDir, *parent)
	if err != nil {
		return err
	}
	defer unlock()

	// audit inside the limit: a command is logged when it starts
	z := zfs.New(zfs.Limit(zfs.Audit(d.runner, nil), *maxZFS))
	if d.version != nil {
		if err := d.version(ctx, z); err != nil {
			return err
		}
	}
	// refuse to start against a parent that is not there: never create it
	after := d.after
	if after == nil {
		after = time.After
	}
	if err := waitParent(ctx, z, *parent, *parentWait, after); err != nil {
		return err
	}
	ep, err := endpoint.New(z, endpoint.Config{Parent: *parent, PropertyPrefix: *prefix, LockWait: *lockWait, FileExport: root, DestroyForeignSnapshots: *destroyForeign, NFSMinPrefix: *nfsMinPrefix, Settle: d.settle})
	if err != nil {
		return configErr("%v", err)
	}
	if !root {
		slog.Warn("not running as root: NFS file export disabled")
	}

	// owner-only until transport auth lands (ADR 0001); a live socket
	// means another endpoint serves this path
	lis, err := unixsock.Listen(sock)
	if err != nil {
		return err
	}
	dmn := server.NewDaemon(&server.Server{EP: ep, Parent: *parent}, nil)
	dmn.SetServing(true)
	probeCtx, stopProbe := context.WithCancel(ctx)
	defer stopProbe()
	go dmn.Probe(probeCtx, healthInterval, func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, probeTimeout)
		defer cancel()
		_, err := z.Get(ctx, *parent, "type")
		return err
	})

	served := make(chan error, 1)
	go func() { served <- dmn.GRPC.Serve(lis) }()
	notify("READY=1\nSTATUS=serving " + *parent)
	slog.Info("serving", "listen", *listen, "parent", *parent, "max_zfs", *maxZFS)
	select {
	case err := <-served:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	// drain in-flight operations; each is idempotent, so anything cut
	// short is completed by the caller's retry. A suspended pool can hang
	// zfs uninterruptibly, so the drain is bounded and a second signal
	// stops at once.
	notify("STOPPING=1\nSTATUS=draining")
	slog.Info("shutting down", "drain", *drain)
	stopProbe()
	var again <-chan struct{}
	if d.interrupt != nil {
		again = d.interrupt()
	}
	dmn.Shutdown(*drain, *stopWait, again)
	<-served
	slog.Info("stopped")
	return nil
}

func notify(state string) {
	if err := sdNotify(state); err != nil {
		slog.Warn("sd_notify", "error", err)
	}
}

// secondSignal returns a channel closed by the next SIGINT or SIGTERM.
func secondSignal() <-chan struct{} {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	again := make(chan struct{})
	go func() { <-sig; close(again) }()
	return again
}

// socketPath accepts only unix:///absolute/path. TCP served the
// destructive API in plaintext with no authentication, to anyone who could
// reach the port; it comes back with ADR 0001's mTLS, not before.
func socketPath(addr string) (string, error) {
	path, ok := strings.CutPrefix(addr, "unix://")
	if !ok {
		return "", configErr("-listen %q: only unix:///absolute/path is served; TCP is refused until transport authentication (ADR 0001) exists", addr)
	}
	if err := unixsock.CheckPath(path); err != nil {
		return "", configErr("-listen: %v", err)
	}
	return path, nil
}
