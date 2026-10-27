import sys
p=sys.argv[1]
s=open(p).read()
def rep(old,new):
    global s
    assert s.count(old)==1, old
    s=s.replace(old,new)
rep('''	// now, when set, is stamped as committed_at by a create's commit: the
	// caller's clock reading, so planning stays pure.
	now int64
}''','''	// now, when set, is stamped as committed_at by a create's commit: the
	// caller's clock reading, so planning stays pure.
	now int64
	// parentMount, when an absolute path, is the parent's mountpoint as
	// the caller read it: a new filesystem's mountpoint is pinned to
	// <parentMount>/<id>, set locally in the create (csi-nfs-design §4).
	parentMount string
}''')
rep('''			own["setuid"], own["exec"], own["devices"] = "off", "off", "off"
		}''','''			own["setuid"], own["exec"], own["devices"] = "off", "off", "off"
			// pinned where zfs would put it now: a PV holds the path as
			// volume_context share (and mountpoint), which must not follow
			// the parent if its mountpoint later moves (csi-nfs-design §4).
			// A native property, not state of our own. A parent mounted
			// nowhere (none, legacy) leaves it inherited.
			if path.IsAbs(p.parentMount) {
				own["mountpoint"] = path.Join(p.parentMount, id)
			}
		}''')
rep('''	"math"
	"slices"''','''	"math"
	"path"
	"slices"''')
open(p,'w').write(s)
