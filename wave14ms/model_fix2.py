p = '/home/user/empty/.claude/worktrees/agent-a5e3d463acd0910f8/internal/endpoint/modelrun_test.go'
s = open(p).read()


def rep(a, b):
    global s
    assert s.count(a) == 1, a
    s = s.replace(a, b)


rep('''func TestModel(t *testing.T) {
	real := os.Getenv("ZFS_TEST_POOL") != ""
	seqs, n := envInt("MODEL_SEQS", 400), envInt("MODEL_LEN", 40)
	if real {
		budget := envInt("MODEL_BUDGET", 200)
		n = min(n, 25)
		seqs = max(1, budget/n)
	}
	if testing.Short() {
		seqs = min(seqs, 50)
	}
	seed0 := uint64(envInt("MODEL_SEED0", 1))
	if s := os.Getenv("MODEL_SEED"); s != "" {
		v, _ := strconv.ParseUint(s, 10, 64)
		seed0, seqs = v, 1
	}
	for _, k := range knownOpens {
		if exp, err := time.Parse(time.DateOnly, k.expires); err != nil || time.Now().After(exp) {
			t.Errorf("mask %s (%s) expired %s: fix the divergence, or renew it in review", k.id, k.row, k.expires)
		}
	}
	workers := envInt("MODEL_WORKERS", 8)
	var mu sync.Mutex
	var first *failure
	firstSeed := uint64(0)
	masks := map[string]int{}
	steps := 0''', '''//
// The PR tier (ADR 0005) runs 64 sequences: about 1 s, 4 s under -race, on
// 4 cores; the nightly sweep (model_nightly_test.go) runs 20,000. On a real
// pool it runs MODEL_BUDGET (75) steps, at most 25 per sequence.
func TestModel(t *testing.T) {
	checkMaskExpiry(t)
	seqs, n := envInt("MODEL_SEQS", 64), envInt("MODEL_LEN", 40)
	if os.Getenv("ZFS_TEST_POOL") != "" {
		n = min(n, 25)
		seqs = max(1, envInt("MODEL_BUDGET", 75)/n)
	}
	if testing.Short() {
		seqs = min(seqs, 16)
	}
	seed0 := uint64(envInt("MODEL_SEED0", 1))
	if s := os.Getenv("MODEL_SEED"); s != "" {
		v, _ := strconv.ParseUint(s, 10, 64)
		seed0, seqs = v, 1
	}
	modelSweep(t, seqs, n, seed0)
}

// checkMaskExpiry: a mask is a known divergence with a deadline, never a
// permanent exemption.
func checkMaskExpiry(t *testing.T) {
	for _, k := range knownOpens {
		if exp, err := time.Parse(time.DateOnly, k.expires); err != nil || time.Now().After(exp) {
			t.Errorf("mask %s (%s) expired %s: fix the divergence, or renew it in review", k.id, k.row, k.expires)
		}
	}
}

// modelSweep runs sequences seed0 .. seed0+seqs-1 of n steps on
// MODEL_WORKERS workers, and reports the lowest failing seed, shrunk.
func modelSweep(t *testing.T, seqs, n int, seed0 uint64) {
	workers := envInt("MODEL_WORKERS", 8)
	var mu sync.Mutex
	var first *failure
	firstSeed := uint64(0)
	steps := 0''')
rep('''					g := newRig(t, fmt.Sprintf("s%d", seed))
					r := &runner{}
					_ = r
					f := runTrace(t, g, trace)''', '''					f := runTrace(t, newRig(t, fmt.Sprintf("s%d", seed)), trace)''')
rep('''	_ = masks
	if testing.Verbose() {''', '''	if testing.Verbose() {''')
rep('''		if fl := runTrace(t, newRig(t, "fuzz"), trace); fl != nil {''', '''		// the simulator only: the fuzzer explores the generator's space, and
		// the seed corpus must not cost the real-pool leg a dataset
		if fl := runTrace(t, newSimRig(t), trace); fl != nil {''')
# masks-still-bite moves to the nightly file
i = s.index('// TestModelMasksStillBite (nightly: MODEL_NIGHTLY=1)')
s = s[:i].rstrip() + '\n'
# real-pool parent: one dataset per sequence, nothing left behind
rep('''func newRig(tb testing.TB, worker string) *rig {
	ctx := context.Background()
	g := &rig{tb: tb}
	var base zfs.Runner
	if pool := os.Getenv("ZFS_TEST_POOL"); pool != "" {
		base = zfs.LocalRunner{}
		g.parent = pool + "/model-" + realRun + "/" + worker''', '''func newRig(tb testing.TB, worker string) *rig { return newRigOn(tb, worker, os.Getenv("ZFS_TEST_POOL")) }

// newSimRig is a rig on the simulator wherever the test runs.
func newSimRig(tb testing.TB) *rig { return newRigOn(tb, "sim", "") }

func newRigOn(tb testing.TB, worker, pool string) *rig {
	ctx := context.Background()
	g := &rig{tb: tb}
	var base zfs.Runner
	if pool != "" {
		base = zfs.LocalRunner{}
		g.parent = pool + "/model-" + realRun + "-" + worker''')
rep('''	} else if os.Getenv("ZFS_REQUIRE_REAL") != "" {
		tb.Fatal("ZFS_REQUIRE_REAL is set but ZFS_TEST_POOL is not")
	} else {
		g.sim = zfstest.New("tank")''', '''	} else if os.Getenv("ZFS_REQUIRE_REAL") != "" && worker != "sim" {
		tb.Fatal("ZFS_REQUIRE_REAL is set but ZFS_TEST_POOL is not")
	} else {
		g.sim = zfstest.New("tank")''')
rep('''//	go test ./internal/endpoint -run TestModel                       # sim, MODEL_SEQS x MODEL_LEN''',
    '''//	go test ./internal/endpoint -run TestModel                       # sim, MODEL_SEQS (64) x MODEL_LEN (40)
//	go test -tags nightly ./internal/endpoint -run TestExploreModel  # 20k-sequence sweep, masks still bite''')
open(p, 'w').write(s)
