p = '/home/user/empty/.claude/worktrees/agent-a5e3d463acd0910f8/internal/endpoint/fsm_explore_test.go'
s = open(p).read()


def rep(a, b):
    global s
    assert s.count(a) == 1, a
    s = s.replace(a, b)


rep('''func TestFSMExplore(t *testing.T) {
	cfg := fsmCfg{prefix: dcPrefix, kind: Filesystem, depth: 3}
	if v := os.Getenv("FSM_DEPTH"); v != "" {
		cfg.depth, _ = strconv.Atoi(v)
	}
	if os.Getenv("FSM_KIND") == "block" {
		cfg.kind = Block
	}
	if v := os.Getenv("FSM_PREFIX"); v != "" {
		cfg.prefix = v
	}
	if os.Getenv("FSM_DEPTH") == "" && testing.Short() {
		t.Skip()
	}
	cleanPrefix = cfg.prefix
''', '''// TestExploreLifecycle runs the explorer over both volume kinds and both
// property prefixes (the endpoint's own and democratic-csi adoption) to
// depth 4 (about 25 s on 4 cores), and fails on any finding. The subtests
// run one at a time: cleanRows reads the package-level cleanPrefix.
func TestExploreLifecycle(t *testing.T) {
	depth := 4
	if v := os.Getenv("FSM_DEPTH"); v != "" {
		depth, _ = strconv.Atoi(v)
	}
	kinds := []VolumeKind{Filesystem, Block}
	switch os.Getenv("FSM_KIND") {
	case "fs":
		kinds = kinds[:1]
	case "block":
		kinds = kinds[1:]
	}
	prefixes := []string{DefaultPropertyPrefix, dcPrefix}
	if v := os.Getenv("FSM_PREFIX"); v != "" {
		prefixes = []string{v}
	}
	for _, k := range kinds {
		for _, p := range prefixes {
			t.Run(fmt.Sprintf("%s/%s", k, p), func(t *testing.T) {
				exploreLifecycle(t, fsmCfg{prefix: p, kind: k, depth: depth})
			})
		}
	}
}

func exploreLifecycle(t *testing.T, cfg fsmCfg) {
	cleanPrefix = cfg.prefix
''')
rep('''	if out := os.Getenv("FSM_OUT"); out != "" {
		os.WriteFile(out, []byte(b.String()), 0o644)
	}
	t.Logf("%d findings", len(fs))
}''', '''	if out := os.Getenv("FSM_OUT"); out != "" {
		if err := os.WriteFile(fmt.Sprintf("%s-%s-%s.txt", out, cfg.kind, cfg.prefix), []byte(b.String()), 0o644); err != nil {
			t.Error(err)
		}
	}
	t.Logf("distinct worlds=%d edges=%d runs=%d, %d findings", len(seen), x.edges, x.runs, len(fs))
	// every finding is a bug in the endpoint, the simulator or the
	// classifier: an unnamed state, a foreign touch, a crash+retry that
	// diverges, a final code after a partial plan, an unsafe or
	// non-idempotent reconcile
	for i, f := range fs {
		if i == 20 {
			t.Errorf("... and %d more (FSM_OUT=<path> writes the full report)", len(fs)-i)
			break
		}
		t.Errorf("[%s] (depth %d) %s\\n  trace: %s", f.kind, f.depth, f.sig, f.trace)
	}
}''')
rep('''	case "create", "snapshot", "clone", "set", "destroy", "inherit", "promote", "rename":''',
    '''	case "create", "snapshot", "clone", "set", "destroy", "inherit", "promote", "rename", "hold", "release":''')
rep('''		{name: "X:dcsi-create(a)", oob: true,''', '''		// a backup tool pinning a snapshot (HOLD-REPLAN): the clone source
		// a clone of a leaves, or the CSI snapshot
		{name: "X:hold(a@*)", oob: true, x: func(s *zfstest.Sim, rows []Row, _ string) [][]string {
			for _, id := range []string{"a@" + cloneSourceSnapshotPrefix + "b", "a@s"} {
				if r, ok := get(rows, id); ok && r.Holds == 0 {
					return [][]string{{"hold", "backup", full(id)}}
				}
			}
			return nil
		}},
		// someone's dataset below a filesystem volume (OWN-12)
		{name: "X:child(a)", oob: true, x: func(s *zfstest.Sim, rows []Row, _ string) [][]string {
			if r, ok := get(rows, "a"); !ok || r.Type != zfs.TypeFilesystem || s.Has(full("a")+"/kid") {
				return nil
			}
			return [][]string{{"create", full("a") + "/kid"}}
		}},
		{name: "X:dcsi-create(a)", oob: true,''')
open(p, 'w').write(s)
