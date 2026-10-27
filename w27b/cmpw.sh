#!/usr/bin/env bash
# compare the regenerated weights with the reader's log-derived ones
set -eo pipefail
W=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad
R=/home/user/empty/.claude/worktrees/agent-affdea78cb48aa142
grep -v '^#' "$R/ci/real-zfs-weights.txt" | sort >"$W/w27b/new.s"
grep -v '^#' "$W/wave27/new-weights.txt" | sort >"$W/w27b/reader.s"
diff "$W/w27b/new.s" "$W/w27b/reader.s" && echo identical-to-reader
grep '^#' "$R/ci/real-zfs-weights.txt"
awk '!/^#/' "$R/ci/real-zfs-weights.txt" | awk 'NR <= 12'
