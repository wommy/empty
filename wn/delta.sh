#!/usr/bin/env bash
# rows 0.00 before and above 0.00 now; and the largest changes
set -euo pipefail
wn=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wn
new=/home/user/empty/.claude/worktrees/agent-aa3b18c2bedd0e0b7/ci/real-zfs-weights.txt
echo "== was 0.00, now > 0"
awk 'FNR == NR { if ($0 !~ /^#/ && NF == 2) o[$1] = $2; next } $0 !~ /^#/ && NF == 2 && ($1 in o) && o[$1] + 0 == 0 && $2 + 0 > 0 { print $1, o[$1], "->", $2 }' "$wn/old.weights" "$new"
echo "== skips listed for vm"
awk 'FNR == NR { if ($0 !~ /^#/ && NF == 2) o[$1] = $2; next } $0 !~ /^#/ && NF == 2 && ($1 in o) && o[$1] + 0 == 0 && $2 + 0 > 0 { print $1 }' "$wn/old.weights" "$new" >"$wn/drift.txt"
awk 'FNR == NR { d[$1] = 1; next } { for (i = 1; i <= NF; i++) { t = $i; sub(/\/.*/, "", t); if (t in d) print } }' "$wn/drift.txt" /home/user/empty/.claude/worktrees/agent-aa3b18c2bedd0e0b7/ci/expected-skips.txt
echo "== largest changes"
awk 'FNR == NR { if ($0 !~ /^#/ && NF == 2) o[$1] = $2; next } $0 !~ /^#/ && NF == 2 { d = $2 - o[$1]; printf "%s %.2f -> %.2f (%+.2f)\n", $1, o[$1], $2, d }' "$wn/old.weights" "$new" |
  awk '{ x = $5; gsub(/[()+]/, "", x); a = x < 0 ? -x : x; print a, $0 }' | sort -k1,1gr | awk 'NR <= 14 { $1 = ""; print }'
