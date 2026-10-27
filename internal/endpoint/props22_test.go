package endpoint_test

import (
	"context"
	"testing"

	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

// On a 2.2 userland, xattr=dir reads back as "on" and snapdir=disabled does
// not exist: both are refused up front, and the values that are accepted
// stay idempotent on both userlands.
func TestPropertiesOnOpenZFS22(t *testing.T) {
	ctx := context.Background()
	for _, v22 := range []bool{false, true} {
		sim := zfstest.New("tank")
		sim.OpenZFS22 = v22
		z := zfs.New(sim)
		if err := z.CreateFilesystem(ctx, "tank/v", nil, false); err != nil {
			t.Fatal(err)
		}
		ep, err := endpoint.New(z, endpoint.Config{Parent: "tank/v"})
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range []map[string]string{{"xattr": "dir"}, {"xattr": "on"}, {"snapdir": "disabled"}} {
			_, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "bad", CapacityBytes: 1 << 20, Properties: p})
			if endpoint.CodeOf(err) != endpoint.InvalidArgument {
				t.Errorf("2.2=%v %v: %v", v22, p, err)
			}
		}
		req := endpoint.CreateVolumeRequest{Name: "pvc", CapacityBytes: 1 << 20, Properties: map[string]string{
			"xattr": "sa", "snapdir": "visible", "compression": "gzip-6", "acltype": "posixacl",
		}}
		for i := 0; i < 2; i++ {
			if _, err := ep.CreateVolume(ctx, req); err != nil {
				t.Fatalf("2.2=%v attempt %d: %v", v22, i, err)
			}
		}
	}
}
