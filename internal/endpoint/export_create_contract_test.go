package endpoint_test

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/wommy/empty/internal/endpoint"
)

// Static NFS export at create (csi-nfs-design §3-§6 as amended by wave13
// Q1): the volume is created with its export and its server, and
// committed only once the NFS server serves every client.

const (
	nfsSrv1 = "192.0.2.10"
	nfsSrv2 = "nfs.example.com"
)

var serverProp = endpoint.DefaultPropertyPrefix + ":nfs_server"

func nfsCreate(name string, server string, clients ...string) endpoint.CreateVolumeRequest {
	return endpoint.CreateVolumeRequest{Name: name, NFS: &endpoint.NFSExport{Clients: clients, Server: server}}
}

func reconcileSkips(t *testing.T, ep *endpoint.Endpoint, id string) []string {
	t.Helper()
	p, err := ep.Reconcile(context.Background(), endpoint.ReconcileRequest{DesiredVolumeIDs: []string{id}, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, s := range p.Skipped {
		out = append(out, s.Target+": "+s.Reason)
	}
	return out
}

// N5, N9, N12: created exported and recorded; a repeat is compatible only
// with the same export and the same server or none; delete unexports.
func TestFileExportCreate(t *testing.T) {
	ctx := context.Background()
	ep, h := setupNFS(t, nfsSrv1, nfsSrv2)
	h.NFS(t)
	v, err := ep.CreateVolume(ctx, nfsCreate("pvc", "", "10.20.1.0/24", "10.20.0.0/24"))
	if err != nil {
		t.Fatal(err)
	}
	want := &endpoint.NFSExport{Server: nfsSrv1, Clients: []string{"10.20.0.0/24", "10.20.1.0/24"}}
	if !reflect.DeepEqual(v.NFS, want) {
		t.Fatalf("created %+v, want %+v", v.NFS, want)
	}
	for _, c := range []string{"10.20.0.0/24", "10.20.1.0/24"} {
		if got, ok := h.served(t, v.Mountpoint, c); !ok || got.ReadOnly {
			t.Fatalf("%s: served %+v %v, want read-write", c, got, ok)
		}
	}
	if val, src := h.propSource(t, v.Dataset, serverProp); val != nfsSrv1 || src != "local" {
		t.Fatalf("%s = %q (%s), want %s set locally", serverProp, val, src, nfsSrv1)
	}
	for _, again := range []endpoint.CreateVolumeRequest{
		nfsCreate("pvc", "", "10.20.0.0/24", "10.20.1.0/24"),
		nfsCreate("pvc", nfsSrv1, "10.20.1.0/24", "10.20.0.0/24"),
	} {
		if v2, err := ep.CreateVolume(ctx, again); err != nil || !reflect.DeepEqual(v2.NFS, want) {
			t.Fatalf("repeat %+v: %+v, %v", again.NFS, v2.NFS, err)
		}
	}
	for _, other := range []endpoint.CreateVolumeRequest{
		nfsCreate("pvc", nfsSrv2, "10.20.0.0/24", "10.20.1.0/24"), // N9: another server, explicitly
		nfsCreate("pvc", "", "10.20.0.0/24"),
	} {
		_, err := ep.CreateVolume(ctx, other)
		wantCode(t, err, endpoint.AlreadyExists)
	}
	if got, err := ep.GetVolume(ctx, "pvc"); err != nil || !reflect.DeepEqual(got.NFS, want) {
		t.Fatalf("GetVolume: %+v, %v", got.NFS, err)
	}
	if err := ep.DeleteVolume(ctx, "pvc"); err != nil {
		t.Fatal(err)
	}
	if hosts := h.servedHosts(t, v.Mountpoint); len(hosts) > 0 {
		t.Fatalf("still served to %v after delete", hosts)
	}
}

// N6: zfs create -o sharenfs exits 0 when exportfs fails. The volume stays
// uncommitted and unlisted, the error says so, and a retry converges.
func TestFileExportCreateNotServed(t *testing.T) {
	t.Parallel()
	requireSim(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		hint  string
		which func(h *harness) func(bool)
	}{
		{name: "exportfs fails", hint: "does not serve", which: func(h *harness) func(bool) { return h.sim.SetNFSDown }},
		{name: "nfsd stopped", hint: "not running", which: func(h *harness) func(bool) {
			return func(down bool) { h.sim.SetNFSDRunning(!down) }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ep, h := setupNFS(t, nfsSrv1)
			down := tc.which(h)
			down(true)
			_, err := ep.CreateVolume(ctx, nfsCreate("pvc", "", "10.20.0.0/24"))
			wantCode(t, err, endpoint.Unavailable)
			if !strings.Contains(err.Error(), tc.hint) {
				t.Fatalf("unclear error: %v", err)
			}
			if vs, _, err := ep.ListVolumes(ctx, 0, ""); err != nil || len(vs) > 0 {
				t.Fatalf("listed before it is served: %v, %v", vs, err)
			}
			down(false)
			v, err := ep.CreateVolume(ctx, nfsCreate("pvc", "", "10.20.0.0/24"))
			if err != nil {
				t.Fatalf("retry: %v", err)
			}
			if _, ok := h.served(t, v.Mountpoint, "10.20.0.0/24"); !ok {
				t.Fatal("retry committed an unserved volume")
			}
			if vs, _, _ := ep.ListVolumes(ctx, 0, ""); len(vs) != 1 {
				t.Fatalf("listed %v after the retry", vs)
			}
		})
	}
}

// N14: the server is recorded, never recomputed. An endpoint restarted with
// its servers reordered returns the recorded one; one that stopped
// advertising it reports the volume (D8), and still keeps it.
func TestFileExportCreateServerSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	ep, h := setupNFS(t, nfsSrv1, nfsSrv2)
	h.NFS(t)
	if _, err := ep.CreateVolume(ctx, nfsCreate("pvc", "", "10.20.0.0/24")); err != nil {
		t.Fatal(err)
	}
	reordered := h.endpoint(t, "", func(c *endpoint.Config) { c.NFSServers = []string{nfsSrv2, nfsSrv1} })
	v, err := reordered.CreateVolume(ctx, nfsCreate("pvc", "", "10.20.0.0/24"))
	if err != nil || v.NFS == nil || v.NFS.Server != nfsSrv1 {
		t.Fatalf("after a reorder: %+v, %v; want the recorded %s", v.NFS, err, nfsSrv1)
	}
	if got := reconcileSkips(t, reordered, "pvc"); len(got) > 0 {
		t.Fatalf("reported %q", got)
	}
	moved := h.endpoint(t, "", func(c *endpoint.Config) { c.NFSServers = []string{nfsSrv2} })
	if got, err := moved.GetVolume(ctx, "pvc"); err != nil || got.NFS.Server != nfsSrv1 {
		t.Fatalf("server no longer advertised: %+v, %v", got.NFS, err)
	}
	if got := reconcileSkips(t, moved, "pvc"); !slices.Equal(got, []string{"pvc: " + endpoint.SkipNFSServerUnadvertised}) {
		t.Fatalf("reported %q", got)
	}
}

// N16: an nfs_server inherited from the parent is not a volume's: no
// volume reports it, Reconcile names it, and a create records its own.
func TestFileExportCreateServerElsewhere(t *testing.T) {
	ctx := context.Background()
	ep, h := setupNFS(t, nfsSrv1)
	h.NFS(t)
	if err := h.Z.Set(ctx, h.Parent, map[string]string{serverProp: "198.51.100.1"}); err != nil {
		t.Fatal(err)
	}
	plain, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "plain"})
	if err != nil {
		t.Fatal(err)
	}
	if plain.NFS != nil {
		t.Fatalf("inherited server reported: %+v", plain.NFS)
	}
	if got := reconcileSkips(t, ep, "plain"); !slices.Equal(got, []string{"plain: " + endpoint.SkipNFSServerNotLocal}) {
		t.Fatalf("reported %q", got)
	}
	v, err := ep.CreateVolume(ctx, nfsCreate("pvc", "", "10.20.0.0/24"))
	if err != nil || v.NFS.Server != nfsSrv1 {
		t.Fatalf("%+v, %v", v.NFS, err)
	}
}

// Q2's endpoint half: without advertised servers the endpoint takes no
// NFS export, and Config refuses servers it could not serve from.
func TestFileExportCreateNeedsServers(t *testing.T) {
	t.Parallel()
	requireSim(t)
	ctx := context.Background()
	ep, h := setup(t)
	_, err := ep.CreateVolume(ctx, nfsCreate("pvc", "", "10.20.0.0/24"))
	wantCode(t, err, endpoint.FailedPrecondition)
	if ok, _ := h.Z.Exists(ctx, h.Parent+"/pvc"); ok {
		t.Fatal("created")
	}
	for _, cfg := range []endpoint.Config{
		{Parent: h.Parent, NFSServers: []string{nfsSrv1}}, // no FileExport
		{Parent: h.Parent, FileExport: true, NFSServers: []string{nfsSrv1, nfsSrv1}},
		{Parent: h.Parent, FileExport: true, NFSServers: []string{"192.0.2.010"}},
		{Parent: h.Parent, FileExport: true, NFSServers: []string{"2001:db8::1"}},
		{Parent: h.Parent, FileExport: true, NFSServers: []string{"nfs:/x"}},
		{Parent: h.Parent, FileExport: true, NFSServers: []string{"0xc0000210"}},
	} {
		if _, err := endpoint.New(h.Z, cfg); err == nil {
			t.Errorf("New accepted %+v", cfg.NFSServers)
		}
	}
}
