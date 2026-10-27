p='/home/user/empty/.claude/worktrees/agent-ae7e48e54556f40be/internal/endpoint/golden_plan_test.go'
s=open(p).read()
old='''	{"restore/unknown-source", []Row{vol("src", nil)}, create(CreateVolumeRequest{Name: "r", SourceSnapshotID: "src@nope"})},'''
new='''	// a filesystem snapshot's recorded size, not its source's grown one;
	// one taken before snapshot_size falls back to the source's
	{"restore/fs-expanded-since", []Row{vol("src", func(r *Row) { r.Refquota = 4 << 20 }),
		snap("src@s", func(r *Row) { r.SnapshotSize, r.SnapshotSizeSet = 1<<20, true })},
		create(CreateVolumeRequest{Name: "r", CapacityBytes: 2 << 20, SourceSnapshotID: "src@s"})},
	{"restore/fs-legacy-snapshot", []Row{vol("src", func(r *Row) { r.Refquota = 4 << 20 }), snap("src@s", nil)},
		create(CreateVolumeRequest{Name: "r", CapacityBytes: 2 << 20, SourceSnapshotID: "src@s"})},
''' + old
assert old in s
if 'fs-expanded-since' not in s:
    s=s.replace(old,new,1)
old='''	{"snapshot/long-name", []Row{vol("a", nil)}, snapshot(strings.Repeat("n", 128), "a")},'''
new=old+'''
	{"snapshot/fs-sized", []Row{vol("a", func(r *Row) { r.Refquota = 1 << 30 })}, snapshot("s", "a")},
	{"snapshot/zvol", []Row{vol("z", zvol(1<<30))}, snapshot("s", "z")},'''
if 'snapshot/fs-sized' not in s:
    s=s.replace(old,new,1)
open(p,'w').write(s)
