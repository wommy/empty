#!/usr/bin/env bash
# The new log path: job logs carrying a test-times.tsv group (as ci/real-zfs-vm.sh
# now prints it), built from the reader's TSVs; regenerate from them and compare
# with the shard-log path's result. Restores the committed weights after.
set -eo pipefail
W=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad
R=/home/user/empty/.claude/worktrees/agent-affdea78cb48aa142
S=$W/w27b/synth
mkdir -p "$S/a" "$S/b"
mk() { # <tsv> <run> <part> <out>
  {
    printf '\357\273\2772026-10-27T17:19:01.4691427Z ##[group]Runner Image Provisioner\n'
    echo "2026-10-27T17:19:01.4698407Z ##[endgroup]"
    echo "2026-10-27T17:19:33.1000000Z ##[group]test-times.tsv"
    echo "2026-10-27T17:19:33.1000000Z # shard test status parallel own_s cost_s wall_s subtests (ci/test-times.awk)"
    echo "2026-10-27T17:19:33.1000000Z # nproc=4 mode=pr run=$2.1 part=$3/2"
    awk '{ print "2026-10-27T17:19:33.1000001Z " $0 }' "$1"
    echo "2026-10-27T17:19:33.2000000Z ##[endgroup]"
  } >"$4"
}
# deliberately not one directory per run: the run= header must group the parts
mk "$W/wave27/tt/r90/ep0/test-times.tsv" 40549402887 0 "$S/a/r90-0.log"
mk "$W/wave27/tt/r90/ep1/test-times.tsv" 40549402887 1 "$S/b/r90-1.log"
mk "$W/wave27/tt/r89/ep0/test-times.tsv" 40548715833 0 "$S/b/r89-0.log"
mk "$W/wave27/tt/r89/ep1/test-times.tsv" 40548715833 1 "$S/a/r89-1.log"
cp "$R/ci/real-zfs-weights.txt" "$S/keep.txt"
cp "$W/w27b/endpoint.list" "$S/endpoint.list"
cd "$R"
git show HEAD:ci/real-zfs-weights.txt >ci/real-zfs-weights.txt
rc=0
ci/real-zfs-weights.sh -s x -l "$S/endpoint.list" "$S/a/r90-0.log" "$S/b/r90-1.log" "$S/b/r89-0.log" "$S/a/r89-1.log" || rc=$?
grep -v '^#' ci/real-zfs-weights.txt | sort >"$S/got"
cp "$S/keep.txt" ci/real-zfs-weights.txt
[ "$rc" = 0 ]
grep -v '^#' "$W/wave27/new-weights.txt" | sort >"$S/want"
diff "$S/got" "$S/want" && echo "tsv-group path: same weights as the shard-log path"
