#!/usr/bin/env bash
# predicted per-part fork work of the old and new plans, under each work model
set -euo pipefail
repo=/home/user/empty/.claude/worktrees/agent-aa3b18c2bedd0e0b7
wn=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wn
wl=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave30/logs
bash "$repo/ci/real-zfs-plan.sh" "$wn/endpoint.list" "$wn/old.weights" 2 3 >"$wn/old.plan" 2>"$wn/old.load"
bash "$repo/ci/real-zfs-plan.sh" "$wn/endpoint.list" "$repo/ci/real-zfs-weights.txt" 2 3 >"$wn/new.plan" 2>"$wn/new.load"
for p in old new; do
  echo "== $p plan: $(tr '\n' ' ' <"$wn/$p.load")"
  for m in "$wl/sh/work.txt" "$wl/work-632.txt" "$wl/work-old.txt"; do
    printf '  %-14s ' "$(basename "$(dirname "$m")")/$(basename "$m")"
    awk -f "$wl/../bin/pred.awk" "$m" "$wn/$p.plan"
  done
  echo "  part 0: $(awk '$1 < 3 && $2 != "TestPropertyRoundTrip" { print $2 }' "$wn/$p.plan" | sort | awk -v w="$wn/../wave30/logs/sh/work.txt" 'BEGIN { while ((getline l < w) > 0) { split(l, f, " "); x[f[1]] = f[2] } } x[$1] >= 100 { printf "%s(%d) ", $1, x[$1] } END { print "" }')"
done
