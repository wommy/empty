p = '/home/user/empty/.claude/worktrees/agent-a5e3d463acd0910f8/internal/endpoint/deleteorder_test.go'
s = open(p).read()
a = '''	// zfs's refusal is reported against the volume, and nothing ran
	if len(p.Actions) != 0 || len(p.Skipped) != 1 || p.Skipped[0].Target != "old" ||
		!strings.HasPrefix(p.Skipped[0].Reason, SkipFailed+": ") || !strings.Contains(p.Skipped[0].Reason, "has children") {
		t.Fatalf("plan %+v", p)
	}
}
'''
b = '''	// the scan sees the child, so the volume is refused at plan time and
	// reported against it, and nothing ran: no destroy zfs refuses on
	// every run (OWN-12), so a second reconcile plans nothing either
	want := "volume old has datasets below it the endpoint did not make: old/someone-elses"
	if len(p.Actions) != 0 || len(p.Skipped) != 1 || p.Skipped[0].Target != "old" || p.Skipped[0].Reason != want {
		t.Fatalf("plan %+v", p)
	}
	again, err := h.ep.Reconcile(ctx, ReconcileRequest{DesiredVolumeIDs: []string{"keep"}, DryRun: true})
	h.must(err)
	if len(again.Actions) != 0 {
		t.Fatalf("reconcile twice: %+v", again)
	}
}

// HOLD-REPLAN: a user hold on a snapshot of an undesired volume (a backup
// tool pinning it for send) makes zfs refuse its plain destroy on every
// run. The scan reads userrefs, so the volume is reported, not planned; the
// stamped orphan clone source is marked with -d, which zfs accepts on a
// held snapshot. A second reconcile plans nothing, and once released the
// next one collects the volume.
func TestReconcileReportsHeldSnapshots(t *testing.T) {
	h := newA4(t)
	ctx := context.Background()
	h.vol("keep", true)
	h.vol("old", true)
	src := a4Parent + "/old@volume-source-for-volume-gone"
	h.must(h.z.Snapshot(ctx, src, map[string]string{h.n.managed(): "true", h.n.cloneSourceFor(): "gone"}))
	zfsRun := func(args ...string) {
		_, err := h.sim.Run(ctx, "zfs", args...)
		h.must(err)
	}
	zfsRun("hold", "backup", src)
	p, err := h.ep.Reconcile(ctx, ReconcileRequest{DesiredVolumeIDs: []string{"keep"}})
	h.must(err)
	acts, skips := summary(p)
	want := "old:volume old has held snapshot old@volume-source-for-volume-gone (1 user hold): zfs release it first"
	if len(acts) != 1 || acts[0] != "old@volume-source-for-volume-gone:"+ReasonOrphanCloneSource || len(skips) != 1 || skips[0] != want {
		t.Fatalf("first run: actions %q skipped %q", acts, skips)
	}
	if !h.has(src) || !h.has(a4Parent+"/old") {
		t.Fatal("a held snapshot or its volume was destroyed")
	}
	again, err := h.ep.Reconcile(ctx, ReconcileRequest{DesiredVolumeIDs: []string{"keep"}, DryRun: true})
	h.must(err)
	if acts, skips := summary(again); len(acts) != 0 || len(skips) != 1 || skips[0] != want {
		t.Fatalf("reconcile twice: actions %q skipped %q", acts, skips)
	}
	// the last release reaps the deferred snapshot; the volume goes next
	zfsRun("release", "backup", src)
	if h.has(src) {
		t.Fatal("release of a deferred snapshot's last hold did not destroy it")
	}
	p, err = h.ep.Reconcile(ctx, ReconcileRequest{DesiredVolumeIDs: []string{"keep"}})
	h.must(err)
	if acts, skips := summary(p); len(acts) != 1 || acts[0] != "old:"+ReasonUndesired || len(skips) != 0 || h.has(a4Parent+"/old") {
		t.Fatalf("after release: actions %q skipped %q", acts, skips)
	}
}
'''
assert s.count(a) == 1
s = s.replace(a, b)
open(p, 'w').write(s)
