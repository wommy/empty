package endpoint

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/wommy/empty/internal/zfs"
)

// "is not managed" says why, and what to do.
func TestNotManagedSaysWhy(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		row  Row
		why  string
	}{
		{"no marker", vol("x", func(r *Row) { r.Managed, r.VolumeName = false, "" }),
			"it has no democratic-csi:managed_resource marker;"},
		{"received", vol("x", func(r *Row) { r.Managed, r.ManagedSource = false, "received" }),
			"its democratic-csi:managed_resource is received (zfs recv), not set locally: a copy of a volume is not the volume;"},
		{"inherited", vol("x", func(r *Row) { r.Managed, r.ManagedSource = false, "inherited from tank/v" }),
			"its democratic-csi:managed_resource is inherited from tank/v, not set on it;"},
		{"no name", vol("x", func(r *Row) { r.VolumeName = "" }),
			"it has democratic-csi:managed_resource but no local democratic-csi:csi_volume_name;"},
		{"renamed", vol("x", func(r *Row) { r.VolumeName = "y" }),
			`its democratic-csi:csi_volume_name is "y", not "x": renamed, or copied from another volume;`},
	} {
		_, err := pl.PlanDeleteVolume(table(tc.row), "x")
		want := "dataset tank/v/x is not managed: " + tc.why +
			" if it is this volume, set both markers locally (zfs set democratic-csi:managed_resource=true democratic-csi:csi_volume_name=x), otherwise rename it away"
		if CodeOf(err) != FailedPrecondition || err.Error() != want {
			t.Errorf("%s: %v\nwant %s", tc.name, err, want)
		}
	}
}

// The scan records where a managed_resource that does not count came
// from.
func TestScanRecordsTheManagedSource(t *testing.T) {
	t.Parallel()
	ep, sim := simEndpoint(t, Config{})
	ctx := context.Background()
	z := zfs.New(sim)
	if err := z.CreateFilesystem(ctx, rParent+"/x", map[string]string{"zfs-endpoint:managed_resource": "maybe", "zfs-endpoint:csi_volume_name": "x"}, false); err != nil {
		t.Fatal(err)
	}
	err := ep.DeleteVolume(ctx, "x")
	if CodeOf(err) != FailedPrecondition || !strings.Contains(err.Error(), "is not managed: its zfs-endpoint:managed_resource is set locally, but not to true;") {
		t.Fatalf("DeleteVolume: %v", err)
	}
}

// "has dependent clone X" says when X is an uncommitted create that
// Reconcile collects: definitely where the table holds X (Reconcile), as
// a hint from the clone-source stamp where it does not (DeleteVolume).
func TestDependentCloneSaysUncommitted(t *testing.T) {
	t.Parallel()
	rows := []Row{
		vol("a", nil),
		vol("b", nil),
		snap("b@volume-source-for-volume-c", stamp),
		vol("c", func(r *Row) {
			r.Origin = "tank/v/b@volume-source-for-volume-c"
			r.Committed = false
			r.Creation = recNow - 60
		}),
	}
	plan, err := pl.PlanReconcile(table(rows...), desired("a"))
	if err != nil {
		t.Fatal(err)
	}
	_, skips := summary(plan)
	want := "b:volume b has dependent clone c (c is an uncommitted create, never provisioned: zfs-endpoint reconcile collects it once older than -min-age, and this delete then goes through)"
	if !slices.Contains(skips, want) {
		t.Errorf("skipped %q, want %q", skips, want)
	}

	// end to end: a clone from a volume whose commit failed
	ep, sim := simEndpoint(t, Config{})
	ctx := context.Background()
	if _, err := ep.CreateVolume(ctx, CreateVolumeRequest{Name: "src", CapacityBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	sim.Fail = func(args []string) error {
		if args[0] == "set" && slices.ContainsFunc(args, func(a string) bool { return strings.Contains(a, "provision_success") }) {
			return &zfs.ExitError{Code: 1, Stderr: "cannot set property: I/O error\n"}
		}
		return nil
	}
	if _, err := ep.CreateVolume(ctx, CreateVolumeRequest{Name: "cl", CapacityBytes: 1 << 20, SourceVolumeID: "src"}); err == nil {
		t.Fatal("the clone committed")
	}
	sim.Fail = nil
	err = ep.DeleteVolume(ctx, "src")
	want = "volume src has dependent clone cl (if cl's CreateVolume never completed, zfs-endpoint reconcile collects it once older than -min-age, and this delete then goes through)"
	if CodeOf(err) != FailedPrecondition || err.Error() != want {
		t.Fatalf("DeleteVolume(src): %v\nwant %s", err, want)
	}
}
