import os
os.chdir('/home/user/empty/.claude/worktrees/agent-ad45c3ab7d0b822f7')
def sub(p, pairs):
    s=open(p).read()
    for old,new in pairs:
        assert s.count(old)==1, (p, old)
        s=s.replace(old,new)
    open(p,'w').write(s)

sub('internal/endpoint/plan.go', [
('''	plan := Plan{{Kind: OpDestroy, Target: row.Name, Recursive: true, OkMissing: true}}
	if strings.Contains(row.Origin, "@"+cloneSourceSnapshotPrefix) {
		plan = append(plan, Op{Kind: OpDestroy, Target: row.Origin, Defer: true, OkMissing: true})
	}
	return plan, nil
}
''','''	plan := Plan{{Kind: OpDestroy, Target: row.Name, Recursive: true, OkMissing: true}}
	if origin, _, ok := p.releasedOrigin(t, row); ok {
		plan = append(plan, Op{Kind: OpDestroy, Target: origin.Name, Defer: true, OkMissing: true})
	}
	return plan, nil
}

// releasedOrigin is the internal clone-source snapshot that deleting row
// releases, and the volume it hangs off. It is the endpoint's only when
// all of these hold:
//   - the origin is exactly <parent>/<src>@volume-source-for-volume-<row id>:
//     cut for this clone, under the parent, from an addressable volume id
//   - src is a volume the endpoint owns
//   - the snapshot is stamped for this clone or, legacy (democratic-csi
//     cuts them bare), carries no ownership marker at all: the clone's
//     origin, recorded by zfs, is then the proof it was cut for this clone.
//     A snapshot stamped for another clone, or a CSI snapshot, is not.
func (p Planner) releasedOrigin(t Table, row Row) (Row, string, bool) {
	rel, ok := strings.CutPrefix(row.Origin, p.Parent+"/")
	if !ok {
		return Row{}, "", false
	}
	src, name, ok := strings.Cut(rel, "@")
	if !ok || name != cloneSourceSnapshotPrefix+row.ID || !zfs.ValidSnapshot(row.Origin) {
		return Row{}, "", false
	}
	if _, err := volumeID(src); err != nil || !t.Covers(src) {
		return Row{}, "", false
	}
	if s, ok := t.Get(src); !ok || !s.OwnedVolume() {
		return Row{}, "", false
	}
	snap, ok := t.Get(rel)
	if !ok || snap.Type != zfs.TypeSnapshot {
		return Row{}, "", false
	}
	clone, stamped := snap.OwnedCloneSource()
	legacy := !snap.Managed && snap.SnapshotName == "" && snap.CloneSourceFor == ""
	if !(stamped && clone == row.ID) && !legacy {
		return Row{}, "", false
	}
	return snap, src, true
}
'''),
])

sub('internal/endpoint/reconcile.go', [
('''				if i > 0 { // the released clone-source origin
					a.Reason = ReasonReleasedWithVolume
					src, _, _ := strings.Cut(a.Target, "@")
					a.Volumes = append(a.Volumes, src)
				}''','''				if i > 0 { // the released clone-source origin
					a.Reason = ReasonReleasedWithVolume
					row, _ := cur.Get(c.id)
					_, src, _ := p.releasedOrigin(cur, row)
					a.Volumes = append(a.Volumes, src)
				}'''),
])
