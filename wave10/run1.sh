#!/usr/bin/env bash
# usage: run1.sh <label> <extra go test flags...>   (cold GOCACHE only if COLD=1)
set -u
S=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave10
W=/home/user/empty/.claude/worktrees/agent-a3777e1ab243b4e78
export GOTOOLCHAIN=go1.27.3
label=$1; shift
cd "$W"
if [ "${COLD:-0}" = 1 ]; then export GOCACHE=$S/gocache-cold-$label; rm -rf "$GOCACHE"; fi
start=$(date +%s.%N)
go test -json -count=1 "$@" ./... > "$S/$label.json" 2> "$S/$label.err"
rc=$?
end=$(date +%s.%N)
echo "$label rc=$rc wall=$(echo "$end - $start" | bc)" | tee -a "$S/walls.txt"
