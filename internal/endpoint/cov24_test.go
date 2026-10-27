package endpoint_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

// simNFS is an endpoint with NFS servers on its own simulator: the tests
// here inject faults, so they never run against a real pool, but neither
// do they skip on one (no requireSim, no skip budget).
func simNFS(t *testing.T, mod func(*endpoint.Config)) (*zfstest.Sim, *zfs.Client, *endpoint.Endpoint) {
	t.Helper()
	sim := zfstest.New("tank")
	z := zfs.New(sim)
	if err := z.CreateFilesystem(context.Background(), "tank/k8s/vols", nil, true); err != nil {
		t.Fatal(err)
	}
	cfg := endpoint.Config{Parent: "tank/k8s/vols", FileExport: true, Etab: sim, NFSServers: []string{nfsSrv1}}
	if mod != nil {
		mod(&cfg)
	}
	ep, err := endpoint.New(z, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return sim, z, ep
}

// afterCreate runs do through the runner it wraps once, right before the
// second scan (`get -d 1`) that follows the `zfs create` of target. In an
// NFS filesystem CreateVolume the first is the second pass's (its plan
// resumes: re-shares, then commits) and the second is verifyExport's,
// between the resume and the commit.
type afterCreate struct {
	zfs.Runner
	t       *testing.T
	target  string
	do      [][]string
	created bool
	scans   int
	done    bool
}

func (r *afterCreate) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if r.created && !r.done && len(args) > 0 && args[0] == "get" && slices.Contains(args, "-d") {
		if r.scans++; r.scans == 2 {
			r.done = true
		}
	}
	if r.done && r.do != nil {
		do := r.do
		r.do = nil
		for _, a := range do {
			if _, err := r.Runner.Run(ctx, "zfs", a...); err != nil {
				r.t.Errorf("zfs %v: %v", a, err)
			}
		}
	}
	out, err := r.Runner.Run(ctx, name, args...)
	if len(args) > 0 && args[0] == "create" && args[len(args)-1] == r.target && err == nil {
		r.created = true
	}
	return out, err
}

// Item 4 (wave22): an NFS volume is committed only once its export is
// served as asked and no wider. An ancestor's crossmnt export ("on") that
// serves it to everyone is caught by verifyExport alone: the planner
// cannot see it before the volume exists. The volume stays uncommitted,
// and a retry once the ancestor stops sharing converges.
func TestFileExportCreateServedBeyondByAncestor(t *testing.T) {
	ctx := context.Background()
	sim, z, ep := simNFS(t, nil)
	if err := z.Set(ctx, "tank/k8s", map[string]string{"sharenfs": "on"}); err != nil {
		t.Fatal(err)
	}
	const ds = "tank/k8s/vols/pvc"
	_, err := ep.CreateVolume(ctx, nfsCreate("pvc", "", "10.20.0.0/24"))
	wantCode(t, err, endpoint.FailedPrecondition)
	if !strings.Contains(err.Error(), "served beyond its export") {
		t.Fatalf("not the exposure refusal: %v", err)
	}
	if !sim.Has(ds) {
		t.Fatal("the volume was not created before the check")
	}
	if got := sim.Prop(ds, endpoint.DefaultPropertyPrefix+":provision_success"); got == "true" {
		t.Fatal("committed while served beyond its export")
	}
	if vols, _, err := ep.ListVolumes(ctx, 0, ""); err != nil || len(vols) != 0 {
		t.Fatalf("listed %+v (%v), want nothing committed", vols, err)
	}
	if err := z.Set(ctx, "tank/k8s", map[string]string{"sharenfs": "off"}); err != nil {
		t.Fatal(err)
	}
	v, err := ep.CreateVolume(ctx, nfsCreate("pvc", "", "10.20.0.0/24"))
	if err != nil {
		t.Fatalf("retry once the ancestor stopped sharing: %v", err)
	}
	if got := sim.Prop(ds, endpoint.DefaultPropertyPrefix+":provision_success"); got != "true" || v.NFS == nil {
		t.Fatalf("retry: committed %q, %+v", got, v)
	}
}

// verifyExport's other refusals: between the create and the commit, the
// volume vanished or its sharenfs changed. Both are Aborted (a retry
// re-plans), never a commit.
func TestFileExportCreateChangedBeforeCommit(t *testing.T) {
	ctx := context.Background()
	const ds = "tank/k8s/vols/pvc"
	for _, tc := range []struct {
		name string
		do   [][]string
		want string
	}{
		{"vanished", [][]string{{"destroy", ds}}, "vanished or changed owner"},
		{"owner changed", [][]string{{"inherit", endpoint.DefaultPropertyPrefix + ":managed_resource", ds}}, "vanished or changed owner"},
		{"sharenfs changed", [][]string{{"set", "sharenfs=off", ds}}, "sharenfs changed during provisioning"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sim := zfstest.New("tank")
			r := &afterCreate{Runner: sim, t: t, target: ds, do: tc.do}
			z := zfs.New(r)
			if err := z.CreateFilesystem(ctx, "tank/k8s/vols", nil, true); err != nil {
				t.Fatal(err)
			}
			ep, err := endpoint.New(z, endpoint.Config{Parent: "tank/k8s/vols", FileExport: true, Etab: sim, NFSServers: []string{nfsSrv1}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = ep.CreateVolume(ctx, nfsCreate("pvc", "", "10.20.0.0/24"))
			if !r.done {
				t.Fatal("no scan after the create")
			}
			wantCode(t, err, endpoint.Aborted)
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%v, want %q", err, tc.want)
			}
			if sim.Has(ds) && sim.Prop(ds, endpoint.DefaultPropertyPrefix+":provision_success") == "true" {
				t.Fatal("committed")
			}
		})
	}
}
