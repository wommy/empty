import os
os.chdir('/home/user/empty/.claude/worktrees/agent-ad45c3ab7d0b822f7')
def sub(p, pairs):
    s=open(p).read()
    for old,new in pairs:
        assert s.count(old)==1, (p, old)
        s=s.replace(old,new)
    open(p,'w').write(s)

sub('internal/endpoint/plan.go', [
('''		own[p.Names.sourceType()], own[p.Names.sourceID()] = srcType, r.SourceVolumeID
		plan = Plan{
			{Kind: OpSnapshot, Target: internal, OkExists: true},''','''		// a snapshot already holding the reserved name is a replay of our
		// own cut only when it carries our stamp for this clone; anything
		// else is someone's snapshot, which a clone would pin (ADR 0003)
		if s, ok := t.Get(srcID + "@" + cloneSourceSnapshotPrefix + id); ok {
			if clone, ours := s.OwnedCloneSource(); !ours || clone != id {
				return nil, errorf(AlreadyExists, "snapshot %s exists and was not cut by the endpoint for %s", s.ID, id)
			}
		}
		own[p.Names.sourceType()], own[p.Names.sourceID()] = srcType, r.SourceVolumeID
		plan = Plan{
			// stamped atomically with its creation: ownership is never a
			// second step that a crash could skip
			{Kind: OpSnapshot, Target: internal, OkExists: true, Props: map[string]string{
				p.Names.managed(): "true", p.Names.cloneSourceFor(): id,
			}},'''),
('''		if s.SnapshotName == "" && !s.Deferred && !internalCloneSource(s) {''','''		if _, ours := s.OwnedCloneSource(); s.SnapshotName == "" && !s.Deferred && !ours {'''),
('''// internalCloneSource reports whether s is the snapshot a volume-to-volume
// clone hangs off: cut by the endpoint, unmarked, under a reserved name
// (PlanCreateSnapshot refuses that prefix for CSI names).
func internalCloneSource(s Row) bool {
	_, name, _ := strings.Cut(s.ID, "@")
	return strings.HasPrefix(name, cloneSourceSnapshotPrefix)
}

''',''),
])
