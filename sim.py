import sys
p = sys.argv[1]
s = open(p).read()


def r(a, b):
    global s
    assert a in s, a
    s = s.replace(a, b, 1)


r("""// promote beyond the origin snapshot, holds, busy (open) zvols, -R, -n,
// native property inheritance, size suffixes; space accounting is
// approximate. CI runs the same contract tests on a real pool.""", """// promote beyond the origin snapshot, holds, busy (open) zvols, -R, -n,
// native property inheritance other than sharenfs/sharesmb, size suffixes;
// space accounting is approximate. CI runs the same contract tests on a
// real pool.
//
// NFS: a filesystem is mounted unless canmount is off/noauto or its
// mountpoint is none/legacy. Setting sharenfs on a mounted filesystem
// exports it, and ReadEtab renders those exports as /var/lib/nfs/etab.
// SetNFSDown makes exports silently stop changing, like libzfs when
// exportfs fails: zfs still exits 0.""")
r("""	// PoolSize is reported as available space at the pool root.
	PoolSize uint64
}""", """	// PoolSize is reported as available space at the pool root.
	PoolSize uint64

	live    map[string]string // dataset -> sharenfs value the "kernel" serves
	nfsDown bool
}""")
r("""	s := &Sim{ds: map[string]*node{}, PoolSize: 1 << 40}""",
  """	s := &Sim{ds: map[string]*node{}, PoolSize: 1 << 40, live: map[string]string{}}""")
r("""		return exit("cannot create '%s': 'refreservation=auto' only supported on volumes", name)
	}
	s.ds[name] = n
	return nil""", """		return exit("cannot create '%s': 'refreservation=auto' only supported on volumes", name)
	}
	s.ds[name] = n
	s.reshare(name)
	return nil""")
r("""	s.ds[name] = n
	return nil
}

func (s *Sim) children""", """	s.ds[name] = n
	s.reshare(name)
	return nil
}

func (s *Sim) children""")
r("""	origin := s.ds[name].origin
	delete(s.ds, name)""", """	origin := s.ds[name].origin
	delete(s.ds, name)
	if !s.nfsDown {
		delete(s.live, name) // destroy unmounts, and unmount unshares
	}""")
r("""		n.props[p[0]] = p[1]
	}
	return nil
}""", """		n.props[p[0]] = p[1]
	}
	s.reshare(name)
	return nil
}""")
r("""	delete(n.props, prop)
	return nil
}""", """	delete(n.props, prop)
	s.reshare(name)
	return nil
}""")
r("""	"sharenfs": "off", "sharesmb": "off",
}""", """	"sharenfs": "off", "sharesmb": "off", "canmount": "on",
}""")
r("""	"mountpoint":     {zfs.TypeFilesystem},""", """	"mountpoint":     {zfs.TypeFilesystem},
	"mounted":        {zfs.TypeFilesystem},
	"canmount":       {zfs.TypeFilesystem},""")
r("""	case "defer_destroy":
		if n.defer_ {""", """	case "mounted":
		if s.mounted(name) {
			return "yes", "-", true
		}
		return "no", "-", true
	case "defer_destroy":
		if n.defer_ {""")
r("""	if v, ok := n.props[prop]; ok {
		return v, "local", true
	}
	// inherited user properties
	if strings.Contains(prop, ":") {""", """	if v, ok := n.props[prop]; ok {
		return v, "local", true
	}
	// inherited user properties, and the share properties (PROP_INHERIT)
	if strings.Contains(prop, ":") || prop == "sharenfs" || prop == "sharesmb" {""")
r("""		return "-", "-", true
	}
	if prop == "volsize" && n.typ == zfs.TypeSnapshot {""", """		if v, ok := defaults[prop]; ok {
			return v, "default", true
		}
		return "-", "-", true
	}
	if prop == "volsize" && n.typ == zfs.TypeSnapshot {""")
s += open(sys.argv[2]).read()
open(p, 'w').write(s)
