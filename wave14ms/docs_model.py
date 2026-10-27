W = '/home/user/empty/.claude/worktrees/agent-a5e3d463acd0910f8/'


def edit(f, pairs):
    p = W + f
    s = open(p).read()
    for a, b in pairs:
        assert s.count(a) == 1, (f, a)
        s = s.replace(a, b)
    open(p, 'w').write(s)


edit('README.md', [('''The simulator injects failures (`Sim.Fail`) and received properties (`Sim.Receive`), and it prints index properties the way zfs 2.4 does.''',
 '''`TestModel` is a model-based test: random sequences of RPCs and out-of-band actions (snapshots, clones, child datasets, receives, renames, holds), with a kill, an error or a racing action injected at a plan step, run against the endpoint and a pure reference model of the contract (`model_test.go`). After every step it compares codes, results and the world read back with plain zfs, checks that nothing unowned changed and that a second reconcile plans nothing, and shrinks a failure to a replayable trace (`MODEL_SEED=<n>`). A PR runs 64 sequences on the simulator and 50 steps on the VM's pool; the nightly tier sweeps 20,000 sequences. A divergence that is known and not yet fixed is masked with a spec row and an expiry date: `TestModel` fails once a mask expires, and the nightly `TestExploreModelMasksStillBite` fails once a mask hides nothing. `TestRealFacts` checks the OpenZFS facts the model and the simulator share against the real pool, and the simulator against them.

The recovery FSM explorer (`fsm_explore_test.go`, nightly) walks every world two volumes reach in four steps, crashing, killing and racing every mutation, and fails on any unnamed state, foreign touch, divergent retry or non-idempotent reconcile.

The simulator injects failures (`Sim.Fail`) and received properties (`Sim.Receive`), and it prints index properties the way zfs 2.4 does.''')])
edit('ci/real-zfs-weights.txt', [('''TestNFSReplay 5.00
''', '''TestNFSReplay 5.00
#
# Estimates (model suite): TestModel runs MODEL_BUDGET=50 steps on the real
# pool, 2 sequences of 25 in parallel, about 6 zfs commands a step at
# 30-60 ms each; TestRealFacts is 16 short plain-zfs facts.
TestModel 5.00
TestRealFacts 1.50
''')])
