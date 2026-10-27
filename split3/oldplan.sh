#!/usr/bin/env bash
# old guest shard() vs new plan 1x3
set -euo pipefail
R=/home/user/empty/.claude/worktrees/agent-acf7494638986ab8a
D=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/split3
awk '/^nfs=/,/^ep_shards=/' "$D/t-oldguest.sh" | sed '$d' >"$D/oldfn.sh"
. "$D/oldfn.sh"
shard "$D/endpoint.list" "$R/ci/real-zfs-weights.txt" 3 | sort >"$D/old3"
"$R/ci/real-zfs-plan.sh" "$D/endpoint.list" "$R/ci/real-zfs-weights.txt" 1 3 2>/dev/null | sort >"$D/new13"
cmp "$D/old3" "$D/new13" && echo "1x3 plan identical to the old 3-way guest plan ($(wc -l <"$D/new13") lines)"
