#!/usr/bin/env bash
R=/home/user/empty/.claude/worktrees/agent-a1791a32ddfa56c19
D=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/skiptest
mkdir -p $D
printf '=== RUN   TestA\n--- PASS: TestA (0.00s)\n=== RUN   TestReconcileLocksTargets\n    x_test.go:1: needs the simulator\n--- SKIP: TestReconcileLocksTargets (0.00s)\nPASS\n' > $D/ok.log
printf '%s\n' '--- PASS: TestA (0.00s)' '--- SKIP: TestFileExportLifecycle (0.00s)' '    export_contract_test.go:24: needs a running NFS server' '    --- SKIP: TestX/sub (0.00s)' > $D/bad.log
: > $D/empty.log
for l in ok bad empty; do echo "## $l"; $R/ci/check-skips.sh vm endpoint $D/$l.log; echo "rc=$?"; done
rm -f $D/sum
echo "## csi"; GITHUB_STEP_SUMMARY=$D/sum $R/ci/check-skips.sh noble csi $D/ok.log; echo rc=$?; cat $D/sum
