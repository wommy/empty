import sys
p=sys.argv[1]
s=open(p).read()
def rep(old,new):
    global s
    assert s.count(old)==1, old
    s=s.replace(old,new)
rep('''	// OpMount: `zfs mount` of a filesystem; already mounted is done
	// (OkExists).
	OpMount
)''','''	// OpMount: `zfs mount` of a filesystem; already mounted is done
	// (OkExists).
	OpMount
	// OpSetRoot: the owner and mode of a mounted filesystem's root
	// directory (Config.Root), Props uid, gid (decimal) and mode (octal),
	// each absent when left as it is, at Path, the mountpoint the plan's
	// scan read. Not a zfs call; idempotent by itself.
	OpSetRoot
)''')
rep('''	OpClone: "clone", OpSet: "set", OpDestroy: "destroy", OpMount: "mount",
}''','''	OpClone: "clone", OpSet: "set", OpDestroy: "destroy", OpMount: "mount", OpSetRoot: "set-root",
}''')
rep('''	OkMissing bool              // destroy: already gone is done
}''','''	OkMissing bool              // destroy: already gone is done
	Path      string            // OpSetRoot: the mountpoint
}''')
rep('''	server   string      // resolved: the request's, or the first advertised
	explicit bool        // the request named the server
}''','''	server   string      // resolved: the request's, or the first advertised
	explicit bool        // the request named the server
	root     rootAttrs   // the volume root's owner and mode (-1: as made)
}''')
rep('''	server := r.NFS.Server
	if server == "" {''','''	root, err := validRoot(r.NFS)
	if err != nil {
		return nil, err
	}
	server := r.NFS.Server
	if server == "" {''')
rep('''	return &nfsWant{clients: cs, server: server, explicit: r.NFS.Server != ""}, nil''','''	return &nfsWant{clients: cs, server: server, explicit: r.NFS.Server != "", root: root}, nil''')
rep('''			if p.RequireMounted && mountable(row) && !row.Mounted {
				// zfs create and clone exit 0 when their mount fails: mount
				// it now, right before the commit, so a failed mount leaves
				// the row uncommitted (a retry mounts again)
				plan = append(plan, Op{Kind: OpMount, Target: p.full(id), OkExists: true})
			}
			return append(plan, commit), nil''','''			if nfs.ownsRoot() && (!mountable(row) || !row.Mounted && !p.RequireMounted) {
				return nil, errorf(FailedPrecondition, "volume %s is not mounted (canmount=%s, mountpoint=%s): its root's owner and mode cannot be set", id, row.CanMount, row.Mount)
			}
			if p.RequireMounted && mountable(row) && !row.Mounted {
				// zfs create and clone exit 0 when their mount fails: mount
				// it now, right before the commit, so a failed mount leaves
				// the row uncommitted (a retry mounts again)
				plan = append(plan, Op{Kind: OpMount, Target: p.full(id), OkExists: true})
			}
			if nfs.ownsRoot() {
				// on every resume, not only the first: nothing records
				// that it ran, and it is idempotent, so a crash anywhere
				// before the commit is converged by applying it again
				plan = append(plan, Op{Kind: OpSetRoot, Target: p.full(id), Path: row.Mount, Props: nfs.root.props()})
			}
			return append(plan, commit), nil''')
rep('''	if p.RequireMounted && r.Kind == Filesystem {
		// whether the create (or clone) mounted is known only from a scan
		// after it: the commit is the resume's, which checks Mounted
		return plan, nil
	}''','''	if (p.RequireMounted || nfs.ownsRoot()) && r.Kind == Filesystem {
		// whether the create (or clone) mounted is known only from a scan
		// after it: the commit is the resume's, which checks Mounted, and
		// which sets the root's owner and mode on the mountpoint it read
		return plan, nil
	}''')
rep('''// nfsCompatible applies''','''// ownsRoot reports whether the export sets the volume root's owner or
// mode. Nil-safe.
func (w *nfsWant) ownsRoot() bool { return w != nil && w.root.set() }

// nfsCompatible applies''')
open(p,'w').write(s)
