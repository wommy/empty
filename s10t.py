p = '/home/user/empty/.claude/worktrees/agent-a5205796387710ae5/cmd/zfs-endpoint/startup_seams_test.go'
s = open(p).read()
old = '''		// S9: another endpoint holds the parent'''
new = '''		// a lock file name is NAME_MAX (255) at most
		{"long lock name", []string{"-parent", "tank/" + strings.Repeat("v", 250)}, nil, "255"},
		// S9: another endpoint holds the parent'''
assert old in s
s = s.replace(old, new)
old = '''		name  string
		props []map[string]string // one child of tank/k8s each
		args  []string
		code  int // 0: passes the check (then fails at the non-socket, 1)
	}{'''
new = '''		name   string
		props  []map[string]string // one child of tank/k8s each
		parent map[string]string   // set on tank/k8s, inherited below
		args   []string
		code   int // 0: passes the check (then fails at the non-socket, 1)
	}{'''
assert old in s
s = s.replace(old, new)
rows_old = s[s.index('		{"legacy only"'):s.index('	} {\n		d, sim := simDeps(t)')]
rows_new = '''		{"legacy only", []map[string]string{{legacy: "true"}, {legacy: "true"}}, nil, nil, exitConfig},
		{"no volumes", nil, nil, nil, 0},
		{"ours only", []map[string]string{{ours: "true"}}, nil, nil, 0},
		{"both", []map[string]string{{legacy: "true"}, {ours: "true"}}, nil, nil, 0},
		{"legacy false", []map[string]string{{legacy: "false"}}, nil, nil, 0},
		// inherited is not owned (ADR 0003)
		{"legacy inherited", []map[string]string{nil}, map[string]string{legacy: "true"}, nil, 0},
		{"adopted", []map[string]string{{legacy: "true"}}, nil, []string{"-property-prefix", "democratic-csi"}, 0},
		// an explicit prefix is the operator's choice, legacy volumes or not
		{"explicit default", []map[string]string{{legacy: "true"}}, nil, []string{"-property-prefix", "zfs-endpoint"}, 0},
'''
s = s.replace(rows_old, rows_new)
old = '''		z := zfs.New(sim)
		for i, p := range tc.props {'''
new = '''		z := zfs.New(sim)
		if tc.parent != nil {
			if err := z.Set(context.Background(), "tank/k8s", tc.parent); err != nil {
				t.Fatal(err)
			}
		}
		for i, p := range tc.props {'''
assert old in s
s = s.replace(old, new)
open(p, 'w').write(s)
