import os
os.chdir('/home/user/empty/.claude/worktrees/agent-ad45c3ab7d0b822f7')
p='internal/endpoint/reconcile_contract_test.go'
s=open(p).read()
for old,new in [
('''// Reconcile takes the lock of everything it touches: an operation holding
// one makes it Aborted, with nothing applied. A dry run takes no locks.''','''// Reconcile takes the lock of everything it touches, without waiting: a
// volume another operation holds is skipped as busy, and the rest of the
// run goes on. A dry run takes no locks.'''),
('''	for _, name := range []string{"keep", "busy"} {''','''	for _, name := range []string{"keep", "busy", "free"} {'''),
('''	_, err = ep.Reconcile(ctx, keep)
	wantCode(t, err, endpoint.Aborted)
	if !h.Has(h.Parent + "/busy") {
		t.Fatal("destroyed a volume another operation held")
	}
	dry := keep
	dry.DryRun = true
	if p, err := ep.Reconcile(ctx, dry); err != nil || len(p.Actions) != 1 {''','''	p, err := ep.Reconcile(ctx, keep)
	if err != nil {
		t.Fatal(err)
	}
	if !h.Has(h.Parent + "/busy") {
		t.Fatal("destroyed a volume another operation held")
	}
	if acts, skips := targets(p); !slices.Equal(acts, []string{"free"}) || !slices.Equal(skips, []string{"busy"}) ||
		p.Skipped[0].Reason != endpoint.SkipBusy || h.Has(h.Parent+"/free") {
		t.Fatalf("busy volume: %+v", p)
	}
	dry := keep
	dry.DryRun = true
	if p, err := ep.Reconcile(ctx, dry); err != nil || len(p.Actions) != 1 {'''),
]:
    assert s.count(old)==1, old
    s=s.replace(old,new)
open(p,'w').write(s)
