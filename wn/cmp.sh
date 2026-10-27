#!/usr/bin/env bash
# compare named tests' rows in the new and old weights
new=/home/user/empty/.claude/worktrees/agent-aa3b18c2bedd0e0b7/ci/real-zfs-weights.txt
old=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wn/old.weights
for t in "$@"; do
  echo "$t: new=$(awk -v t="$t" '$1==t{printf "%s ",$2}' "$new") old=$(awk -v t="$t" '$1==t{printf "%s ",$2}' "$old")"
done
