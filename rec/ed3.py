import os
os.chdir('/home/user/empty/.claude/worktrees/agent-ad45c3ab7d0b822f7')
def sub(p, pairs):
    s=open(p).read()
    for old,new in pairs:
        assert s.count(old)==1, (p, old)
        s=s.replace(old,new)
    open(p,'w').write(s)

sub('internal/endpoint/endpoint.go', [
('''		n.snapshotName(), n.snapshotSource(), n.nfsClients()}''',
 '''		n.snapshotName(), n.snapshotSource(), n.nfsClients(), n.cloneSourceFor()}'''),
('''// nfsClients is the endpoint's own property''',
 '''// cloneSourceFor is the endpoint's own property (not the baseline's): set
// locally on the internal snapshot a volume-to-volume clone hangs off, at
// its creation (`zfs snapshot -o`), naming the clone it was cut for. With
// managed_resource it is what makes such a snapshot the endpoint's; the
// reserved name alone is not ownership (anyone can `zfs snapshot` it).
func (n props) cloneSourceFor() string { return n.p + ":clone_source_for" }

// nfsClients is the endpoint's own property'''),
])

sub('internal/endpoint/table.go', [
('''	SnapshotName   string
	SnapshotSource string
}''','''	SnapshotName   string
	SnapshotSource string
	CloneSourceFor string // internal clone-source snapshots: the clone's id
}'''),
('''		n.snapshotName(), n.snapshotSource(), n.nfsClients(),
	}, propNames[:]...)''','''		n.snapshotName(), n.snapshotSource(), n.nfsClients(), n.cloneSourceFor(),
	}, propNames[:]...)'''),
('''	cShare, cMounted, cClients := j("sharenfs"), j("mounted"), j(n.nfsClients())''',
 '''	cShare, cMounted, cClients := j("sharenfs"), j("mounted"), j(n.nfsClients())
	cCloneFor := j(n.cloneSourceFor())'''),
('''			SnapshotSource: own(cSnapSrc),''','''			SnapshotSource: own(cSnapSrc),
			CloneSourceFor: own(cCloneFor),'''),
('''// Table is an immutable''','''// OwnedCloneSource reports whether r is an internal clone-source snapshot
// the endpoint cut, and for which clone: named
// "<vol>@volume-source-for-volume-<clone>" and stamped at creation with
// managed_resource and clone_source_for=<clone>, both set locally. A
// snapshot that only carries the reserved name is someone else's (an
// administrator's, another tool's, or one democratic-csi cut unstamped).
func (r Row) OwnedCloneSource() (string, bool) {
	_, name, isSnap := strings.Cut(r.ID, "@")
	clone, ok := strings.CutPrefix(name, cloneSourceSnapshotPrefix)
	return clone, isSnap && ok && clone != "" && r.Type == zfs.TypeSnapshot &&
		r.Managed && r.SnapshotName == "" && r.CloneSourceFor == clone
}

// Table is an immutable'''),
])
