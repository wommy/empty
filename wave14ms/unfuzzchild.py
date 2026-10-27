import shutil
p = '/home/user/empty/.claude/worktrees/agent-a5e3d463acd0910f8/internal/endpoint/reconcile_fuzz_test.go'
shutil.copy(p, '/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave14ms/fuzz_full.go.txt')
s = open(p).read()


def rep(b, a):
    global s
    assert s.count(a) == 1, a
    s = s.replace(a, b)


rep('''				outside = append(outside, ds+"/kid", ds+"/kid@s")
				hidden[id] = true
''', '''				// not hidden: the scan sees the child and refuses the volume
				// at plan time (OWN-12), so no group fails on it
				outside = append(outside, ds+"/kid", ds+"/kid@s")
''')
rep('''//     volume zfs refuses for a reason the scan cannot see (a child dataset,
//     an outside clone of a deferred snapshot); with no such volume a dry''',
    '''//     volume zfs refuses for a reason the scan cannot see (an outside clone
//     of a deferred snapshot); with no such volume a dry''')
rep('''t.Fatalf("third reconcile still failing %s, which no hidden child or outside clone explains: %s"''',
    '''t.Fatalf("third reconcile still failing %s, which no outside clone explains: %s"''')
rep('''		// with nothing hidden from the scan, a converged parent plans nothing; a
		// hidden child or outside clone keeps its group (and the groups''',
    '''		// with nothing hidden from the scan, a converged parent plans nothing; a
		// hidden outside clone keeps its group (and the groups''')
open(p, 'w').write(s)
