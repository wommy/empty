p = '/home/user/empty/.claude/worktrees/agent-a5e3d463acd0910f8/internal/endpoint/reconcile_fuzz_test.go'
s = open(p).read()


def rep(a, b):
    global s
    assert s.count(a) == 1, a
    s = s.replace(a, b)


rep('''		hidden := map[string]bool{} // volumes zfs refuses to destroy for a reason the scan cannot see
''', '''		hidden := map[string]bool{} // volumes zfs refuses to destroy for a reason the scan cannot see
		zvol := map[string]bool{}   // a clone is a zvol when its source is
''')
rep('''				must(z.Snapshot(ctx, snap, stamp))
				must(z.Clone(ctx, snap, ds, own))
			case x&originOutside != 0:''', '''				must(z.Snapshot(ctx, snap, stamp))
				must(z.Clone(ctx, snap, ds, own))
				zvol[id] = zvol[src]
			case x&originOutside != 0:''')
rep('''			case b&block != 0:
				must(z.CreateVolume(ctx, ds, 1<<20, true, own))''', '''			case b&block != 0:
				must(z.CreateVolume(ctx, ds, 1<<20, true, own))
				zvol[id] = true''')
rep('''			if x&child != 0 && b&block == 0 {''', '''			if x&child != 0 && !zvol[id] { // zfs creates nothing below a zvol''')
open(p, 'w').write(s)
