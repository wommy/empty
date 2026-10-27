#!/usr/bin/env bash
# measure.sh <label>: endpoint zfs calls over the endpoint suite (simulator)
set -euo pipefail
d=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/scancut
out=$d/$1.tsv
rm -f "$out"
cd /home/user/empty/.claude/worktrees/agent-aec3b828c6a86e68b
ZFS_TEST_CALLS=$out go test -count=1 ./internal/endpoint >/dev/null
echo "$1: total=$(wc -l <"$out") endpoint=$(awk -F'\t' '$2=="endpoint"' "$out" | wc -l)"
