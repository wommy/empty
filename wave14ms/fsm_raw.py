p = '/home/user/empty/.claude/worktrees/agent-a5e3d463acd0910f8/internal/endpoint/fsm_explore_test.go'
s = open(p).read()


def rep(a, b):
    global s
    assert s.count(a) == 1, a
    s = s.replace(a, b)


rep('''type snapshotOf struct {
	rows    []Row
	t       Table
	classes map[string]string // full name -> state
}''', '''type snapshotOf struct {
	rows    []Row
	t       Table
	classes map[string]string // full name -> state
	raw     string            // rawExtra: what the scan may not see
}''')
rep('''	for _, r := range rows {
		cl[r.Name] = classify(r, t, prov, prefix)
	}
	return snapshotOf{rows, t, cl}
}''', '''	for _, r := range rows {
		cl[r.Name] = classify(r, t, prov, prefix)
	}
	return snapshotOf{rows, t, cl, rawExtra(s)}
}

// rawExtra reads, with plain zfs and not through the scan under test, what
// a world has that the scan may drop: datasets below a volume and user
// holds. It is part of a world's identity, so a scan that loses them
// cannot merge two worlds that zfs tells apart.
func rawExtra(s *zfstest.Sim) string {
	out, err := s.Run(context.Background(), "zfs", "get", "-Hp", "-r", "-o", "name,property,value,source",
		"-t", "filesystem,volume,snapshot", "userrefs", fsmParent)
	if err != nil {
		panic(err)
	}
	var b strings.Builder
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\\n") {
		f := strings.Split(l, "\\t")
		if len(f) < 3 {
			continue
		}
		id, _ := strings.CutPrefix(f[0], fsmParent+"/")
		vol, _, _ := strings.Cut(id, "@")
		if strings.Contains(vol, "/") || f[2] != "0" && f[2] != "-" {
			fmt.Fprintf(&b, "%s=%s;", id, f[2])
		}
	}
	return b.String()
}''')
rep('''			r.Local, r.ShareNFS, prov[r.Name])
	}
	return b.String()
}''', '''			r.Local, r.ShareNFS, prov[r.Name])
	}
	return b.String() + "|raw:" + o.raw
}''')
rep('''	for _, d := range [][]string{nil, desired} {''', '''	sets := [][]string{nil, desired}
	if len(desired) > 1 {
		// a proper subset: committed volumes outside it are undesired, the
		// only way a reconcile reaches a committed volume's snapshots and
		// children (HOLD-REPLAN, OWN-12)
		sets = append(sets, desired[:1])
	}
	for _, d := range sets {''')
open(p, 'w').write(s)
