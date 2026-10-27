package endpoint_test

import (
	"context"
	"testing"

	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/zfs"
)

// failOn makes the first zfs call whose subcommand is cmd fail with stderr.
func failOn(h *harness, t *testing.T, cmd, stderr string) {
	done := false
	h.Sim(t).Fail = func(a []string) error {
		if !done && a[0] == cmd {
			done = true
			return &zfs.ExitError{Code: 1, Stderr: stderr}
		}
		return nil
	}
}

// A busy dataset is mapped by operation: CSI requires FAILED_PRECONDITION
// for a volume or snapshot in use on DeleteVolume and DeleteSnapshot; on
// create, snapshot and expand paths it is transient, so retryable.
func TestBusyIsMappedByOperation(t *testing.T) {
	ctx := context.Background()
	const busy = "cannot destroy 'x': dataset is busy\n"
	ep, h := setup(t)
	for _, n := range []string{"a", "b"} {
		if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: n, Kind: endpoint.Block, CapacityBytes: 1 << 20}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ep.CreateSnapshot(ctx, "s", "a"); err != nil {
		t.Fatal(err)
	}

	failOn(h, t, "destroy", busy)
	wantCode(t, ep.DeleteVolume(ctx, "b"), endpoint.FailedPrecondition)
	failOn(h, t, "destroy", "cannot destroy snapshot x@s: it's being held. Run 'zfs holds -r x@s' to see holders.\n")
	wantCode(t, ep.DeleteSnapshot(ctx, "a@s"), endpoint.FailedPrecondition)

	failOn(h, t, "create", busy)
	_, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "c", Kind: endpoint.Block, CapacityBytes: 1 << 20})
	wantCode(t, err, endpoint.Unavailable)
	failOn(h, t, "snapshot", busy)
	_, err = ep.CreateSnapshot(ctx, "s2", "a")
	wantCode(t, err, endpoint.Unavailable)
	failOn(h, t, "set", busy)
	_, err = ep.ExpandVolume(ctx, "a", 2<<20, 0)
	wantCode(t, err, endpoint.Unavailable)
	failOn(h, t, "clone", busy)
	_, err = ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "d", Kind: endpoint.Block, SourceSnapshotID: "a@s"})
	wantCode(t, err, endpoint.Unavailable)
}

// An unclassified failure before any mutation is Internal. After a step of
// the plan already changed something, it is retryable: the retry re-scans
// and converges, while a final code would strand the half-made volume.
func TestUnknownFailureAfterAMutationIsRetryable(t *testing.T) {
	ctx := context.Background()
	const boom = "internal error: Input/output error\n"
	ep, h := setup(t)
	failOn(h, t, "create", boom)
	_, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "v", CapacityBytes: 1 << 20})
	wantCode(t, err, endpoint.Internal)

	failOn(h, t, "set", boom) // the commit marker, after the create
	_, err = ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "v", CapacityBytes: 1 << 20})
	wantCode(t, err, endpoint.Aborted)
	h.Sim(t).Fail = nil
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "v", CapacityBytes: 1 << 20}); err != nil {
		t.Fatalf("retry: %v", err)
	}
}
