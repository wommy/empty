// Command zfs-endpoint serves the ZFS endpoint API on a unix socket for one
// parent dataset on this host. `zfs-endpoint reconcile` is an operator's
// client of a running endpoint's admin socket (reconcile.go).
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
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/server"
	"github.com/wommy/empty/internal/unixsock"
	"github.com/wommy/empty/internal/version"
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
	// etab is the NFS export table (endpoint.Config.Etab); nil:
	// endpoint.DefaultEtab. Tests pass the simulator.
	etab endpoint.EtabReader
	// stdout receives -version; nil: os.Stdout.
	stdout io.Writer
}

const (
	// probeTimeout bounds each startup and health probe: a suspended pool
	// (failmode=wait) blocks zfs indefinitely.
	probeTimeout   = 10 * time.Second
	healthInterval = 30 * time.Second
)

func run(ctx context.Context, args []string, d deps) error {
	if len(args) > 0 && args[0] == "reconcile" {
		out := d.stdout
		if out == nil {
			out = os.Stdout
		}
		return runReconcile(ctx, args[1:], out)
	}
	fs := flag.NewFlagSet("zfs-endpoint", flag.ContinueOnError)
	parent := fs.String("parent", "", "parent dataset to manage, e.g. tank/k8s/vols (required)")
	listen := fs.String("listen", "", "unix:///absolute/path to serve on (TCP is refused until transport auth, ADR 0001); default "+unixsock.EndpointAddr("<parent>")+", systemd-escaped, as zfs-endpoint@.service serves it")
	adminListen := fs.String("admin-listen", "", "unix:///absolute/path to serve the admin role on (Reconcile, PublishFileExport, UnpublishFileExport; root or the daemon's uid only), in a directory of its own; default none: -listen serves the CSI role only, and those calls are refused. zfs-endpoint@.service serves "+unixsock.EndpointAdminAddr("<parent>"))
	socketGroup := fs.String("socket-group", "", "group name or gid given the -listen socket: the socket becomes 0660 and its directory 0710, so a non-root CSI controller in the group (supplementalGroups) can connect. "+
		"The directory must hold nothing but the socket. Default none: the socket stays 0600, owner only. The admin socket is never shared")
	showVersion := fs.Bool("version", false, "print the version and exit")
	prefix := fs.String("property-prefix", endpoint.DefaultPropertyPrefix, "user property namespace (democratic-csi: adopt that driver's volumes)")
	lockWait := fs.Duration("lock-wait", 30*time.Second, "max wait for a busy resource; 0: try once and never wait (negative is refused)")
	drain := fs.Duration("drain", 30*time.Second, "max time to drain in-flight calls on shutdown")
	stopWait := fs.Duration("stop-wait", 10*time.Second, "after the drain, max wait for cancelled calls (and their zfs) to end")
	parentWait := fs.Duration("parent-wait", 60*time.Second, "at startup, how long to wait for the parent dataset (pool import) before exiting 75")
	maxZFS := fs.Int("max-zfs", 8, "max zfs commands running at once")
	zfsSlow := fs.Duration("zfs-slow", zfs.DefaultSlow, `log a "zfs slow" warning, once, for any zfs command (reads included) still running after this long; 0: never`)
	allowNonRoot := fs.Bool("allow-non-root", false, "run without root (delegated zfs allow); filesystem volumes cannot be mounted and NFS export is off")
	nfsMinPrefix := fs.Int("nfs-min-prefix", endpoint.DefaultNFSMinPrefix, "broadest IPv4 client network PublishFileExport and CreateVolume's nfs_export clients accept (prefix length, 1-32; 0, every IPv4 client, is refused)")
	minFree := fs.Uint64("min-free-bytes", 0, "headroom floor: CreateVolume and ExpandVolume are RESOURCE_EXHAUSTED when the space a new child of the parent gets after what they make or reserve would fall below this many bytes. "+
		"Advisory, not a reservation: concurrent creates and other writers can each pass it and together cross it. Default 0: off")
	destroyForeign := fs.Bool("destroy-foreign-snapshots", false, "let DeleteVolume destroy snapshots the endpoint did not make along with their volume")
	var nfsServers []string
	fs.Func("nfs-server", "an NFS server address (IPv4 or DNS name) clients mount volumes from; repeat for more, the first is the default. "+
		"Advertise stable names (a VIP or DNS name): a volume keeps the address it was created with for its life. Needs root", func(s string) error {
		if err := endpoint.ValidNFSServer(s); err != nil {
			return err
		}
		nfsServers = append(nfsServers, s)
		return nil
	})
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return configErr("%v", err)
	}
	if *showVersion {
		out := d.stdout
		if out == nil {
			out = os.Stdout
		}
		_, err := fmt.Fprintln(out, version.Version)
		return err
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
	sock, err := socketPath(listenAddr(*listen, *parent))
	if err != nil {
		if *listen == "" {
			// the unit's %i.sock: each byte of -parent outside
			// [A-Za-z0-9:_.], '-' included, is \xNN there, 4 bytes
			return configErr("%v (the default socket, named after the systemd-escaped -parent); pass a shorter -listen", err)
		}
		return err
	}
	if err := sockOutsideLocks(sock, d.lockDir); err != nil {
		return err
	}
	adminSock, err := adminSocketPath(*adminListen, sock, d.lockDir)
	if err != nil {
		return err
	}
	gid, err := socketGID(*socketGroup)
	if err != nil {
		return err
	}
	if *maxZFS < 1 {
		return configErr("-max-zfs must be at least 1")
	}
	if *zfsSlow < 0 {
		return configErr("-zfs-slow %v: want 0 (off) or a positive duration", *zfsSlow)
	}
	if *lockWait < 0 {
		return configErr("-lock-wait %v: want 0 (try once, never wait) or a positive wait", *lockWait)
	}
	// endpoint.Config reads 0 as "default /16"; a /0 floor would let
	// PublishFileExport export to every IPv4 client, so 0 is refused
	// rather than given either meaning
	if *nfsMinPrefix < 1 || *nfsMinPrefix > 32 {
		return configErr("-nfs-min-prefix %d: want a prefix length of 1-32", *nfsMinPrefix)
	}
	// zfs create mounts filesystems and NFS export runs exportfs and
	// writes /etc/exports.d: both need root. Delegated (zfs allow) use is
	// possible but must be asked for.
	root := d.euid == 0
	if !root && !*allowNonRoot {
		return configErr("must run as root (euid is %d); pass -allow-non-root to run with delegated zfs permissions, NFS export off", d.euid)
	}
	if !root && len(nfsServers) > 0 {
		return configErr("-nfs-server needs root: NFS export is off without it")
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

	// audit inside the limit: a command is logged when it starts, and its
	// slow-command clock starts once it has a slot
	z := zfs.New(zfs.Limit(zfs.AuditSlow(d.runner, nil, *zfsSlow), *maxZFS))
	if d.version != nil {
		if err := d.version(ctx, z); err != nil {
			return startupErr(ctx, err)
		}
	}
	// refuse to start against a parent that is not there: never create it
	after := d.after
	if after == nil {
		after = time.After
	}
	if err := waitParent(ctx, z, *parent, *parentWait, after); err != nil {
		return startupErr(ctx, err)
	}
	etab := d.etab
	if etab == nil {
		etab = endpoint.DefaultEtab
	}
	mount, err := checkExposure(ctx, z, *parent, etab)
	if err != nil {
		return startupErr(ctx, err)
	}
	warnTraversable(slog.Default(), *parent, mount)
	ep, err := endpoint.New(z, endpoint.Config{Parent: *parent, PropertyPrefix: *prefix, LockWait: configLockWait(*lockWait), FileExport: root, DestroyForeignSnapshots: *destroyForeign, NFSMinPrefix: *nfsMinPrefix, MinFreeBytes: *minFree, NFSServers: nfsServers, Settle: d.settle, Etab: etab})
	if err != nil {
		return configErr("%v", err)
	}
	// an explicit -property-prefix is the operator's choice; the default
	// is checked for an upgrade across its change
	explicitPrefix := false
	fs.Visit(func(f *flag.Flag) { explicitPrefix = explicitPrefix || f.Name == "property-prefix" })
	if !explicitPrefix {
		if err := checkLegacyPrefix(ctx, z, *parent, *prefix); err != nil {
			return startupErr(ctx, err)
		}
	}
	if !root {
		slog.Warn("not running as root: NFS file export disabled")
	}

	// owner-only until transport auth lands (ADR 0001); a live socket
	// means another endpoint serves this path
	lis, err := listenCSI(sock, gid)
	if err != nil {
		return err
	}
	var adminLis net.Listener
	if adminSock != "" {
		if adminLis, err = unixsock.Listen(adminSock); err != nil {
			lis.Close()
			return err
		}
	}
	dmn := server.NewDaemon(&server.Server{EP: ep, Parent: *parent}, nil)
	dmn.SetServing(true)
	probeCtx, stopProbe := context.WithCancel(ctx)
	defer stopProbe()
	go dmn.Probe(probeCtx, healthInterval, healthCheck(z, *parent, etab, slog.Default()))

	// the -listen socket serves the CSI role (what zfs-csi calls); the
	// admin role (Reconcile, NFS export) only on -admin-listen (F2)
	served := make(chan error, 2)
	listeners := 1
	go func() { served <- dmn.Serve(lis, server.RoleCSI) }()
	adminAddr := ""
	if adminLis != nil {
		listeners++
		adminAddr = "unix://" + adminSock
		go func() { served <- dmn.Serve(adminLis, server.RoleAdmin) }()
	}
	notify("READY=1\nSTATUS=serving " + *parent)
	slog.Info("serving", "listen", "unix://"+sock, "admin_listen", adminAddr, "parent", *parent, "max_zfs", *maxZFS, "version", version.Version)
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
	for range listeners {
		<-served
	}
	slog.Info("stopped")
	return nil
}

// startupErr is err from a startup step, or nil once ctx has ended: a
// SIGTERM (systemctl stop) while waiting for the pool or probing zfs is a
// normal stop, whatever the interrupted probe returned. Exit 1 would leave
// the unit failed, and 75 would have Restart=on-failure start it again.
func startupErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		slog.Info("stopped during startup", "error", err)
		return nil
	}
	return err
}

// checkExposure refuses (78) a parent whose mountpoint is under a crossmnt
// NFS export: that line would serve every volume to its clients from
// creation on, whatever the volume's sharenfs (endpoint.ExposedByAncestor,
// F5). Typically `zfs set sharenfs=on` on the pool or an ancestor dataset,
// or a crossmnt line in /etc/exports. Restarting cannot fix it; removing
// the export can. A parent with no mountpoint (none, legacy) mounts no
// volume through it and is not checked.
func checkExposure(ctx context.Context, z *zfs.Client, parent string, etab endpoint.EtabReader) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	mount, x, err := exposure(ctx, z, parent, etab)
	if err != nil {
		return "", err
	}
	if len(x) > 0 {
		return mount, configErr("parent %s (mounted at %s) is under a crossmnt NFS export, which serves every volume to its clients as soon as it is created: %s; "+
			"unshare it (zfs set sharenfs=off on that dataset, or remove the /etc/exports line) and start again", parent, mount, strings.Join(x, "; "))
	}
	return mount, nil
}

// warnTraversable warns at startup when users other than the owner and
// group may search the parent's mountpoint (mode o+x): every volume is
// mounted below it, so any local user can then walk into what NFS
// clients wrote there. setuid=off, exec=off and devices=off (F12) stop
// the host running it from the volume, not a user reading or copying it
// out. Only a warning: the mode is the operator's, and a parent with no
// mountpoint, or one not mounted yet, is not checked.
func warnTraversable(log *slog.Logger, parent, mount string) {
	if !filepath.IsAbs(mount) {
		return
	}
	fi, err := os.Stat(mount)
	if err != nil {
		log.Debug("parent mountpoint not checked for traversal", "mountpoint", mount, "error", err)
		return
	}
	if fi.IsDir() && fi.Mode().Perm()&0o001 != 0 {
		log.Warn("the parent's mountpoint is searchable by every local user, who can then reach the files NFS clients write to the volumes below it; "+
			"chmod o-x it (F12)", "parent", parent, "mountpoint", mount, "mode", fi.Mode().Perm().String())
	}
}

// exposure is the parent's mountpoint and the crossmnt etab lines that
// cover it (endpoint.ExposedByAncestor); none for a parent with no
// mountpoint (none, legacy).
func exposure(ctx context.Context, z *zfs.Client, parent string, etab endpoint.EtabReader) (string, []string, error) {
	ds, err := z.Get(ctx, parent, "mountpoint")
	if err != nil {
		return "", nil, fmt.Errorf("parent %s: reading its mountpoint: %w", parent, err)
	}
	mount := ds.Props["mountpoint"].Value
	if !filepath.IsAbs(mount) {
		return mount, nil, nil
	}
	b, err := etab.ReadEtab(ctx)
	if err != nil {
		return mount, nil, fmt.Errorf("reading the NFS export table: %w", err)
	}
	return mount, endpoint.ExposedByAncestor(b, mount), nil
}

// healthCheck is the health probe's check: the parent must answer zfs
// (a suspended or exported pool turns health NOT_SERVING). It also
// re-runs the startup exposure check (F5): a crossmnt export that came
// to cover the parent after startup is logged as a warning, never
// NOT_SERVING, which would refuse every CSI call, deletes included, for
// an export only an administrator can remove. The warning is logged
// when the covering lines appear or change, and an info line when they
// are gone; between, nothing repeats. Failing to read the exposure
// changes nothing either.
func healthCheck(z *zfs.Client, parent string, etab endpoint.EtabReader, log *slog.Logger) func(context.Context) error {
	var last string
	return func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, probeTimeout)
		defer cancel()
		if _, err := z.Get(ctx, parent, "type"); err != nil {
			return err
		}
		mount, x, err := exposure(ctx, z, parent, etab)
		if err != nil {
			log.Debug("health: exposure not checked", "error", err)
			return nil
		}
		now := strings.Join(x, "; ")
		switch {
		case now == last:
		case now != "":
			log.Warn("health: parent is under a crossmnt NFS export, which serves every volume to its clients; "+
				"unshare it (zfs set sharenfs=off on that dataset, or remove the /etc/exports line)",
				"parent", parent, "mountpoint", mount, "exports", now)
		default:
			log.Info("health: parent is no longer under a crossmnt NFS export", "parent", parent, "mountpoint", mount)
		}
		last = now
		return nil
	}
}

// configLockWait maps -lock-wait (validated: not negative) to
// endpoint.Config.LockWait. Config's zero value means its 30s default, so
// -lock-wait 0 is passed as tryOnce: a lock wait that is not positive
// takes a free lock and refuses a held one at once (Endpoint.lock's
// TryAcquire path).
func configLockWait(w time.Duration) time.Duration {
	if w == 0 {
		return tryOnce
	}
	return w
}

// tryOnce is a non-positive Config.LockWait: try the lock, never wait.
const tryOnce = -time.Nanosecond

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

// listenAddr is -listen, or by default the one socket convention for parent
// (unixsock.EndpointAddr): the path zfs-endpoint@.service serves and
// zfs-csi -parent dials.
func listenAddr(listen, parent string) string {
	if listen == "" {
		return unixsock.EndpointAddr(parent)
	}
	return listen
}

// sockOutsideLocks refuses a socket whose directory is the lock directory
// or contains it. A pod reaches the socket through a hostPath of that
// directory: it would also get every instance's lock files (flock one and
// that endpoint cannot restart) and every other instance's socket (unlink
// it and serve a fake). A directory of its own, under the lock directory
// or anywhere else, is fine (F1).
func sockOutsideLocks(sock, lockDir string) error {
	dir := filepath.Dir(sock)
	rel, err := filepath.Rel(dir, lockDir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return nil
	}
	return configErr("-listen %s: the socket's directory %s is or contains the lock directory %s, and a pod given the socket would get every endpoint's locks and sockets; "+
		"use a directory of its own, as the default does (%s)", sock, dir, lockDir, strings.TrimPrefix(unixsock.EndpointAddr("<parent>"), "unix://"))
}

// adminSocketPath checks -admin-listen ("" for none, the default): a unix
// socket, outside the lock directory, and in a directory other than the
// -listen socket's, which is mounted into the CSI controller pod; a pod
// given that directory would otherwise get the admin socket too (F2).
func adminSocketPath(addr, sock, lockDir string) (string, error) {
	if addr == "" {
		return "", nil
	}
	path, ok := strings.CutPrefix(addr, "unix://")
	if !ok {
		return "", configErr("-admin-listen %q: only unix:///absolute/path is served", addr)
	}
	if err := unixsock.CheckPath(path); err != nil {
		return "", configErr("-admin-listen: %v", err)
	}
	if err := sockOutsideLocks(path, lockDir); err != nil {
		return "", err
	}
	if rel, err := filepath.Rel(filepath.Dir(sock), filepath.Dir(path)); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
		return "", configErr("-admin-listen %s: its directory is or is under the -listen socket's directory %s, which a CSI pod mounts; "+
			"use a directory of its own, as the unit does (%s)", path, filepath.Dir(sock), strings.TrimPrefix(unixsock.EndpointAdminAddr("<parent>"), "unix://"))
	}
	return path, nil
}

// noGroup is socketGID's "no -socket-group".
const noGroup = -1

// socketGID resolves -socket-group, a group name or a numeric gid, to a
// gid; "" is noGroup. An unknown group is a configuration error (78).
func socketGID(group string) (int, error) {
	if group == "" {
		return noGroup, nil
	}
	if n, err := strconv.Atoi(group); err == nil {
		if n < 0 {
			return 0, configErr("-socket-group %d: want a gid of 0 or more", n)
		}
		return n, nil
	}
	g, err := user.LookupGroup(group)
	if err != nil {
		return 0, configErr("-socket-group %q: %v", group, err)
	}
	n, err := strconv.Atoi(g.Gid)
	if err != nil {
		return 0, configErr("-socket-group %q: gid %q is not a number", group, g.Gid)
	}
	return n, nil
}

// listenCSI listens on the -listen socket, owner only (unixsock.Listen),
// and, given a gid, opens it to that group (unixsock.ShareWithGroup, ADR
// 0001 v2 D7). A directory that holds more than the socket is a
// configuration error: the operator chose the path.
func listenCSI(sock string, gid int) (net.Listener, error) {
	lis, err := unixsock.Listen(sock)
	if err != nil || gid == noGroup {
		return lis, err
	}
	if err := unixsock.ShareWithGroup(sock, gid); err != nil {
		lis.Close()
		return nil, configErr("-socket-group: %v", err)
	}
	return lis, nil
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
