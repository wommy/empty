package endpoint_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/zfs"
)

// NFS skips a test that needs exports to really happen, unless it runs on
// the simulator or an NFS server is running on this host.
func (h *harness) NFS(t *testing.T) {
	t.Helper()
	if h.sim != nil {
		return
	}
	b, err := os.ReadFile("/proc/fs/nfsd/threads")
	if n, _ := strconv.Atoi(strings.TrimSpace(string(b))); err != nil || n == 0 {
		t.Skip("needs a running NFS server (nfsd threads > 0)")
	}
}

func (h *harness) etab() endpoint.EtabReader {
	if h.sim != nil {
		return h.sim
	}
	return endpoint.DefaultEtab
}

// served reports how the kernel exports path to host.
func (h *harness) served(t *testing.T, path, host string) (endpoint.NFSClient, bool) {
	t.Helper()
	b, err := h.etab().ReadEtab(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// exportfs stores the real path
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	return endpoint.ParseEtab(b).Lookup(path, host)
}

// servedHosts lists every etab client of path as written (names, "*").
func (h *harness) servedHosts(t *testing.T, path string) []string {
	t.Helper()
	b, err := h.etab().ReadEtab(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	var hosts []string
	for _, c := range endpoint.ParseEtab(b)[path] {
		hosts = append(hosts, c.Host)
	}
	return hosts
}

func (h *harness) propSource(t *testing.T, name, prop string) (string, string) {
	t.Helper()
	d, err := h.Z.Get(context.Background(), name, prop)
	if err != nil {
		t.Fatal(err)
	}
	return d.Props[prop].Value, d.Props[prop].Source
}

func sets(h *harness, from int) int {
	if h.sim == nil {
		return 0
	}
	n := 0
	for _, c := range h.sim.Calls[from:] {
		if c[1] == "set" {
			n++
		}
	}
	return n
}

func TestFileExportLifecycle(t *testing.T) {
	ctx := context.Background()
	ep, h := setup(t)
	h.NFS(t)
	v, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "pvc-nfs", CapacityBytes: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}
	ds := v.Dataset

	path, err := ep.PublishFileExport(ctx, "pvc-nfs", "10.0.0.1", false)
	if err != nil || path != v.Mountpoint {
		t.Fatalf("publish: %q %v (mountpoint %q)", path, err, v.Mountpoint)
	}
	if val, src := h.propSource(t, ds, "sharenfs"); val != "rw=10.0.0.1/32" || src != "local" {
		t.Fatalf("sharenfs %q (%s)", val, src)
	}
	if c, ok := h.served(t, path, "10.0.0.1"); !ok || c.ReadOnly {
		t.Fatalf("not served rw: %v %v", c, ok)
	}

	// idempotent: nothing is set again
	var calls int
	if h.sim != nil {
		calls = len(h.sim.Calls)
	}
	if p2, err := ep.PublishFileExport(ctx, "pvc-nfs", "10.0.0.1", false); err != nil || p2 != path {
		t.Fatalf("repeat publish: %q %v", p2, err)
	}
	if n := sets(h, calls); n != 0 {
		t.Fatalf("repeat publish ran %d zfs set", n)
	}

	if _, err := ep.PublishFileExport(ctx, "pvc-nfs", "10.9.0.0/16", true); err != nil {
		t.Fatal(err)
	}
	if val := h.Prop(ds, "sharenfs"); val != "rw=10.0.0.1/32,ro=10.9.0.0/16" {
		t.Fatalf("sharenfs %q", val)
	}
	_, err = ep.PublishFileExport(ctx, "pvc-nfs", "10.0.0.1", true)
	wantCode(t, err, endpoint.AlreadyExists)

	if err := ep.UnpublishFileExport(ctx, "pvc-nfs", "10.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.served(t, path, "10.0.0.1"); ok {
		t.Fatal("still served after unpublish")
	}
	if c, ok := h.served(t, path, "10.9.0.0/16"); !ok || !c.ReadOnly {
		t.Fatalf("other client lost: %v %v", c, ok)
	}
	if err := ep.UnpublishFileExport(ctx, "pvc-nfs", "10.0.0.1"); err != nil {
		t.Fatalf("repeat unpublish: %v", err)
	}
	// the last client leaves sharenfs=off set locally, never inherited
	if err := ep.UnpublishFileExport(ctx, "pvc-nfs", "10.9.0.0/16"); err != nil {
		t.Fatal(err)
	}
	if val, src := h.propSource(t, ds, "sharenfs"); val != "off" || src != "local" {
		t.Fatalf("sharenfs %q (%s)", val, src)
	}
	if err := ep.DeleteVolume(ctx, "pvc-nfs"); err != nil {
		t.Fatal(err)
	}
	if err := ep.UnpublishFileExport(ctx, "pvc-nfs", "10.0.0.1"); err != nil {
		t.Fatalf("unpublish of a deleted volume: %v", err)
	}
}

func TestFileExportPreconditions(t *testing.T) {
	ctx := context.Background()
	ep, h := setup(t)
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "blk", Kind: endpoint.Block, CapacityBytes: 1 << 20, Sparse: true}); err != nil {
		t.Fatal(err)
	}
	_, err := ep.PublishFileExport(ctx, "blk", "10.0.0.1", false)
	wantCode(t, err, endpoint.FailedPrecondition)
	if err := ep.UnpublishFileExport(ctx, "blk", "10.0.0.1"); err != nil {
		t.Fatalf("unpublish zvol: %v", err)
	}

	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "fs"}); err != nil {
		t.Fatal(err)
	}
	if err := h.Z.Set(ctx, h.Parent+"/fs", map[string]string{"mountpoint": "none"}); err != nil {
		t.Fatal(err)
	}
	_, err = ep.PublishFileExport(ctx, "fs", "10.0.0.1", false)
	wantCode(t, err, endpoint.FailedPrecondition)
	if h.Prop(h.Parent+"/fs", "sharenfs") != "off" {
		t.Fatal("refused publish changed sharenfs")
	}

	_, err = ep.PublishFileExport(ctx, "missing", "10.0.0.1", false)
	wantCode(t, err, endpoint.NotFound)
	if err := ep.UnpublishFileExport(ctx, "missing", "10.0.0.1"); err != nil {
		t.Fatalf("unpublish missing: %v", err)
	}
	for _, host := range []string{"", "*", "10.0.0.1,rw", "a:b", "no_root_squash", "-o", "x(rw)", "fe80::1", "a b", "node-a", "0.0.0.0/0", "10.0.0.1/8"} {
		_, err = ep.PublishFileExport(ctx, "fs", host, false)
		wantCode(t, err, endpoint.InvalidArgument)
		wantCode(t, ep.UnpublishFileExport(ctx, "fs", host), endpoint.InvalidArgument)
	}
	// an id this endpoint never issues names no volume, as for the other
	// volume calls: Publish NotFound, Unpublish OK; a missing one is invalid
	for _, id := range []string{"a/b", "a@b", strings.Repeat("x", 129)} {
		_, err = ep.PublishFileExport(ctx, id, "10.0.0.1", false)
		wantCode(t, err, endpoint.NotFound)
		if err := ep.UnpublishFileExport(ctx, id, "10.0.0.1"); err != nil {
			t.Fatalf("unpublish %q: %v", id, err)
		}
	}
	_, err = ep.PublishFileExport(ctx, "", "10.0.0.1", false)
	wantCode(t, err, endpoint.InvalidArgument)
	wantCode(t, ep.UnpublishFileExport(ctx, "", "10.0.0.1"), endpoint.InvalidArgument)

	// the floor is configurable; /16 by default
	for _, bad := range []int{-1, 33} {
		if _, err := endpoint.New(h.Z, endpoint.Config{Parent: h.Parent, NFSMinPrefix: bad}); err == nil {
			t.Fatalf("NFSMinPrefix %d accepted", bad)
		}
	}
	_, err = ep.PublishFileExport(ctx, "fs", "10.0.0.0/15", false)
	wantCode(t, err, endpoint.InvalidArgument)
	wide, err := endpoint.New(h.Z, endpoint.Config{Parent: h.Parent, LockWait: time.Second, FileExport: true, NFSMinPrefix: 8})
	if err != nil {
		t.Fatal(err)
	}
	_, err = wide.PublishFileExport(ctx, "fs", "10.0.0.0/7", false)
	wantCode(t, err, endpoint.InvalidArgument)

	off, err := endpoint.New(h.Z, endpoint.Config{Parent: h.Parent, LockWait: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if off.FileExportEnabled() {
		t.Fatal("file export enabled by default")
	}
	_, err = off.PublishFileExport(ctx, "fs", "10.0.0.1", false)
	wantCode(t, err, endpoint.Unimplemented)
	wantCode(t, off.UnpublishFileExport(ctx, "fs", "10.0.0.1"), endpoint.Unimplemented)
}

// `zfs set sharenfs` exits 0 when exportfs fails. Publish must not report
// success then, and the retry must re-share although the properties are
// already right; likewise Unpublish must not report a revoke that did not
// happen (review: "Unexport reports OK while the volume is still exported").
func TestFileExportVerifiesKernelTable(t *testing.T) {
	ctx := context.Background()
	ep, h := setup(t)
	sim := h.Sim(t)
	v, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "pvc"})
	if err != nil {
		t.Fatal(err)
	}

	sim.SetNFSDown(true)
	_, err = ep.PublishFileExport(ctx, "pvc", "10.0.0.1", false)
	wantCode(t, err, endpoint.Unavailable)
	if !strings.Contains(err.Error(), "NFS server") {
		t.Fatalf("unclear error: %v", err)
	}
	sim.SetNFSDown(false)
	if _, err := ep.PublishFileExport(ctx, "pvc", "10.0.0.1", false); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if _, ok := h.served(t, v.Mountpoint, "10.0.0.1"); !ok {
		t.Fatal("retry did not re-share")
	}

	sim.SetNFSDown(true)
	wantCode(t, ep.UnpublishFileExport(ctx, "pvc", "10.0.0.1"), endpoint.Unavailable)
	wantCode(t, ep.UnpublishFileExport(ctx, "pvc", "10.0.0.1"), endpoint.Unavailable)
	sim.SetNFSDown(false)
	if err := ep.UnpublishFileExport(ctx, "pvc", "10.0.0.1"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if _, ok := h.served(t, v.Mountpoint, "10.0.0.1"); ok {
		t.Fatal("still served")
	}
}

// `systemctl stop nfs-server` empties etab (exportfs -au), and the next
// sharenfs change repopulates it although no nfsd runs (exportfs -ra does
// not check). etab alone would report that as served (adv4 F4).
func TestFileExportRequiresNFSD(t *testing.T) {
	ctx := context.Background()
	ep, h := setup(t)
	sim := h.Sim(t)
	v, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "pvc"})
	if err != nil {
		t.Fatal(err)
	}
	sim.SetNFSDRunning(false)
	for range 2 { // the second is the empty-plan path
		_, err = ep.PublishFileExport(ctx, "pvc", "10.0.0.1", false)
		wantCode(t, err, endpoint.Unavailable)
		if !strings.Contains(err.Error(), "not running") {
			t.Fatalf("unclear error: %v", err)
		}
		if _, ok := h.served(t, v.Mountpoint, "10.0.0.1"); !ok {
			t.Fatal("test setup: etab not repopulated without nfsd")
		}
	}
	sim.SetNFSDRunning(true)
	if _, err := ep.PublishFileExport(ctx, "pvc", "10.0.0.1", false); err != nil {
		t.Fatalf("after start: %v", err)
	}
	// revoking needs no server: nothing serves the client either way
	sim.SetNFSDRunning(false)
	if err := ep.UnpublishFileExport(ctx, "pvc", "10.0.0.1"); err != nil {
		t.Fatalf("unpublish with nfsd stopped: %v", err)
	}
	sim.SetNFSDRunning(true)
	if _, ok := h.served(t, v.Mountpoint, "10.0.0.1"); ok {
		t.Fatal("served again after restart")
	}
}

// exportfs can drop one client and apply the rest, still exiting 0 (adv4
// F4): only that client's publish fails, every retry re-shares, and the
// other client is untouched.
func TestFileExportDroppedHost(t *testing.T) {
	ctx := context.Background()
	ep, h := setup(t)
	sim := h.Sim(t)
	v, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "pvc"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ep.PublishFileExport(ctx, "pvc", "10.0.0.1", false); err != nil {
		t.Fatal(err)
	}
	sim.DropHost("10.0.0.2/32")
	for range 2 {
		from := len(sim.Calls)
		_, err = ep.PublishFileExport(ctx, "pvc", "10.0.0.2", true)
		wantCode(t, err, endpoint.Unavailable)
		if sets(h, from) != 1 {
			t.Fatalf("publish ran %d zfs set, want 1", sets(h, from))
		}
	}
	if c, ok := h.served(t, v.Mountpoint, "10.0.0.1"); !ok || c.ReadOnly {
		t.Fatalf("other client: %v %v", c, ok)
	}
	if err := ep.UnpublishFileExport(ctx, "pvc", "10.0.0.2"); err != nil {
		t.Fatalf("unpublish the dropped client: %v", err)
	}
	if val := h.Prop(v.Dataset, "sharenfs"); val != "rw=10.0.0.1/32" {
		t.Fatalf("sharenfs %q", val)
	}
}

// exportfs stores an export under its real path (nfsd_realpath); the
// mountpoint property may cross a symlink (adv4 F6).
func TestFileExportResolvesSymlinks(t *testing.T) {
	ctx := context.Background()
	ep, h := setup(t)
	h.Sim(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "real", "pvc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "real"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	v, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "pvc"})
	if err != nil {
		t.Fatal(err)
	}
	mp := filepath.Join(dir, "link", "pvc")
	if err := h.Z.Set(ctx, v.Dataset, map[string]string{"mountpoint": mp}); err != nil {
		t.Fatal(err)
	}
	path, err := ep.PublishFileExport(ctx, "pvc", "10.0.0.1", false)
	if err != nil || path != mp {
		t.Fatalf("publish: %q %v", path, err)
	}
	b, _ := h.etab().ReadEtab(ctx)
	if strings.Contains(string(b), mp) {
		t.Fatalf("test setup: etab names the symlink path:\n%s", b)
	}
	if err := ep.UnpublishFileExport(ctx, "pvc", "10.0.0.1"); err != nil {
		t.Fatalf("unpublish: %v", err)
	}
	if b, _ = h.etab().ReadEtab(ctx); len(b) != 0 {
		t.Fatalf("still served:\n%s", b)
	}
}

// An operator unmounted the volume (canmount stays on). A plain
// `zfs set sharenfs` would mount and serve it again (adv4 F8); Unpublish
// uses `zfs set -u`, so it stays unmounted, and the next mount shares the
// revoked list. Publish asks for the volume to be served, so it lets zfs
// mount it.
func TestFileExportUnmountedVolume(t *testing.T) {
	ctx := context.Background()
	ep, h := setup(t)
	sim := h.Sim(t)
	v, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "pvc"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"10.0.0.1", "10.0.0.2"} {
		if _, err := ep.PublishFileExport(ctx, "pvc", c, false); err != nil {
			t.Fatal(err)
		}
	}
	sim.Unmount(v.Dataset)
	if err := ep.UnpublishFileExport(ctx, "pvc", "10.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if m := h.Prop(v.Dataset, "mounted"); m != "no" {
		t.Fatalf("unpublish mounted the volume: mounted=%s", m)
	}
	if val := h.Prop(v.Dataset, "sharenfs"); val != "rw=10.0.0.2/32" {
		t.Fatalf("sharenfs %q", val)
	}
	if _, ok := h.served(t, v.Mountpoint, "10.0.0.2"); ok {
		t.Fatal("unmounted volume is served")
	}
	if _, err := ep.PublishFileExport(ctx, "pvc", "10.0.0.3", true); err != nil {
		t.Fatalf("publish of an unmounted canmount=on volume: %v", err)
	}
	if m := h.Prop(v.Dataset, "mounted"); m != "yes" {
		t.Fatalf("publish did not mount: mounted=%s", m)
	}
	if _, ok := h.served(t, v.Mountpoint, "10.0.0.1"); ok {
		t.Fatal("revoked client served after the mount")
	}
	if _, ok := h.served(t, v.Mountpoint, "10.0.0.2"); !ok {
		t.Fatal("remaining client not served after the mount")
	}
}

// A sharenfs received with a replication stream (`zfs recv`) on an owned
// volume is the sending side's export decision: a local value would
// silently shadow it, so it is refused, not overridden or adopted. A
// received "off" exports nothing and is pinned like an inherited one.
func TestFileExportReceivedShareNFS(t *testing.T) {
	ctx := context.Background()
	ep, h := setup(t)
	sim := h.Sim(t)
	v, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "pvc"})
	if err != nil {
		t.Fatal(err)
	}
	sim.Receive(v.Dataset, "sharenfs", "rw=10.0.0.0/8")
	if err := h.Z.Inherit(ctx, v.Dataset, "sharenfs"); err != nil {
		t.Fatal(err)
	}
	_, err = ep.PublishFileExport(ctx, "pvc", "10.0.0.1", false)
	wantCode(t, err, endpoint.FailedPrecondition)
	if !strings.Contains(err.Error(), "received") {
		t.Fatalf("unclear error: %v", err)
	}
	if val, src := h.propSource(t, v.Dataset, "sharenfs"); val != "rw=10.0.0.0/8" || src != "received" {
		t.Fatalf("received sharenfs overridden: %q (%s)", val, src)
	}
	wantCode(t, ep.UnpublishFileExport(ctx, "pvc", "10.0.0.0/8"), endpoint.FailedPrecondition)
	if err := ep.UnpublishFileExport(ctx, "pvc", "10.9.0.0/16"); err != nil {
		t.Fatalf("unpublish of a client nothing serves: %v", err)
	}

	sim.Receive(v.Dataset, "sharenfs", "off")
	if _, err := ep.PublishFileExport(ctx, "pvc", "10.0.0.1", false); err != nil {
		t.Fatalf("received off: %v", err)
	}
	if val, src := h.propSource(t, v.Dataset, "sharenfs"); val != "rw=10.0.0.1/32" || src != "local" {
		t.Fatalf("sharenfs %q (%s)", val, src)
	}
}

// A volume adopted from democratic-csi (adoption mode) inherits sharenfs;
// a parent shared to everyone exports it to "*". The first publish pins a
// local value, and a sharenfs someone else set locally is refused, not
// overwritten.
func TestFileExportAdoptedVolumes(t *testing.T) {
	ctx := context.Background()
	ep, h := setupPrefix(t, "democratic-csi")
	h.NFS(t)
	own := map[string]string{"democratic-csi:managed_resource": "true", "democratic-csi:provision_success": "true",
		"democratic-csi:csi_volume_name": "old"} // democratic-csi writes all three at create
	if err := h.Z.Set(ctx, h.Parent, map[string]string{"sharenfs": "on"}); err != nil {
		t.Fatal(err)
	}
	if err := h.Z.CreateFilesystem(ctx, h.Parent+"/old", own, false); err != nil {
		t.Fatal(err)
	}
	path, err := ep.PublishFileExport(ctx, "old", "10.0.0.1", false)
	if err != nil {
		t.Fatal(err)
	}
	if val, src := h.propSource(t, h.Parent+"/old", "sharenfs"); val != "rw=10.0.0.1/32" || src != "local" {
		t.Fatalf("sharenfs %q (%s)", val, src)
	}
	// Lookup takes only addresses and networks: read the "*" line directly
	if hosts := h.servedHosts(t, path); len(hosts) != 1 || hosts[0] != "10.0.0.1/32" {
		t.Fatalf("served to %v, want only 10.0.0.1/32", hosts)
	}

	own["sharenfs"], own["democratic-csi:csi_volume_name"] = "rw=10.0.0.0/8", "static"
	if err := h.Z.CreateFilesystem(ctx, h.Parent+"/static", own, false); err != nil {
		t.Fatal(err)
	}
	_, err = ep.PublishFileExport(ctx, "static", "10.0.0.1", false)
	wantCode(t, err, endpoint.FailedPrecondition)
	if h.Prop(h.Parent+"/static", "sharenfs") != "rw=10.0.0.0/8" {
		t.Fatal("foreign sharenfs overwritten")
	}
	if err := h.Z.Set(ctx, h.Parent, map[string]string{"sharenfs": "off"}); err != nil {
		t.Fatal(err)
	}
	if err := h.Z.Destroy(ctx, h.Parent+"/static", zfs.DestroyOptions{}); err != nil {
		t.Fatal(err)
	}
}
