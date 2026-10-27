#!/usr/bin/env bash
# bash -n and shellcheck on ci/*.sh, actionlint on the workflows
cd /home/user/empty/.claude/worktrees/agent-a53361ecab5fd8058 || exit 1
rc=0
for f in ci/*.sh; do bash -n "$f" || { echo "bash -n FAIL $f"; rc=1; }; done
if command -v shellcheck >/dev/null; then shellcheck -S warning ci/*.sh || rc=1; else echo "no shellcheck"; fi
/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/bin/actionlint || rc=1
mawk -f ci/test-times.awk /dev/null || rc=1
echo "lint rc=$rc"
exit $rc
