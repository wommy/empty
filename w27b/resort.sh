#!/usr/bin/env bash
# re-sort the weights' data lines as the script does; then plan with the new weights
set -eo pipefail
W=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad
R=/home/user/empty/.claude/worktrees/agent-affdea78cb48aa142
f=$R/ci/real-zfs-weights.txt
{ grep '^#' "$f"; grep -v '^#' "$f" | sort -k2,2gr -k1,1; } >"$W/w27b/w.new"
cp "$W/w27b/w.new" "$f"
cd "$R"
ci/real-zfs-plan.sh "$W/w27b/endpoint.list" ci/real-zfs-weights.txt 2 3 >"$W/w27b/plan-after.txt" 2>"$W/w27b/loads-after.txt"
cat "$W/w27b/loads-after.txt"
# which part each heavy test lands in, before and after (shards 0-2 = part 0)
for t in TestCrashAtEveryMutationConverges TestZFSDiffReplay TestRealFacts TestModel TestNFSReplay TestReconcileAgainstDesiredSet TestCloneSourcesMustBeOwned TestDeleteVolumeScanIsSubtreeScoped TestDestroyNeverHitsAReplacedDataset TestFileExportCreate; do
  b=$(awk -v t="$t" '$2 == t { print $1 }' "$W/w27b/plan-before.txt")
  a=$(awk -v t="$t" '$2 == t { print $1 }' "$W/w27b/plan-after.txt")
  echo "$t shard $b -> $a"
done
# the old weights' plan, scored with the new (measured) weights: what the parts actually carried
awk 'FNR == NR { if (!/^#/ && NF == 2) w[$1] = $2; next }
  $2 == "TestPropertyRoundTrip" { l[int($1 / 3)] += w[$2] / 6; next }
  { l[int($1 / 3)] += ($2 in w) ? w[$2] : 0.1 }
  END { printf "old plan under measured weights: part 0 %.2f, part 1 %.2f (%.0f%%/%.0f%%)\n", l[0], l[1], 100 * l[0] / (l[0] + l[1]), 100 * l[1] / (l[0] + l[1]) }' "$f" "$W/w27b/plan-before.txt"
