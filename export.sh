S=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad
export PATH=$S/tc/golang.org/toolchain@v0.0.1-go1.27.3.linux-amd64/bin:$PATH GOTOOLCHAIN=local
W=/home/user/empty/.claude/worktrees/agent-a12cc5177ef7ae6a6
P=$S/wave10/ci-patches
mkdir -p $P
cd $W
git diff -- .github/workflows/ci.yml > $P/0001-ci-workflow-static-test-jobs.patch
git diff --no-index /dev/null .github/actions/setup-go/action.yml > $P/0002-setup-go-composite-action.patch
git diff --no-index /dev/null .github/workflows/nightly.yml > $P/0003-nightly-workflow.patch
ls -la $P
# verify the patches apply to a clean tree
rm -rf $S/applycheck && mkdir $S/applycheck && git archive HEAD | tar -x -C $S/applycheck
cd $S/applycheck
for f in $P/*.patch; do git apply --check -p1 "$f" && git apply -p1 "$f" && echo "applied $f"; done
GOFLAGS=-mod=mod go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7 -color=false .github/workflows/ci.yml .github/workflows/nightly.yml 2>&1 | tail -30
echo actionlint-exit=$?
