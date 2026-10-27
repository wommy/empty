import sys
root = sys.argv[1]

p = root + '/internal/endpoint/reconcile_test.go'
s = open(p).read()
old = '''		{
			name: "unowned datasets are invisible",'''
new = '''		// ADR 0004: export state is sharenfs alone; a leftover stored
		// client list is reported on the volumes that stay, never acted on
		{
			name:  "leftover nfs_clients is reported",
			rows:  []Row{vol("a", func(r *Row) { r.NFSClientsSet = true }), vol("b", nil)},
			want:  desired("a", "b"),
			skips: []string{"a:" + SkipLegacyNFSClients},
		},
		{
			name: "leftover nfs_clients on a collected volume goes with it",
			rows: []Row{vol("a", nil), vol("b", func(r *Row) { r.NFSClientsSet = true })},
			want: desired("a"),
			acts: []string{"b:" + ReasonUndesired},
		},
		{
			name: "leftover nfs_clients on an unowned dataset is not ours to report",
			rows: []Row{vol("x", func(r *Row) { r.Managed, r.NFSClientsSet = false, true }), vol("a", nil)},
			want: desired("a"),
		},
		{
			name: "unowned datasets are invisible",'''
assert old in s
s = s.replace(old, new, 1)
open(p, 'w').write(s)

p = root + '/internal/endpoint/export_contract_test.go'
s = open(p).read()
s += '''
// ADR 0004: export state lives in sharenfs alone. A leftover
// <prefix>:nfs_clients (the client list earlier versions computed sharenfs
// from), stale here, is neither read nor written by Publish or Unpublish,
// and Reconcile reports it. Serial like the other export tests: on the
// real host exportfs -ra rewrites one global table.
func TestFileExportLeftoverClientList(t *testing.T) {
	ctx := context.Background()
	ep, h := setup(t)
	h.NFS(t)
	v, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "pvc-left", CapacityBytes: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ep.PublishFileExport(ctx, "pvc-left", "10.0.0.1", false); err != nil {
		t.Fatal(err)
	}
	stored := endpoint.DefaultPropertyPrefix + ":nfs_clients"
	if err := h.Z.Set(ctx, v.Dataset, map[string]string{stored: "10.0.0.9/32=rw"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ep.PublishFileExport(ctx, "pvc-left", "10.0.0.2", false); err != nil {
		t.Fatal(err)
	}
	if got := h.Prop(v.Dataset, "sharenfs"); got != "rw=10.0.0.1/32:10.0.0.2/32" {
		t.Fatalf("sharenfs %q: computed from the leftover list, not from sharenfs", got)
	}
	if err := ep.UnpublishFileExport(ctx, "pvc-left", "10.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if got := h.Prop(v.Dataset, "sharenfs"); got != "rw=10.0.0.2/32" {
		t.Fatalf("sharenfs %q after unpublish", got)
	}
	if got := h.Prop(v.Dataset, stored); got != "10.0.0.9/32=rw" {
		t.Fatalf("%s rewritten to %q: export state is sharenfs alone", stored, got)
	}
	plan, err := ep.Reconcile(ctx, endpoint.ReconcileRequest{DesiredVolumeIDs: []string{"pvc-left"}, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(plan.Skipped, endpoint.ReconcileSkip{Target: "pvc-left", Reason: endpoint.SkipLegacyNFSClients}) {
		t.Fatalf("reconcile does not report the leftover list: %+v", plan.Skipped)
	}
}

// ADR 0004 (and the wave13 verifier's tension): an admin's "rw=*" over a
// volume the endpoint exports is someone else's value. It is served
// untouched and never rewritten; Publish and Unpublish refuse while it
// stands.
func TestFileExportAdminWorldEditIsForeign(t *testing.T) {
	ctx := context.Background()
	ep, h := setup(t)
	h.NFS(t)
	v, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "pvc-star", CapacityBytes: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ep.PublishFileExport(ctx, "pvc-star", "10.0.0.2", true); err != nil {
		t.Fatal(err)
	}
	if err := h.Z.Set(ctx, v.Dataset, map[string]string{"sharenfs": "rw=*"}); err != nil {
		t.Fatal(err)
	}
	wantCode(t, ep.UnpublishFileExport(ctx, "pvc-star", "10.0.0.1"), endpoint.FailedPrecondition)
	wantCode(t, ep.UnpublishFileExport(ctx, "pvc-star", "10.0.0.2"), endpoint.FailedPrecondition)
	_, err = ep.PublishFileExport(ctx, "pvc-star", "10.0.0.3", false)
	wantCode(t, err, endpoint.FailedPrecondition)
	if got := h.Prop(v.Dataset, "sharenfs"); got != "rw=*" {
		t.Fatalf("an admin's sharenfs was rewritten to %q", got)
	}
	// back inside the grammar, it is the endpoint's again, as typed
	if err := h.Z.Set(ctx, v.Dataset, map[string]string{"sharenfs": "ro=10.0.0.2/32"}); err != nil {
		t.Fatal(err)
	}
	if err := ep.UnpublishFileExport(ctx, "pvc-star", "10.0.0.2"); err != nil {
		t.Fatal(err)
	}
	if got := h.Prop(v.Dataset, "sharenfs"); got != "off" {
		t.Fatalf("sharenfs %q after the last unpublish", got)
	}
}
'''
open(p, 'w').write(s)
