package endpoint_test

import (
	"context"
	"testing"

	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

// mark names a marker under the prefix setup's endpoint reads. A marker
// under any other prefix is an ordinary user property, so a fixture built
// with one tests nothing about ownership (wave12 AN-02): every row would be
// unowned for the trivial reason. The tests below also pin a positive
// control, so a prefix drift fails instead of passing vacuously.
func mark(k string) string { return endpoint.DefaultPropertyPrefix + ":" + k }

// markIsLive fails the test unless vol, a volume the endpoint created,
// carries its name under mark(): the fixtures' markers are the ones read.
func markIsLive(t *testing.T, h *harness, vol string) {
	t.Helper()
	if got := h.Prop(h.Parent+"/"+vol, mark("csi_volume_name")); got != vol {
		t.Fatalf("%s: %s=%q: the fixtures' marker prefix is not the endpoint's", vol, mark("csi_volume_name"), got)
	}
}

// ADR 0003, "never touch what you don't own": a clone source must be a
// committed volume the endpoint owns, and a snapshot source a CSI snapshot
// of one. Anything else names no volume or snapshot the endpoint issued,
// so it is NotFound, and nothing is snapshotted or cloned.
func TestCloneSourcesMustBeOwned(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ep, h := setup(t)
	if err := h.Z.CreateFilesystem(ctx, h.Parent+"/foreign", map[string]string{"refquota": "1048576"}, false); err != nil {
		t.Fatal(err)
	}
	if err := h.Z.Snapshot(ctx, h.Parent+"/foreign@admin", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "v", CapacityBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	// an administrator's snapshot of an owned volume is not a CSI snapshot
	if err := h.Z.Snapshot(ctx, h.Parent+"/v@admin", nil); err != nil {
		t.Fatal(err)
	}
	// a clone from v leaves the internal clone-source snapshot behind
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "c0", SourceVolumeID: "v"}); err != nil {
		t.Fatal(err)
	}
	// an uncommitted volume: an interrupted provision, never returned
	if err := h.Z.CreateFilesystem(ctx, h.Parent+"/half", map[string]string{
		mark("managed_resource"): "true", mark("csi_volume_name"): "half",
	}, false); err != nil {
		t.Fatal(err)
	}
	for _, r := range []endpoint.CreateVolumeRequest{
		{Name: "c1", SourceVolumeID: "foreign"},
		{Name: "c2", SourceSnapshotID: "foreign@admin"},
		{Name: "c3", SourceSnapshotID: "v@admin"},
		{Name: "c4", SourceSnapshotID: "v@volume-source-for-volume-c0"},
		{Name: "c5", SourceVolumeID: "half"},
	} {
		_, err := ep.CreateVolume(ctx, r)
		if endpoint.CodeOf(err) != endpoint.NotFound {
			t.Errorf("%s from %q%q: %v, want NotFound", r.Name, r.SourceVolumeID, r.SourceSnapshotID, err)
		}
		if h.Has(h.Parent + "/" + r.Name) {
			t.Errorf("%s was cloned", r.Name)
		}
	}
	for _, s := range []string{"foreign@volume-source-for-volume-c1", "half@volume-source-for-volume-c5"} {
		if h.Has(h.Parent + "/" + s) {
			t.Errorf("snapshot %s cut on a dataset the endpoint does not own", s)
		}
	}
	// control: committed, the same hand-made volume is a source, so the
	// refusal above was for "uncommitted", not for unreadable markers
	markIsLive(t, h, "v")
	if err := h.Z.Set(ctx, h.Parent+"/half", map[string]string{mark("provision_success"): "true"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "c6", SourceVolumeID: "half"}); err != nil {
		t.Errorf("c6 from committed half: %v", err)
	}
}

// cloneRacer creates the clone's target name, unowned, right before the
// clone runs: an admin, another tool or an HA peer in the scan→clone window.
type cloneRacer struct {
	sim    *zfstest.Sim
	target string
	did    bool
}

func (r *cloneRacer) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if args[0] == "clone" && !r.did {
		r.did = true
		if _, err := r.sim.Run(ctx, name, "create", r.target); err != nil {
			return nil, err
		}
	}
	return r.sim.Run(ctx, name, args...)
}

// A clone that finds its target already there must fail, not adopt it:
// "already exists" is not "done" for a dataset someone else made (the
// same hazard the README rules out for `zfs create -p`). A replay of our
// own interrupted clone re-scans and takes the uncommitted-row path.
func TestCloneNeverAdoptsARacingDataset(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sim := zfstest.New("tank")
	r := &cloneRacer{sim: sim, target: "tank/p/c"}
	z := zfs.New(r)
	if err := z.CreateFilesystem(ctx, "tank/p", nil, false); err != nil {
		t.Fatal(err)
	}
	ep, err := endpoint.New(z, endpoint.Config{Parent: "tank/p", PropertyPrefix: "democratic-csi"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "src", CapacityBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	if _, err := ep.CreateSnapshot(ctx, "s", "src"); err != nil {
		t.Fatal(err)
	}
	v, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "c", SourceSnapshotID: "src@s"})
	if endpoint.CodeOf(err) != endpoint.AlreadyExists {
		t.Errorf("CreateVolume over a racing dataset: %+v %v, want AlreadyExists", v, err)
	}
	for _, p := range []string{"refquota", "democratic-csi:provision_success", "democratic-csi:managed_resource"} {
		if got := sim.Prop("tank/p/c", p); got != "" {
			t.Errorf("foreign dataset mutated: %s=%q", p, got)
		}
	}
}

// A dataset whose local markers name another volume (a renamed volume, or
// a copy whose properties were re-set locally) is not the volume its
// dataset name suggests: it is not served, expanded or deleted under it.
func TestVolumeNameMustMatchID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ep, h := setup(t)
	if err := h.Z.CreateFilesystem(ctx, h.Parent+"/moved", map[string]string{
		mark("managed_resource"): "true", mark("provision_success"): "true",
		mark("csi_volume_name"): "orig", "refquota": "1048576",
	}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := ep.GetVolume(ctx, "moved"); endpoint.CodeOf(err) != endpoint.NotFound {
		t.Errorf("GetVolume: %v", err)
	}
	if _, err := ep.ExpandVolume(ctx, "moved", 2<<20, 0); endpoint.CodeOf(err) != endpoint.NotFound {
		t.Errorf("ExpandVolume: %v", err)
	}
	if err := ep.DeleteVolume(ctx, "moved"); endpoint.CodeOf(err) != endpoint.FailedPrecondition {
		t.Errorf("DeleteVolume: %v", err)
	}
	if _, err := ep.CreateSnapshot(ctx, "s", "moved"); endpoint.CodeOf(err) != endpoint.NotFound {
		t.Errorf("CreateSnapshot: %v", err)
	}
	if vols, _, err := ep.ListVolumes(ctx, 0, ""); err != nil || len(vols) != 0 {
		t.Errorf("ListVolumes: %+v %v", vols, err)
	}
	if !h.Has(h.Parent+"/moved") || h.Prop(h.Parent+"/moved", "refquota") != "1048576" {
		t.Error("dataset touched")
	}
	// control: the same markers naming the dataset's own id are a volume,
	// so the refusals above were for the name, not for unreadable markers
	if err := h.Z.CreateFilesystem(ctx, h.Parent+"/same", map[string]string{
		mark("managed_resource"): "true", mark("provision_success"): "true",
		mark("csi_volume_name"): "same", "refquota": "1048576",
	}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := ep.GetVolume(ctx, "same"); err != nil {
		t.Errorf("GetVolume(same), the control: %v", err)
	}
}

// A backup copy received next to the original (`zfs send -R v@x | zfs recv
// v-copy`) carries every user property with source "received". It is not
// the endpoint's: not listed, not deletable, and its snapshots do not
// shadow the original's CSI snapshot names.
func TestReceivedCopyIsNotOwned(t *testing.T) {
	t.Parallel()
	requireSim(t)
	ctx := context.Background()
	ep, h := setup(t)
	sim := h.Sim(t)
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "pvc-1", CapacityBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	if _, err := ep.CreateSnapshot(ctx, "snapA", "pvc-1"); err != nil {
		t.Fatal(err)
	}
	cp := h.Parent + "/pvc-1-copy"
	if err := h.Z.CreateFilesystem(ctx, cp, nil, false); err != nil {
		t.Fatal(err)
	}
	if err := h.Z.Snapshot(ctx, cp+"@snapA", nil); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"managed_resource": "true", "provision_success": "true", "csi_volume_name": "pvc-1"} {
		sim.Receive(cp, mark(k), v)
	}
	for k, v := range map[string]string{"managed_resource": "true", "csi_snapshot_name": "snapA", "csi_snapshot_source_volume_id": "pvc-1"} {
		sim.Receive(cp+"@snapA", mark(k), v)
	}
	// a backup restored under its original name: the received markers name
	// the dataset's own id, so only "received is not local" stands between
	// it and adoption
	rs := h.Parent + "/pvc-2"
	if err := h.Z.CreateFilesystem(ctx, rs, nil, false); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"managed_resource": "true", "provision_success": "true", "csi_volume_name": "pvc-2"} {
		sim.Receive(rs, mark(k), v)
	}
	markIsLive(t, h, "pvc-1")

	vols, _, err := ep.ListVolumes(ctx, 0, "")
	if err != nil || len(vols) != 1 || vols[0].ID != "pvc-1" {
		t.Errorf("ListVolumes: %+v %v", vols, err)
	}
	snaps, _, err := ep.ListSnapshots(ctx, "", "", 0, "")
	if err != nil || len(snaps) != 1 || snaps[0].ID != "pvc-1@snapA" {
		t.Errorf("ListSnapshots: %+v %v", snaps, err)
	}
	if _, err := ep.CreateSnapshot(ctx, "snapA", "pvc-1"); err != nil {
		t.Errorf("CreateSnapshot retry: %v", err)
	}
	if _, err := ep.GetVolume(ctx, "pvc-1-copy"); endpoint.CodeOf(err) != endpoint.NotFound {
		t.Errorf("GetVolume copy: %v", err)
	}
	if err := ep.DeleteVolume(ctx, "pvc-1-copy"); endpoint.CodeOf(err) != endpoint.FailedPrecondition {
		t.Errorf("DeleteVolume copy: %v", err)
	}
	if err := ep.DeleteSnapshot(ctx, "pvc-1-copy@snapA"); endpoint.CodeOf(err) != endpoint.FailedPrecondition {
		t.Errorf("DeleteSnapshot copy: %v", err)
	}
	if _, err := ep.GetVolume(ctx, "pvc-2"); endpoint.CodeOf(err) != endpoint.NotFound {
		t.Errorf("GetVolume restored copy: %v", err)
	}
	if err := ep.DeleteVolume(ctx, "pvc-2"); endpoint.CodeOf(err) != endpoint.FailedPrecondition {
		t.Errorf("DeleteVolume restored copy: %v", err)
	}
	if !h.Has(cp) || !h.Has(cp+"@snapA") || !h.Has(rs) {
		t.Error("received copy destroyed")
	}
}

// Reconcile collects only what the endpoint owns: a received copy and a
// dataset whose markers name another volume are someone else's, however
// "undesired" they look.
func TestReconcileSparesReceivedAndRenamed(t *testing.T) {
	t.Parallel()
	requireSim(t)
	ctx := context.Background()
	ep, h := setup(t)
	sim := h.Sim(t)
	for _, n := range []string{"keep", "gone"} {
		if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: n, CapacityBytes: 1 << 20}); err != nil {
			t.Fatal(err)
		}
	}
	cp := h.Parent + "/keep-copy"
	if err := h.Z.CreateFilesystem(ctx, cp, nil, false); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"managed_resource": "true", "provision_success": "true", "csi_volume_name": "keep"} {
		sim.Receive(cp, mark(k), v)
	}
	if err := h.Z.CreateFilesystem(ctx, h.Parent+"/moved", map[string]string{
		mark("managed_resource"): "true", mark("provision_success"): "true",
		mark("csi_volume_name"): "orig",
	}, false); err != nil {
		t.Fatal(err)
	}
	rs := h.Parent + "/restored"
	if err := h.Z.CreateFilesystem(ctx, rs, nil, false); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"managed_resource": "true", "provision_success": "true", "csi_volume_name": "restored"} {
		sim.Receive(rs, mark(k), v)
	}
	markIsLive(t, h, "keep")
	old := later(t, ep, h)
	p, err := old.Reconcile(ctx, digested(ctx, t, old, endpoint.ReconcileRequest{DesiredVolumeIDs: []string{"keep"}}))
	if acts, _ := targets(p); err != nil || len(acts) != 1 || acts[0] != "gone" {
		t.Fatalf("reconcile: %+v %v", p, err)
	}
	if !h.Has(cp) || !h.Has(h.Parent+"/moved") || !h.Has(rs) {
		t.Fatal("reconcile destroyed a dataset it does not own")
	}
}
