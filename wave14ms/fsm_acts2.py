p = '/home/user/empty/.claude/worktrees/agent-a5e3d463acd0910f8/internal/endpoint/fsm_explore_test.go'
s = open(p).read()


def rep(a, b):
    global s
    assert s.count(a) == 1, a
    s = s.replace(a, b)


rep('''		// a backup tool pinning a snapshot (HOLD-REPLAN): the clone source
		// a clone of a leaves, or the CSI snapshot
		{name: "X:hold(a@*)", oob: true, x: func(s *zfstest.Sim, rows []Row, _ string) [][]string {
			for _, id := range []string{"a@" + cloneSourceSnapshotPrefix + "b", "a@s"} {''', '''		// a backup tool pinning a snapshot (HOLD-REPLAN): the clone source
		// a clone of a leaves, or a CSI snapshot (b's, once deleted, is the
		// one Reconcile{a} meets on an undesired volume)
		{name: "X:hold(*@*)", oob: true, x: func(s *zfstest.Sim, rows []Row, _ string) [][]string {
			for _, id := range []string{"a@" + cloneSourceSnapshotPrefix + "b", "a@s", "b@s"} {''')
rep('''		// someone's dataset below a filesystem volume (OWN-12)
		{name: "X:child(a)", oob: true, x: func(s *zfstest.Sim, rows []Row, _ string) [][]string {
			if r, ok := get(rows, "a"); !ok || r.Type != zfs.TypeFilesystem || s.Has(full("a")+"/kid") {
				return nil
			}
			return [][]string{{"create", full("a") + "/kid"}}
		}},''', '''		// someone's dataset below a filesystem volume (OWN-12); b is the
		// volume Reconcile{a} collects
		{name: "X:child(b)", oob: true, x: func(s *zfstest.Sim, rows []Row, _ string) [][]string {
			if r, ok := get(rows, "b"); !ok || r.Type != zfs.TypeFilesystem || s.Has(full("b")+"/kid") {
				return nil
			}
			return [][]string{{"create", full("b") + "/kid"}}
		}},''')
rep('''		{name: "DS(a@s)", rpc: func(ctx context.Context, e *Endpoint) string { return respErr(e.DeleteSnapshot(ctx, "a@s")) }},''',
    '''		{name: "DS(a@s)", rpc: func(ctx context.Context, e *Endpoint) string { return respErr(e.DeleteSnapshot(ctx, "a@s")) }},
		{name: "DS(b@s)", rpc: func(ctx context.Context, e *Endpoint) string { return respErr(e.DeleteSnapshot(ctx, "b@s")) }},''')
open(p, 'w').write(s)
