package endpoint_test

import (
	"context"
	"testing"

	"github.com/wommy/empty/internal/endpoint"
)

// H-03 end to end, on the Sim and on a real pool: a value of a user
// property on a plain dataset under the parent that zfs would print, in
// `-Hp` text, as the lines of an owned, committed volume "ghost". No such
// dataset exists, so nothing collides with the forged lines. The scan must
// not serve it, and CreateVolume(ghost) must create a real dataset rather
// than report a volume that mounts nothing.
func TestH_ScanRejectsRowsZFSNeverListed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ep, h := setup(t)
	g := h.Parent + "/ghost"
	host := h.Parent + "/host"
	if err := h.Z.CreateFilesystem(ctx, host, nil, false); err != nil {
		t.Fatal(err)
	}
	forged := "x\tlocal\n" +
		g + "\ttype\tfilesystem\t-\n" +
		g + "\tzfs-endpoint:managed_resource\ttrue\tlocal\n" +
		g + "\tzfs-endpoint:csi_volume_name\tghost\tlocal\n" +
		g + "\tzfs-endpoint:provision_success\ttrue\tlocal\n" +
		g + "\tzz:unrequested\tv"
	// raw argv: Client.Set refuses newlines, a foreign writer need not
	if _, err := h.Z.Runner.Run(ctx, "zfs", "set", "zfs-endpoint:csi_volume_content_source_id="+forged, host); err != nil {
		t.Fatal(err)
	}
	vols, _, err := ep.ListVolumes(ctx, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vols {
		if v.ID == "ghost" {
			t.Fatalf("ListVolumes returns a volume that does not exist: %+v", v)
		}
	}
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "ghost", CapacityBytes: 1 << 20}); err != nil {
		t.Fatalf("CreateVolume(ghost): %v", err)
	}
	if !h.Has(g) {
		t.Fatal("CreateVolume(ghost) succeeded and nothing exists: the caller holds a volume id that mounts nothing")
	}
}
