#!/usr/bin/env bash
set -euo pipefail
R=/home/user/empty/.claude/worktrees/agent-acf7494638986ab8a
D=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/split3
cd "$R"
s=$EPOCHREALTIME
"$D/endpoint.test" -test.list '.*' >"$D/l2"
m=$EPOCHREALTIME
ci/real-zfs-plan.sh "$D/l2" ci/real-zfs-weights.txt 2 3 >"$D/p2" 2>/dev/null
e=$EPOCHREALTIME
w=$D/work/syn
c=$EPOCHREALTIME
ci/check-split.sh TestPropertyRoundTrip "$w/endpoint.list" "$w/endpoint.plan" 1/2 "$w"/endpoint.{3,4,5}.log >/dev/null
f=$EPOCHREALTIME
echo "list $(echo "$m - $s" | bc)s, plan $(echo "$e - $m" | bc)s, check-split $(echo "$f - $c" | bc)s"
