package endpoint_test

import (
	"context"
	"syscall"
	"testing"

	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

// rootOf is a filesystem's root directory owner and mode: the simulator's
// record, or the mounted directory itself on a real pool.
func (h *harness) rootOf(t *testing.T, dataset, mountpoint string) zfstest.Root {
	t.Helper()
	if h.sim != nil {
		r, ok := h.sim.RootOf(dataset)
		if !ok {
			t.Fatalf("%s: no such filesystem", dataset)
		}
		return r
	}
	var st syscall.Stat_t
	if err := syscall.Lstat(mountpoint, &st); err != nil {
		t.Fatal(err)
	}
	return zfstest.Root{UID: st.Uid, GID: st.Gid, Mode: uint32(st.Mode) & 0o7777}
}

func u32(n uint32) *uint32 { return &n }

func nfsRoot(name string, uid, gid, mode *uint32) endpoint.CreateVolumeRequest {
	r := nfsCreate(name, "", "10.20.0.0/24")
	r.NFS.UID, r.NFS.GID, r.NFS.Mode = uid, gid, mode
	return r
}

// N11 (wave9; csi-nfs-design §13 Q1(a)): under root_squash a client's root
// cannot chown, so the endpoint gives the volume's root its owner and
// mode before the commit. Unset is as the create made it; a restore keeps
// its source's; a repeat is never compared on them.
func TestFileExportCreateRootOwner(t *testing.T) {
	ctx := context.Background()
	ep, h := setupNFS(t, nfsSrv1)
	h.NFS(t)
	v, err := ep.CreateVolume(ctx, nfsRoot("pvc", u32(1000), u32(2000), u32(0o2775)))
	if err != nil {
		t.Fatal(err)
	}
	want := zfstest.Root{UID: 1000, GID: 2000, Mode: 0o2775}
	if got := h.rootOf(t, v.Dataset, v.Mountpoint); got != want {
		t.Fatalf("root %+v, want %+v", got, want)
	}
	// file metadata, not volume state: the repeat is compatible, and the
	// committed volume's root is not touched again
	if _, err := ep.CreateVolume(ctx, nfsRoot("pvc", u32(0), nil, u32(0o700))); err != nil {
		t.Fatalf("repeat with another owner: %v", err)
	}
	if got := h.rootOf(t, v.Dataset, v.Mountpoint); got != want {
		t.Fatalf("a repeat changed the root: %+v", got)
	}
	if _, err := ep.CreateSnapshot(ctx, "s", "pvc"); err != nil {
		t.Fatal(err)
	}
	restore := nfsRoot("r", nil, nil, u32(0o770))
	restore.SourceSnapshotID = "pvc@s"
	r, err := ep.CreateVolume(ctx, restore)
	if err != nil {
		t.Fatal(err)
	}
	if got := h.rootOf(t, r.Dataset, r.Mountpoint); got != (zfstest.Root{UID: 1000, GID: 2000, Mode: 0o770}) {
		t.Fatalf("restore: root %+v, want the source's owner with mode 0770", got)
	}
	plain, err := ep.CreateVolume(ctx, nfsCreate("plain", "", "10.20.0.0/24"))
	if err != nil {
		t.Fatal(err)
	}
	if got := h.rootOf(t, plain.Dataset, plain.Mountpoint); got != (zfstest.Root{Mode: 0o755}) {
		t.Fatalf("no owner asked: root %+v, want root:root 0755", got)
	}
	_, err = ep.CreateVolume(ctx, nfsRoot("bad", u32(endpoint.MaxRootID+1), nil, nil))
	wantCode(t, err, endpoint.InvalidArgument)
	if h.Has(h.Parent + "/bad") {
		t.Fatal("a refused create made a dataset")
	}
}
