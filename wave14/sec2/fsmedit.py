import os
os.chdir('/home/user/empty/.claude/worktrees/agent-a031c2bb39fd727ca')
p = 'internal/endpoint/fsm_explore_test.go'
s = open(p).read()
pairs = [
    ('return reconResp(e.Reconcile(ctx, ReconcileRequest{})) }},',
     'return reconResp(reconcileAsCaller(ctx, e, ReconcileRequest{})) }},'),
    ('return reconResp(e.Reconcile(ctx, ReconcileRequest{DesiredVolumeIDs: []string{"a"}}))',
     'return reconResp(reconcileAsCaller(ctx, e, ReconcileRequest{DesiredVolumeIDs: []string{"a"}}))'),
    ('return reconResp(e.Reconcile(ctx, ReconcileRequest{DesiredVolumeIDs: d}))',
     'return reconResp(reconcileAsCaller(ctx, e, ReconcileRequest{DesiredVolumeIDs: d}))'),
    ('return reconResp(e.Reconcile(ctx, ReconcileRequest{DesiredVolumeIDs: desired}))',
     'return reconResp(reconcileAsCaller(ctx, e, ReconcileRequest{DesiredVolumeIDs: desired}))'),
]
for old, new in pairs:
    assert s.count(old) == 1, old
    s = s.replace(old, new)
anchor = 'func TestExploreLifecycle(t *testing.T) {'
assert s.count(anchor) == 1
s = s.replace(anchor, '''// reconcileAsCaller runs r as a caller must (ADR 0001 v2 D3): a dry run,
// then that plan by its digest. The dry run mutates nothing, so it moves
// no crash or kill point.
func reconcileAsCaller(ctx context.Context, e *Endpoint, r ReconcileRequest) (ReconcilePlan, error) {
	dry := r
	dry.DryRun = true
	p, err := e.Reconcile(ctx, dry)
	if err != nil {
		return p, err
	}
	r.PlanDigest = p.Digest
	return e.Reconcile(ctx, r)
}

''' + anchor)
open(p, 'w').write(s)
