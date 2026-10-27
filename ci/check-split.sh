#!/usr/bin/env bash
# Check that a test split across shards (ci/real-zfs-guest.sh: split=) ran
# every one of its subtests in exactly one shard. ci/check-skips.sh already
# checks that the test itself passed; this checks that the parts add up.
#
# usage: ci/check-split.sh <test> <test list> <shard log>...
#   ci/check-split.sh TestPropertyRoundTrip "$o/endpoint.list" "$o"/endpoint.[0-9].log
#
# Each shard's log carries the test's partition line
#   <file>:<line>: PROPRT_SHARD <i>/<n>: <k> of <total> rows
# and fails here unless
# - there is exactly one such line per shard i = 0..n-1, with one n and one
#   total across them, and the k sum to the total;
# - the subtests reporting a result (PASS, FAIL or SKIP) number the total,
#   and none reports in two shards (or twice in one).
# A test missing from the list is not checked (it was renamed or removed;
# check-skips covers the list).
set -euo pipefail

test=${1:?test, e.g. TestPropertyRoundTrip}
list=${2:?test list}
shift 2
[ $# -gt 0 ] || { echo "::error::check-split: no shard logs" >&2; exit 1; }

listed=$(awk -v t="$test" '$1 == t { n++ } END { print n + 0 }' "$list")
if [ "$listed" = 0 ]; then
  echo "check-split: $test is not in $list; nothing to check"
  exit 0
fi

# awk reads every line of every log: no early-closing reader in a pipe
awk -v t="$test" '
  # the partition line, wherever t.Logf put it on the line
  match($0, /PROPRT_SHARD [0-9]+\/[0-9]+: [0-9]+ of [0-9]+ rows/) {
    split(substr($0, RSTART, RLENGTH), f, /[ \/:]+/)
    i = f[2]; n = f[3]; k = f[4]; tot = f[6]
    parts++
    if (i in seen) { bad = bad sprintf("shard %d reported twice (%s)\n", i, FILENAME) }
    seen[i] = 1
    if (n0 == "") { n0 = n; tot0 = tot } else if (n != n0 || tot != tot0) {
      bad = bad sprintf("%s says %d/%d of %d rows, another shard %s of %s\n", FILENAME, i, n, tot, n0, tot0)
    }
    sum += k
    next
  }
  # a subtest result, anchored nowhere: it can be glued to output
  match($0, /--- (PASS|FAIL|SKIP): [^ ]+ \(/) {
    name = substr($0, RSTART, RLENGTH)
    sub(/^--- [A-Z]+: /, "", name)
    sub(/ \($/, "", name)
    if (index(name, t "/") != 1) next
    if (name in where) dup = dup sprintf("%s in %s and %s\n", name, where[name], FILENAME)
    else { where[name] = FILENAME; subs++ }
  }
  END {
    rc = 0
    if (parts == 0) { printf "::error::check-split: no PROPRT_SHARD line for %s in any shard log\n", t; exit 1 }
    for (i = 0; i < n0; i++) if (!(i in seen)) { bad = bad sprintf("shard %d/%d never ran %s\n", i, n0, t) }
    if (parts != n0) bad = bad sprintf("%d partition lines for %d shards\n", parts, n0)
    if (sum != tot0) bad = bad sprintf("the shards ran %d rows, the table has %d\n", sum, tot0)
    if (subs != tot0) bad = bad sprintf("%d subtests reported a result, the table has %d\n", subs, tot0)
    if (bad != "" || dup != "") {
      printf "::error::check-split: %s was not split into exactly one run per subtest\n%s", t, bad
      if (dup != "") printf "ran twice:\n%s", dup
      exit 1
    }
    printf "check-split: %s: %d subtests over %d shards, each exactly once\n", t, subs, n0
  }' "$@"
