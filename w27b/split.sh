#!/usr/bin/env bash
# check-split's static half on the new plan (the log half needs a run: its
# failure on empty logs is expected here)
set -eo pipefail
W=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/w27b
R=/home/user/empty/.claude/worktrees/agent-affdea78cb48aa142
mkdir -p "$W/fake"
for i in 0 1 2; do : >"$W/fake/endpoint.$i.log"; done
cd "$R"
ci/check-split.sh TestPropertyRoundTrip "$W/endpoint.list" "$W/plan-after.txt" 0/2 "$W"/fake/endpoint.{0,1,2}.log || echo "(log half failed as expected on empty logs: rc=$?)"
# a stale plan must be refused
ci/check-split.sh TestPropertyRoundTrip "$W/endpoint.list" "$W/plan-before.txt" 0/2 "$W"/fake/endpoint.{0,1,2}.log >"$W/stale.out" 2>&1 || echo "stale plan refused: $(awk 'NR == 1' "$W/stale.out")"
