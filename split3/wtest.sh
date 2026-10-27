#!/usr/bin/env bash
# real-zfs-weights.sh: two parts of one run sum, two runs average
set -euo pipefail
R=/home/user/empty/.claude/worktrees/agent-acf7494638986ab8a
D=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/split3/wt
rm -rf "${D:?}" && mkdir -p "$D/ci" "$D/a" "$D/b" "$D/c"
cp "$R/ci/real-zfs-weights.sh" "$D/ci/"
printf '# <test> <seconds>\nTestOld 1.00\n' >"$D/ci/real-zfs-weights.txt"
T=$'\t'
row() { echo "$1${T}$2${T}PASS${T}0${T}$3${T}$3${T}$3${T}0"; }
{ echo "# shard ..."; echo "# nproc=4 mode=pr run=77.1 part=0/2"
  row endpoint.0 TestPropertyRoundTrip 1; row endpoint.1 TestPropertyRoundTrip 1; row endpoint.2 TestPropertyRoundTrip 1
  row endpoint.0 TestA 4; } >"$D/a/test-times.tsv"
{ echo "# shard ..."; echo "# nproc=4 mode=pr run=77.1 part=1/2"
  row endpoint.3 TestPropertyRoundTrip 1; row endpoint.4 TestPropertyRoundTrip 1; row endpoint.5 TestPropertyRoundTrip 1
  row endpoint.3 TestB 2; } >"$D/b/test-times.tsv"
# an older single-job run (no run= tag): 3 shards
{ echo "# shard ..."; echo "# nproc=4 mode=pr"
  row endpoint.0 TestPropertyRoundTrip 4; row endpoint.1 TestPropertyRoundTrip 4; row endpoint.2 TestPropertyRoundTrip 4
  row endpoint.0 TestA 6; row endpoint.1 TestB 4; } >"$D/c/test-times.tsv"
"$D/ci/real-zfs-weights.sh" -s test "$D/a/test-times.tsv" "$D/b/test-times.tsv" "$D/c/test-times.tsv"
grep -v '^#' "$D/ci/real-zfs-weights.txt"
# want: TestPropertyRoundTrip (6 + 12) / 2 = 9, TestA (4 + 6) / 2 = 5, TestB (2 + 4) / 2 = 3, TestOld kept
