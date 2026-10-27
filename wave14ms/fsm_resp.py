p = '/home/user/empty/.claude/worktrees/agent-a5e3d463acd0910f8/internal/endpoint/fsm_explore_test.go'
s = open(p).read()


def rep(a, b):
    global s
    assert s.count(a) == 1, a
    s = s.replace(a, b)


rep('''	var ts []string
	for _, a := range p.Actions {
		ts = append(ts, a.Target)
	}
	return "OK collected=" + strings.Join(ts, ",")
}''', '''	var ts, failed []string
	for _, a := range p.Actions {
		ts = append(ts, a.Target)
	}
	// a group zfs refuses fails without a collected action: counting
	// actions alone reads a reconcile that fails the same way on every run
	// as idempotent (wave13 AN-01; HOLD-REPLAN, OWN-12)
	for _, sk := range p.Skipped {
		if strings.HasPrefix(sk.Reason, SkipFailed) {
			failed = append(failed, sk.Target)
		}
	}
	if len(failed) > 0 {
		return "OK collected=" + strings.Join(ts, ",") + " failed=" + strings.Join(failed, ",")
	}
	return "OK collected=" + strings.Join(ts, ",")
}''')
open(p, 'w').write(s)
