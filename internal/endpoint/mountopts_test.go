package endpoint

import (
	"context"
	"testing"

	"github.com/wommy/empty/internal/zfs"
)

// F12: an NFS client can write a setuid binary or a device node into its
// volume, and on the storage host the volume is mounted beneath a
// traversable parent. Each filesystem volume, created or cloned, holds
// setuid, exec and devices off locally, whatever the parent says. A
// StorageClass cannot turn them back on (props.go denies them).
func TestFilesystemsMountWithoutSetuidExecOrDevices(t *testing.T) {
	t.Parallel()
	ep, sim := simEndpoint(t, Config{})
	ctx := context.Background()
	z := zfs.New(sim)
	// an operator's parent with the opposite stated explicitly
	if err := z.Set(ctx, rParent, map[string]string{"setuid": "on", "exec": "on", "devices": "on"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ep.CreateVolume(ctx, CreateVolumeRequest{Name: "src", Kind: Filesystem, CapacityBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	if _, err := ep.CreateVolume(ctx, CreateVolumeRequest{Name: "cl", Kind: Filesystem, CapacityBytes: 1 << 20, SourceVolumeID: "src"}); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"src", "cl"} {
		ds, err := z.Get(ctx, rParent+"/"+v, "setuid", "exec", "devices")
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"setuid", "exec", "devices"} {
			if p := ds.Props[k]; p.Value != "off" || !p.Local() {
				t.Errorf("%s: %s=%s (%s), want off set locally", v, k, p.Value, p.Source)
			}
		}
	}
	if _, err := ep.CreateVolume(ctx, CreateVolumeRequest{Name: "x", Kind: Filesystem, Properties: map[string]string{"exec": "on"}}); err == nil {
		t.Error("a StorageClass turned exec on")
	}
}
