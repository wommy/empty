import os
os.chdir('/home/user/empty/.claude/worktrees/agent-ad45c3ab7d0b822f7')
def sub(p, pairs):
    s=open(p).read()
    for old,new in pairs:
        assert s.count(old)==1, (p, old)
        s=s.replace(old,new)
    open(p,'w').write(s)

sub('internal/endpoint/reconcile.go', [
('''	// clone-source snapshots whose clone is gone, on volumes that stay
	for r := range cur.All() {
		vol, name, ok := strings.Cut(r.ID, "@")
		if !ok || !strings.HasPrefix(name, cloneSourceSnapshotPrefix) || r.SnapshotName != "" || r.Deferred {
			continue
		}
		if r.Managed {
			continue // a CSI snapshot by ownership, whatever its name
		}
		if hasAny(cur.ClonesOf(r.Name)) {
			continue // in use
		}
		if src, ok := cur.Get(vol); !ok || !src.OwnedVolume() {
			skip(r.ID, SkipUnmanagedSource)
			continue
		}
		if why := tooYoung(r, d); why != "" {
			skip(r.ID, why)
			continue
		}
		vols := []string{vol}
		if clone := strings.TrimPrefix(name, cloneSourceSnapshotPrefix); clone != "" {
			vols = append(vols, clone) // CreateVolume(clone) holds this while cutting it
		}
''','''	// clone-source snapshots whose clone is gone, on volumes that stay.
	// Only ones the endpoint stamped at creation: the reserved name alone is
	// not ownership. Unstamped ones (an administrator's, another tool's, or
	// democratic-csi's, which cuts them bare) are reported, never collected;
	// a legacy one is still released with the clone it serves, where the
	// clone's origin proves it ours (PlanDeleteVolume).
	for r := range cur.All() {
		vol, name, ok := strings.Cut(r.ID, "@")
		if !ok || !strings.HasPrefix(name, cloneSourceSnapshotPrefix) || r.SnapshotName != "" || r.Deferred {
			continue
		}
		if hasAny(cur.ClonesOf(r.Name)) {
			continue // in use
		}
		clone, ours := r.OwnedCloneSource()
		if !ours {
			skip(r.ID, SkipUnstampedCloneSource)
			continue
		}
		if src, ok := cur.Get(vol); !ok || !src.OwnedVolume() {
			skip(r.ID, SkipUnmanagedSource)
			continue
		}
		// a name zfs accepted but the client refuses to address would fail
		// at apply on every run: report it instead
		if _, err := volumeID(clone); err != nil || !zfs.ValidSnapshot(r.Name) {
			skip(r.ID, SkipUnaddressable)
			continue
		}
		if why := tooYoung(r, d); why != "" {
			skip(r.ID, why)
			continue
		}
		// CreateVolume(clone) holds vol:<clone> while cutting it
		vols := []string{vol, clone}
'''),
('''	SkipUnmanagedSource  = "clone-source snapshot on an unmanaged volume"''','''	SkipUnmanagedSource  = "clone-source snapshot on an unmanaged volume"
	// SkipUnstampedCloneSource: a snapshot with the reserved clone-source
	// name but without the endpoint's stamp (managed_resource and
	// clone_source_for set locally at creation) is not the endpoint's.
	SkipUnstampedCloneSource = "clone-source name without the endpoint's stamp: not ours"
	SkipUnaddressable        = "name the endpoint cannot address"'''),
])
