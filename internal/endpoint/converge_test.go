package endpoint_test

import (
	"context"
	"testing"

	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

// FuzzCreateConverges checks the core data-oriented invariant: for any
// request the planner accepts, applying its plan reaches a state whose plan
// is empty. A second identical CreateVolume must succeed and mutate nothing.
//
// The properties dimension (pk=pv, plus a fixed second one when both is
// set) checks that whatever the allowlist accepts is applied locally and
// reads back equal, and that nothing outside it is ever accepted.
func FuzzCreateConverges(f *testing.F) {
	f.Add(false, uint64(1<<30), uint64(0), false, false, "", "", false)
	f.Add(true, uint64(1<<30+1), uint64(0), true, false, "", "", false)
	f.Add(true, uint64(1), uint64(16384), false, false, "", "", false)
	f.Add(false, uint64(0), uint64(1<<20), false, false, "", "", false)
	f.Add(false, uint64(0), uint64(0), false, true, "", "", false) // reserve without size
	f.Add(false, uint64(5), uint64(0), false, true, "", "", false)
	f.Add(false, uint64(1<<30), uint64(0), false, false, "recordsize", "16K", true)
	f.Add(false, uint64(0), uint64(0), false, false, "compression", "gzip-6", false)
	f.Add(false, uint64(0), uint64(0), false, false, "xattr", "sa", true)
	f.Add(true, uint64(1<<20+1), uint64(0), false, false, "volblocksize", "4k", true)
	f.Add(true, uint64(1<<20), uint64(1<<20+512), true, false, "volblocksize", "512", false)
	f.Add(true, uint64(1<<20), uint64(0), false, false, "recordsize", "16K", false)
	f.Add(false, uint64(0), uint64(0), false, false, "mountpoint", "/etc", false)
	f.Add(false, uint64(0), uint64(0), false, false, "democratic-csi:managed_resource", "false", false)
	f.Fuzz(func(t *testing.T, block bool, capacity, limit uint64, sparse, reserve bool, pk, pv string, both bool) {
		// the simulator prints index properties through OpenZFS 2.4's
		// tables, so read-backs are checked against what zfs prints
		createConverges(t, block, capacity, limit, sparse, reserve, pk, pv, both)
	})
}

func createConverges(t *testing.T, block bool, capacity, limit uint64, sparse, reserve bool, pk, pv string, both bool) {
	{
		ctx := context.Background()
		sim := zfstest.New("tank")
		z := zfs.New(sim)
		if err := z.CreateFilesystem(ctx, "tank/v", nil, false); err != nil {
			t.Fatal(err)
		}
		ep, err := endpoint.New(z, endpoint.Config{Parent: "tank/v"})
		if err != nil {
			t.Fatal(err)
		}
		kind := endpoint.Filesystem
		if block {
			kind = endpoint.Block
		}
		var props map[string]string
		if pk != "" || pv != "" {
			props = map[string]string{pk: pv}
		}
		if both {
			if props == nil {
				props = map[string]string{}
			}
			props["compression"] = "zstd"
		}
		req := endpoint.CreateVolumeRequest{Name: "pvc", Kind: kind, CapacityBytes: capacity % (1 << 50),
			LimitBytes: limit % (1 << 50), Sparse: sparse, Reserve: reserve, Properties: props}
		canon, verr := endpoint.ValidateProperties(kind, props)
		v1, err := ep.CreateVolume(ctx, req)
		if verr != nil {
			if endpoint.CodeOf(err) != endpoint.InvalidArgument || sim.Has("tank/v/pvc") {
				t.Fatalf("invalid properties %v accepted: %v", props, err)
			}
			return
		}
		if err != nil {
			return // rejected requests have nothing to converge
		}
		for k, want := range canon {
			if got := sim.Prop("tank/v/pvc", k); got != want {
				t.Fatalf("property %s = %q, want %q", k, got, want)
			}
		}
		if req.CapacityBytes > 0 && v1.CapacityBytes < req.CapacityBytes {
			t.Fatalf("capacity %d < requested %d", v1.CapacityBytes, req.CapacityBytes)
		}
		if req.LimitBytes > 0 && v1.CapacityBytes > req.LimitBytes {
			t.Fatalf("capacity %d > limit %d", v1.CapacityBytes, req.LimitBytes)
		}
		before := mutations(sim)
		v2, err := ep.CreateVolume(ctx, req)
		if err != nil {
			t.Fatalf("retry of an accepted request failed: %v (%+v)", err, req)
		}
		if after := mutations(sim); after != before {
			t.Fatalf("retry mutated state: %d -> %d zfs mutations", before, after)
		}
		if v1 != v2 {
			t.Fatalf("retry returned %+v, first %+v", v2, v1)
		}
	}
}

func mutations(s *zfstest.Sim) int {
	n := 0
	for _, c := range s.Calls {
		if len(c) > 1 && c[1] != "get" {
			n++
		}
	}
	return n
}
