#!/usr/bin/env bash
# stub-weights.sh <repo>: ci/real-zfs-weights.sh on a copy of ci/, with two
# fake runs: run A has TestPropertyRoundTrip split over three shards
# (parallel, 30+30+24 test-s, nproc 4: 21s), run B has it in one shard
# (40 test-s: 10s). Expected weight: (21 + 10) / 2 = 15.50. A sequential
# test measured once per run averages as before: (3 + 5) / 2 = 4.00.
set -euo pipefail
repo=$1
t=$(mktemp -d)
trap 'rm -rf "$t"' EXIT
cp -r "$repo/ci" "$t/ci"
tab=$(printf '\t')
{
  echo "# shard test status parallel own_s cost_s wall_s subtests"
  echo "# nproc=4 mode=pr"
  printf 'endpoint.0\tTestPropertyRoundTrip\tPASS\t1\t1.0\t30.0\t-\t66\n'
  printf 'endpoint.1\tTestPropertyRoundTrip\tPASS\t1\t1.0\t30.0\t-\t65\n'
  printf 'endpoint.2\tTestPropertyRoundTrip\tPASS\t1\t1.0\t24.0\t-\t65\n'
  printf 'endpoint.1\tTestModel\tPASS\t0\t3.0\t3.0\t3.0\t9\n'
} >"$t/a.tsv"
{
  echo "# nproc=4 mode=pr"
  printf 'endpoint.1\tTestPropertyRoundTrip\tPASS\t1\t1.0\t40.0\t-\t196\n'
  printf 'endpoint.1\tTestModel\tPASS\t0\t5.0\t5.0\t5.0\t9\n'
} >"$t/b.tsv"
"$t/ci/real-zfs-weights.sh" -s stub "$t/a.tsv" "$t/b.tsv" >/dev/null
got=$(awk '$1 == "TestPropertyRoundTrip" { print $2 }' "$t/ci/real-zfs-weights.txt" | awk 'NR == 1')
m=$(awk '$1 == "TestModel" { print $2 }' "$t/ci/real-zfs-weights.txt" | awk 'NR == 1')
echo "TestPropertyRoundTrip $got (want 15.50), TestModel $m (want 4.00)"
[ "$got" = 15.50 ] && [ "$m" = 4.00 ] || { echo FAIL; exit 1; }
echo "PASS: stub-weights ($tab separated rows summed per run, averaged across runs)"
