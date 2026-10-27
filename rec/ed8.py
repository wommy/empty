import os
os.chdir('/home/user/empty/.claude/worktrees/agent-ad45c3ab7d0b822f7')
p='internal/endpoint/plan_test.go'
s=open(p).read()
old=s[s.index('	plan, err := pl.PlanDeleteVolume(tab, "c")\n'):s.index('	if plan, err := pl.PlanDeleteVolume(tab, "gone")')]
new='''	// the released origin first (deferred), then the volume, never -r
	plan, err := pl.PlanDeleteVolume(tab, "c")
	if err != nil || len(plan) != 2 || !plan[0].Defer || plan[0].Target != "tank/v/b@volume-source-for-volume-c" ||
		plan[1].Target != "tank/v/c" || plan[1].Recursive || plan[1].Defer {
		t.Fatalf("clone delete %+v %v", plan, err)
	}
	// a deferred (already deleted) snapshot does not block its volume; it
	// is destroyed by name, plainly, before the volume
	if plan, err := pl.PlanDeleteVolume(tab, "d"); err != nil || len(plan) != 2 ||
		plan[0].Target != "tank/v/d@x" || plan[0].Defer || plan[1].Target != "tank/v/d" || plan[1].Recursive {
		t.Fatalf("deferred %+v %v", plan, err)
	}
'''
s=s.replace(old,new)
open(p,'w').write(s)

p='internal/endpoint/reconcile_test.go'
s=open(p).read()
for old,new in [
('''			acts: []string{
				"c:" + ReasonUndesired,
				"b@volume-source-for-volume-c:" + ReasonReleasedWithVolume,
				"b:" + ReasonUndesired,
			},''','''			acts: []string{
				"b@volume-source-for-volume-c:" + ReasonReleasedWithVolume,
				"c:" + ReasonUndesired,
				"b:" + ReasonUndesired,
			},'''),
('''				snap("b@volume-source-for-volume-gone", stamp),
			},
			want: desired("a"),
			acts: []string{"b:" + ReasonUndesired},''','''				snap("b@volume-source-for-volume-gone", stamp),
			},
			want: desired("a"),
			acts: []string{"b@volume-source-for-volume-gone:" + ReasonWithVolume, "b:" + ReasonUndesired},'''),
('''			rows: []Row{vol("a", nil), vol("b", nil), snap("b@s", func(r *Row) { r.Deferred = true })},
			want: desired("a"),
			acts: []string{"b:" + ReasonUndesired},''','''			rows: []Row{vol("a", nil), vol("b", nil), snap("b@s", func(r *Row) { r.Deferred = true })},
			want: desired("a"),
			acts: []string{"b@s:" + ReasonWithVolume, "b:" + ReasonUndesired},'''),
('''				if strings.Contains(op.Target, "@") != op.Defer {
					t.Fatalf("op %+v: snapshots are destroyed deferred, volumes not", op)
				}''','''				if op.Recursive || op.Defer && !strings.Contains(op.Target, "@") {
					t.Fatalf("op %+v: never -r; only snapshots are destroyed deferred", op)
				}'''),
]:
    assert old in s, old; s=s.replace(old,new)
open(p,'w').write(s)

p='internal/endpoint/reconcile_contract_test.go'
s=open(p).read()
old='''	// the clone first, its source snapshot with it, then the freed source
	if !slices.Equal(acts, []string{"clone", "src@volume-source-for-volume-clone", "drop", "src"}) {'''
new='''	// the clone's source snapshot released (deferred) with the clone, then
	// the freed source
	if !slices.Equal(acts, []string{"src@volume-source-for-volume-clone", "clone", "drop", "src"}) {'''
assert old in s; s=s.replace(old,new)
open(p,'w').write(s)
