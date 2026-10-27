import os
os.chdir('/home/user/empty/.claude/worktrees/agent-ad45c3ab7d0b822f7')
p='internal/endpoint/reconcile_fuzz_test.go'
s=open(p).read()
old='''		for _, run := range []ReconcilePlan{first, second} {
			// volumes this run collected, by the origin they release
			released := map[string]bool{}
			for _, a := range run.Actions {
				if r, ok := before.Get(a.Target); ok && r.Type != zfs.TypeSnapshot {
					released[r.Origin] = true
				}
			}
'''
new='''		// origins an undesired clone of ours proves were cut for it: under
		// the parent, from a volume of ours, named for that clone
		released := map[string]bool{}
		for r := range before.Volumes() {
			src, name, ok := strings.Cut(strings.TrimPrefix(r.Origin, parent+"/"), "@")
			if s, sok := before.Get(src); ok && sok && s.OwnedVolume() && r.OwnedVolume() && !isWanted[r.ID] &&
				strings.HasPrefix(r.Origin, parent+"/") && name == cloneSourceSnapshotPrefix+r.ID {
				released[r.Origin] = true
			}
		}
		for _, run := range []ReconcilePlan{first, second} {
'''
assert s.count(old)==1
s=s.replace(old,new)
open(p,'w').write(s)
