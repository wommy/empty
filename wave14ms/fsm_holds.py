W = '/home/user/empty/.claude/worktrees/agent-a5e3d463acd0910f8/internal/endpoint/'


def edit(f, pairs):
    p = W + f
    s = open(p).read()
    for a, b in pairs:
        assert s.count(a) == 1, (f, a)
        s = s.replace(a, b)
    open(p, 'w').write(s)


edit('fsm_classify_test.go', [
    ('''		if r.Deferred && !clones {
			return "IMPOSSIBLE(deferred without clones)"
		}
		if internal {''', '''		if r.Deferred && !clones && r.Holds == 0 {
			return "IMPOSSIBLE(deferred without clones or holds)"
		}
		if internal {'''),
    ('''	if r.Deferred {
		if !clones {
			return "IMPOSSIBLE(deferred without clones)"
		}
		return "CSI-snap-deferred"
	}''', '''	if r.Deferred {
		// zfs keeps a snapshot marked for deferred destroy while a clone
		// or a user hold pins it, and reaps it with the last of them
		if !clones && r.Holds == 0 {
			return "IMPOSSIBLE(deferred without clones or holds)"
		}
		return "CSI-snap-deferred"
	}'''),
])
edit('fsm_explore_test.go', [
    ('''		fmt.Fprintf(&b, "%s|%s|v%d|q%d|r%d|o%s|d%v|M%v|C%v|%s|%s|%s|%s|%s|%v|%s|%s;",
			r.ID, r.Type, r.Volsize, r.Refquota, r.Refres, strings.TrimPrefix(r.Origin, fsmParent+"/"), r.Deferred,''',
     '''		fmt.Fprintf(&b, "%s|%s|v%d|q%d|r%d|o%s|d%v|h%d|k%v|M%v|C%v|%s|%s|%s|%s|%s|%v|%s|%s;",
			r.ID, r.Type, r.Volsize, r.Refquota, r.Refres, strings.TrimPrefix(r.Origin, fsmParent+"/"), r.Deferred, r.Holds, r.Children,'''),
])
