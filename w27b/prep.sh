#!/usr/bin/env bash
# lay the reader's job logs out one directory per run; plan with the old weights
set -eo pipefail
W=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad
R=/home/user/empty/.claude/worktrees/agent-affdea78cb48aa142
mkdir -p "$W/w27b/40549402887" "$W/w27b/40548715833"
cp "$W/wave27/logs/r90_ep0.log" "$W/w27b/40549402887/ep0.log"
cp "$W/wave27/logs/r90_ep1.log" "$W/w27b/40549402887/ep1.log"
cp "$W/wave27/logs/r89_ep0.log" "$W/w27b/40548715833/ep0.log"
cp "$W/wave27/logs/r89_ep1.log" "$W/w27b/40548715833/ep1.log"
cd "$R"
ci/real-zfs-plan.sh "$W/w27b/endpoint.list" ci/real-zfs-weights.txt 2 3 >"$W/w27b/plan-before.txt" 2>"$W/w27b/loads-before.txt"
cat "$W/w27b/loads-before.txt"
