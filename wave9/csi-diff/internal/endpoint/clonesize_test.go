package endpoint_test

import (
	"context"
	"strings"
	"testing"

	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/zfs"
)

const mib = 1 << 20

// CSI: limit_bytes is a MUST. A clone starts at its source's size and is
// never shrunk, so a source larger than the limit is OutOfRange before any
// op, not a success above the limit followed by AlreadyExists on retry.
func TestCloneLargerThanLimitIsOutOfRange(t *testing.T) {
	ctx := context.Background()
	ep, h := setup(t)
	for _, k := range []endpoint.VolumeKind{endpoint.Block, endpoint.Filesystem} {
		src := "src-" + k.String()
		if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: src, Kind: k, CapacityBytes: 10 * mib}); err != nil {
			t.Fatal(err)
		}
		if _, err := ep.CreateSnapshot(ctx, "s-"+k.String(), src); err != nil {
			t.Fatal(err)
		}
		for _, r := range []endpoint.CreateVolumeRequest{
			{Name: "fromsnap-" + k.String(), Kind: k, SourceSnapshotID: src + "@s-" + k.String(), CapacityBytes: 4 * mib, LimitBytes: 5 * mib},
			{Name: "fromvol-" + k.String(), Kind: k, SourceVolumeID: src, CapacityBytes: 4 * mib, LimitBytes: 5 * mib},
		} {
			for i := 0; i < 2; i++ {
				if _, err := ep.CreateVolume(ctx, r); endpoint.CodeOf(err) != endpoint.OutOfRange {
					t.Errorf("%s attempt %d: %v, want OutOfRange", r.Name, i, err)
				}
			}
			if h.Has(h.Parent + "/" + r.Name) {
				t.Errorf("%s created", r.Name)
			}
		}
	}
	if h.Has(h.Parent + "/src-block@volume-source-for-volume-fromvol-block") {
		t.Error("clone-source snapshot cut for a refused clone")
	}
}

// A filesystem restore with Reserve and no capacity reserves the size it
// takes from its source, and the identical retry is a no-op.
func TestReserveRestoreIsIdempotent(t *testing.T) {
	ctx := context.Background()
	ep, h := setup(t)
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "src", CapacityBytes: 10 * mib}); err != nil {
		t.Fatal(err)
	}
	if _, err := ep.CreateSnapshot(ctx, "s", "src"); err != nil {
		t.Fatal(err)
	}
	for _, req := range []endpoint.CreateVolumeRequest{
		{Name: "dst", SourceSnapshotID: "src@s", Reserve: true},
		{Name: "dst2", SourceVolumeID: "src", Reserve: true},
	} {
		for i := 0; i < 2; i++ {
			v, err := ep.CreateVolume(ctx, req)
			if err != nil || v.CapacityBytes != 10*mib {
				t.Fatalf("%s attempt %d: %+v %v", req.Name, i, v, err)
			}
		}
		if got := h.Prop(h.Parent+"/"+req.Name, "refreservation"); got != "10485760" {
			t.Errorf("%s refreservation %q", req.Name, got)
		}
	}
	// an unlimited source gives nothing to reserve
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "free"}); err != nil {
		t.Fatal(err)
	}
	_, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "dst3", SourceVolumeID: "free", Reserve: true})
	wantCode(t, err, endpoint.InvalidArgument)
}

// A failure (or crash) right after the clone converges, on retry, to the
// end state an uninterrupted run reaches: same refquota, same reservation.
func TestCloneFailureAfterCloneConvergesToSameState(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		req  endpoint.CreateVolumeRequest
	}{
		{"snapshot", endpoint.CreateVolumeRequest{Name: "dst", SourceSnapshotID: "src@s"}},
		{"snapshot-reserve", endpoint.CreateVolumeRequest{Name: "dst", SourceSnapshotID: "src@s", Reserve: true}},
		{"volume", endpoint.CreateVolumeRequest{Name: "dst", SourceVolumeID: "src"}},
		{"volume-reserve", endpoint.CreateVolumeRequest{Name: "dst", SourceVolumeID: "src", Reserve: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ep, h := setup(t)
			sim := h.Sim(t)
			if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "src", CapacityBytes: 10 * mib}); err != nil {
				t.Fatal(err)
			}
			if _, err := ep.CreateSnapshot(ctx, "s", "src"); err != nil {
				t.Fatal(err)
			}
			sim.Fail = func(a []string) error {
				if a[0] == "set" && !strings.Contains(strings.Join(a, " "), "provision_success") {
					return &zfs.ExitError{Code: 1, Stderr: "internal error: Input/output error\n"}
				}
				return nil
			}
			if _, err := ep.CreateVolume(ctx, tc.req); err == nil {
				t.Fatal("want the injected failure")
			}
			sim.Fail = nil
			for i := 0; i < 2; i++ {
				v, err := ep.CreateVolume(ctx, tc.req)
				if err != nil || v.CapacityBytes != 10*mib {
					t.Fatalf("attempt %d: %+v %v", i, v, err)
				}
			}
			wantRes := "0"
			if tc.req.Reserve {
				wantRes = "10485760"
			}
			if got := h.Prop(h.Parent+"/dst", "refreservation"); got != wantRes {
				t.Errorf("refreservation %q, want %q", got, wantRes)
			}
		})
	}
}
