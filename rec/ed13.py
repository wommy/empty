import os
os.chdir('/home/user/empty/.claude/worktrees/agent-ad45c3ab7d0b822f7')
def sub(p, pairs):
    s=open(p).read()
    for old,new in pairs:
        assert s.count(old)==1, (p, old)
        s=s.replace(old,new)
    open(p,'w').write(s)

sub('internal/endpoint/reconcile.go', [
('''	SkipEmptyDesired    = "desired set is empty: undesired volumes are not collected"''',
 '''	SkipEmptyDesired    = "desired set names no existing volume: undesired volumes are not collected"'''),
('''	for _, id := range slices.Sorted(maps.Keys(want)) {
		if r, ok := t.Get(id); !ok || r.Type == zfs.TypeSnapshot {
			skip(id, SkipDesiredMissing)
		}
	}
''','''	// A set naming none of the endpoint's volumes says nothing about them:
	// a page of another list, another cluster's or another parent's set.
	// Like the empty set, it collects no committed volume (adv4 F2).
	stated := false
	for _, id := range slices.Sorted(maps.Keys(want)) {
		r, ok := t.Get(id)
		if !ok || r.Type == zfs.TypeSnapshot {
			skip(id, SkipDesiredMissing)
		}
		stated = stated || ok && r.OwnedVolume()
	}
'''),
('''			if len(want) == 0 {''','''			if !stated {'''),
('''	// VolumeIDs is the complete set of volumes the caller wants. Every owned
	// volume outside it is undesired. Empty means "no statement": only
	// uncommitted rows and clone-source snapshots are collected, because an
	// empty set cannot be told apart from a caller that sent nothing.''','''	// VolumeIDs is the complete set of volumes the caller wants. Every owned
	// volume outside it is undesired. Empty means "no statement": only
	// uncommitted rows and clone-source snapshots are collected, because an
	// empty set cannot be told apart from a caller that sent nothing. A set
	// in which no id names a volume the endpoint owns counts as empty.'''),
])
