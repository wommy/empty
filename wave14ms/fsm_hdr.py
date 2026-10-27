p = '/home/user/empty/.claude/worktrees/agent-a5e3d463acd0910f8/internal/endpoint/fsm_explore_test.go'
s = open(p).read()


def rep(a, b):
    global s
    assert s.count(a) == 1, a
    s = s.replace(a, b)


rep('''// Exhaustive lifecycle explorer (wave5 FSM study). Not part of the product.
//''', '''// Exhaustive lifecycle explorer (wave5 FSM study), nightly tier (ADR 0005):
// TestExploreLifecycle, about 40 s on 4 cores at depth 4. The classifier and
// its table test are in fsm_classify_test.go and run on every PR.
//''')
rep('''// and from every reachable state (crash intermediates included) two
// reconciles (desired = {} and desired = every committed volume).''', '''// and from every reachable state (crash intermediates included) reconciles
// with desired = {}, = every committed volume, and = the first of them
// (so a committed volume is undesired). Out-of-band actions include a user
// hold and a dataset below a volume, read raw into each world's identity.''')
rep('''// a second reconcile plans nothing and changes nothing.
//
// Env: FSM_DEPTH (default 3), FSM_KIND (fs|block), FSM_PREFIX (property
// prefix; default democratic-csi), FSM_OUT (report file).''', '''// a second reconcile collects nothing, fails nothing and changes nothing.
// Any finding fails the test.
//
// Env: FSM_DEPTH (default 4), FSM_KIND (fs|block; default both),
// FSM_PREFIX (property prefix; default zfs-endpoint and democratic-csi),
// FSM_OUT (report path prefix; one file per kind and prefix).''')
open(p, 'w').write(s)
