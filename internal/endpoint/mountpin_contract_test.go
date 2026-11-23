package endpoint_test

import (
	"context"
	"errors"
	"path"
	"testing"

	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/zfs"
)

// csi-nfs-design §4: a volume's mountpoint, which a PV holds as
// volume_context share, does not move when the parent's does. A volume
// made before the pin (or adopted) follows the parent: Reconcile reports
// it and leaves it alone. The parent itself cannot move while a pinned
// volume is mounted inside it.
func TestCreateVolumePinsMountpoint(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ep, h := setup(t)
	parentMP := h.Prop(h.Parent, "mountpoint")
	v, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "pinned"})
	if err != nil {
		t.Fatal(err)
	}
	if val, src := h.propSource(t, v.Dataset, "mountpoint"); val != path.Join(parentMP, "pinned") || src != "local" || v.Mountpoint != val {
		t.Fatalf("mountpoint %q (%s), volume reports %q; want %s set locally", val, src, v.Mountpoint, path.Join(parentMP, "pinned"))
	}
	// a volume as a version before the pin made it: committed, inherited
	own := map[string]string{
		endpoint.DefaultPropertyPrefix + ":managed_resource":  "true",
		endpoint.DefaultPropertyPrefix + ":csi_volume_name":   "legacy",
		endpoint.DefaultPropertyPrefix + ":provision_success": "true",
	}
	if err := h.Z.CreateFilesystem(ctx, h.Parent+"/legacy", own, false); err != nil {
		t.Fatal(err)
	}
	p, err := ep.Reconcile(ctx, endpoint.ReconcileRequest{DesiredVolumeIDs: []string{"pinned", "legacy"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Actions) > 0 || len(p.Skipped) != 1 || p.Skipped[0] != (endpoint.ReconcileSkip{Target: "legacy", Reason: endpoint.SkipMountpointNotPinned}) {
		t.Fatalf("reconcile %+v, want only legacy reported", p)
	}
	// reported, never rewritten: neither Reconcile nor a repeat pins it
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "legacy"}); err != nil {
		t.Fatal(err)
	}
	if _, src := h.propSource(t, h.Parent+"/legacy", "mountpoint"); src == "local" {
		t.Fatal("the endpoint pinned an existing volume")
	}
	// the parent cannot move while the pinned volume is mounted inside it:
	// its local mountpoint and sharenfs keep it out of zfs's changelist
	// (libzfs change_one), so the parent's unmount is busy and nothing is
	// set. A share in use cannot move under its clients.
	moved := parentMP + "-moved"
	if err := h.Z.Set(ctx, h.Parent, map[string]string{"mountpoint": moved}); !errors.Is(err, zfs.ErrBusy) {
		t.Fatalf("moving the parent over a mounted pinned volume: %v, want busy", err)
	}
	if got := h.Prop(h.Parent, "mountpoint"); got != parentMP {
		t.Fatalf("parent mountpoint %q after a refused move, want %q", got, parentMP)
	}
	// unmounted, the parent moves: the pinned volume stays, the legacy one follows
	if _, err := h.Z.Runner.Run(ctx, "zfs", "unmount", v.Dataset); err != nil {
		t.Fatal(err)
	}
	if err := h.Z.Set(ctx, h.Parent, map[string]string{"mountpoint": moved}); err != nil {
		t.Fatal(err)
	}
	if got, err := ep.GetVolume(ctx, "pinned"); err != nil || got.Mountpoint != v.Mountpoint {
		t.Fatalf("pinned volume after the parent moved: %q, %v; want %q", got.Mountpoint, err, v.Mountpoint)
	}
	if got, err := ep.GetVolume(ctx, "legacy"); err != nil || got.Mountpoint != path.Join(moved, "legacy") {
		t.Fatalf("legacy volume after the parent moved: %q, %v", got.Mountpoint, err)
	}
	// and a new volume is pinned under the parent's new place
	n, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "after"})
	if err != nil || n.Mountpoint != path.Join(moved, "after") {
		t.Fatalf("created after the move: %q, %v", n.Mountpoint, err)
	}
}
