p = '/home/user/empty/.claude/worktrees/agent-a5e3d463acd0910f8/internal/zfs/zfstest/sim.go'
s = open(p).read()


def rep(a, b):
    global s
    assert s.count(a) == 1, a
    s = s.replace(a, b)


rep('''// modelled: rename, destroy -R, -n,''', '''// modelled: destroy -R, -n,''')
rep('''	// (see mount).
	mounted bool
}''', '''	// (see mount).
	mounted bool
	guid    uint64 // assigned on first read; kept across rename (sim_ext.go)
}''')
rep('''	dropHosts map[string]bool // clients exportfs leaves out of etab
	txg       uint64
}''', '''	dropHosts map[string]bool // clients exportfs leaves out of etab
	txg       uint64
	guids     uint64 // last guid handed out (sim_ext.go)
}''')
rep('''	case "release":
		return nil, s.hold(args[1:], false)
	}''', '''	case "release":
		return nil, s.hold(args[1:], false)
	case "rename":
		return nil, s.rename(args[1:])
	}''')
rep('''	case "all", "name", "type", "origin", "creation", "used", "userrefs":''',
    '''	case "all", "name", "type", "origin", "creation", "used", "userrefs", "guid":''')
rep('''	case "userrefs":
		if n.typ != zfs.TypeSnapshot {''', '''	case "guid":
		return s.guid(n), "-", true
	case "userrefs":
		if n.typ != zfs.TypeSnapshot {''')
open(p, 'w').write(s)
