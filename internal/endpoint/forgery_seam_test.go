package endpoint

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

// S1 / H-03: a user property value with embedded newlines printed as a
// whole block of `zfs get -Hp` lines for a dataset that does not exist.
// The text parser refused only a repeated (dataset, property), and a ghost
// has no real line to collide with, so ListVolumes served it and Reconcile
// planned on it. The JSON scan escapes the value: it stays the value of the
// property it was set on. Driven through the Sim, so the forgery is written
// the way a foreign writer would (raw argv, no checkProps), and the test
// holds whichever format the scan reads.
func TestSeamGhostRowFromForgedNewlines(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ep, sim := simEndpoint(t, Config{Now: func() time.Time { return rNow }, LockWait: time.Second})
	if _, err := ep.CreateVolume(ctx, CreateVolumeRequest{Name: "a", CapacityBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	g := rParent + "/ghost"
	// zfs prints "<a>\t<prop>\t" before the value and "\t<source>\n"
	// after it, which complete the first and the last forged line
	forged := "x\tlocal\n" +
		g + "\ttype\tfilesystem\t-\n" +
		g + "\t" + rNames.managed() + "\ttrue\tlocal\n" +
		g + "\t" + rNames.volumeName() + "\tghost\tlocal\n" +
		g + "\t" + rNames.success() + "\ttrue\tlocal\n" +
		g + "\tcreation\t1\t-\n" +
		g + "\tzz:unrequested\tv"
	if _, err := sim.Run(ctx, "zfs", "set", rNames.sourceID()+"="+forged, rParent+"/a"); err != nil {
		t.Fatal(err)
	}
	vols, _, err := ep.ListVolumes(ctx, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, v := range vols {
		ids = append(ids, v.ID)
	}
	if !slices.Equal(ids, []string{"a"}) {
		t.Fatalf("ListVolumes = %q, want [a]: it serves a dataset zfs never listed", ids)
	}
	plan, err := ep.Reconcile(ctx, ReconcileRequest{DesiredVolumeIDs: []string{"a"}, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range plan.Actions {
		if strings.HasPrefix(a.Target, "ghost") {
			t.Fatalf("reconcile plans %s %s (%s) on a dataset that does not exist", a.Op.Kind, a.Target, a.Reason)
		}
	}
	// the forged text is still there, as the value it is
	tbl, err := ep.scan(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := tbl.Get("a"); !ok || r.SourceID != forged {
		t.Fatalf("a: found %v, source id %q", ok, r.SourceID)
	}
}
