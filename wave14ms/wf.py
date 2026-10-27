p = '/home/user/empty/.claude/worktrees/agent-a5e3d463acd0910f8/.github/workflows/nightly.yml'
s = open(p).read()


def rep(a, b):
    global s
    assert s.count(a) == 1, a
    s = s.replace(a, b)


rep('''  # The scale tests and the model-based FSM explorer. Contract with the test
  # code: they carry `//go:build nightly` and are named TestScale* and
  # TestExplore*, and take their budget from -timeout. Until such tests exist
  # this step runs nothing and passes.''', '''  # The scale tests and the recovery FSM explorer
  # (internal/endpoint/fsm_explore_test.go: TestExploreLifecycle). Contract
  # with the test code: they carry `//go:build nightly`, are named TestScale*
  # and TestExplore*, and take their budget from -timeout. A run in which no
  # such test passed (a lost build tag, a renamed test) fails: green with
  # "[no tests to run]" is not coverage (ci/check-explore-ran.sh).''')
rep('''        run: go test -tags nightly -count=1 -timeout 25m -run '^Test(Scale|Explore)' -v ./... | tee "$RUNNER_TEMP/explore.log"
      - name: file issue
        if: failure() && steps.explore.conclusion == 'failure\'''', '''        run: go test -tags nightly -count=1 -timeout 25m -run '^Test(Scale|Explore)' -v ./... | tee "$RUNNER_TEMP/explore.log"
      - name: explorer tests ran
        id: ran
        run: ci/check-explore-ran.sh "$RUNNER_TEMP/explore.log"
      - name: file issue
        if: failure() && (steps.explore.conclusion == 'failure' || steps.ran.conclusion == 'failure')''')
open(p, 'w').write(s)
