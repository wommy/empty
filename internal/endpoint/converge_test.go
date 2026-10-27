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
func FuzzCreateConverges(f *testing.F) {
	f.Add(false, uint64(1<<30), uint64(0), false, false)
	f.Add(true, uint64(1<<30+1), uint64(0), true, false)
	f.Add(true, uint64(1), uint64(16384), false, false)
	f.Add(false, uint64(0), uint64(1<<20), false, false)
	f.Add(false, uint64(0), uint64(0), false, true) // reserve without size
	f.Add(false, uint64(5), uint64(0), false, true)
	f.Fuzz(func(t *testing.T, block bool, capacity, limit uint64, sparse, reserve bool) {
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
		req := endpoint.CreateVolumeRequest{Name: "pvc", Kind: kind, CapacityBytes: capacity % (1 << 50),
			LimitBytes: limit % (1 << 50), Sparse: sparse, Reserve: reserve}
		v1, err := ep.CreateVolume(ctx, req)
		if err != nil {
			return // rejected requests have nothing to converge
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
	})
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
