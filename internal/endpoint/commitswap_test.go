package endpoint_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/zfs"
)

// commitSwapRunner replaces the dataset a commit (`zfs set
// ns:provision_success=true`) names just before the set runs: the
// original is renamed away and an unowned filesystem made in its place,
// so the commit lands on the impostor.
type commitSwapRunner struct {
	zfs.Runner
	t       *testing.T
	swapped string // the commit's target, "" until swapped
}

func (s *commitSwapRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	commit := endpoint.DefaultPropertyPrefix + ":provision_success=true"
	if s.swapped == "" && len(args) > 2 && args[0] == "set" && slices.Contains(args, commit) {
		target := args[len(args)-1]
		for _, a := range [][]string{{"rename", target, target + "-moved"}, {"create", "-o", "canmount=noauto", target}} {
			if _, err := s.Runner.Run(ctx, "zfs", a...); err != nil {
				s.t.Fatalf("swap: zfs %v: %v", a, err)
			}
		}
		s.swapped = target
	}
	return s.Runner.Run(ctx, name, args...)
}

// A dataset replaced between the create and its commit is never returned
// as the provisioned volume: the re-read after the commit sees that it is
// not ours (Aborted, "changed owner after provisioning"), and the
// endpoint neither adopts nor destroys it. The commit `zfs set` goes by
// name with no guid check, so only that re-read catches the swap.
func TestCreateVolumeCatchesDatasetReplacedBeforeCommit(t *testing.T) {
	t.Parallel()
	for _, kind := range []endpoint.VolumeKind{endpoint.Filesystem, endpoint.Block} {
		t.Run(kind.String(), func(t *testing.T) {
			t.Parallel()
			if kind == endpoint.Block {
				// a zvol udev is still probing may refuse the rename
				requireSim(t)
			}
			ctx := context.Background()
			_, h := setup(t)
			sw := &commitSwapRunner{Runner: h.Z.Runner, t: t}
			ep := crashEndpoint(t, h, sw, false)
			v, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "v", Kind: kind, CapacityBytes: 16 << 20})
			if sw.swapped == "" {
				t.Fatalf("no commit was swapped (%+v, %v)", v, err)
			}
			if endpoint.CodeOf(err) != endpoint.Aborted || !strings.Contains(err.Error(), "owner") {
				t.Fatalf("got %+v, %v; want Aborted: the volume changed owner", v, err)
			}
			if !h.Has(sw.swapped) || !h.Has(sw.swapped+"-moved") {
				t.Fatalf("the impostor (%v) or the renamed original (%v) is gone", h.Has(sw.swapped), h.Has(sw.swapped+"-moved"))
			}
			if got := h.Prop(sw.swapped, endpoint.DefaultPropertyPrefix+":managed_resource"); got == "true" {
				t.Fatal("the endpoint adopted the impostor")
			}
		})
	}
}
