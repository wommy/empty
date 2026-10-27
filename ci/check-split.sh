#!/usr/bin/env bash
# Check that the endpoint suite's shards, over every job that runs a part of
# it, run each test exactly once, from inside one of those jobs: no job
# waits for the others. Two halves:
#
# 1. The plan (static: no run can change it). <plan> must be exactly what
#    ci/real-zfs-plan.sh makes from <test list>, ci/real-zfs-weights.txt and
#    <part>'s P with 3 shards per part (a pure function: every part's job
#    makes the same plan from the same commit, and prints its sha256 to
#    compare), and a partition of the list over all P*3 shards: every
#    top-level test (Test, Fuzz, Example) in exactly one shard, the split
#    test <test> in every shard, nothing that is not listed.
# 2. This part's run against the plan. The shard logs are this part's
#    shards (<dir>/endpoint.<shard>.log, shards 3p..3p+2), each given once;
#    every top-level test that reports a result (PASS, FAIL or SKIP) does so
#    once, in the log of the shard the plan gives it; every test the plan
#    gives these shards reports; the split test reports in every log, and
#    each log carries its partition line
#      <file>:<line>: PROPRT_SHARD <i>/<n>: <k> of <total> rows
#    with i its shard, n = P*3, one total, and k the number of rows r <
#    total with r mod n = i (the test runs exactly those,
#    internal/endpoint/props_roundtrip_test.go); the subtests reporting a
#    result number the k summed, none twice.
# With 1, the parts' shard sets are disjoint and cover the list, and the
# rows i mod n over i = 0..n-1 are each row once; with 2 in every part's
# job, every listed test and row ran exactly once across the jobs.
# ci/check-skips.sh checks that the tests passed or skipped.
#
# usage: ci/check-split.sh <test> <test list> <plan> <part p/P> <shard log>...
#   ci/check-split.sh TestPropertyRoundTrip "$work/endpoint.list" "$work/endpoint.plan" 1/2 "$o"/endpoint.{3,4,5}.log
# A split test missing from the list is not checked for its rows (it was
# renamed or removed; check-skips covers the list); the rest still is.
set -euo pipefail

test=${1:?test, e.g. TestPropertyRoundTrip}
list=${2:?test list}
plan=${3:?plan}
part=${4:?part p/P}
shift 4
per=3
[ $# -gt 0 ] || { echo "::error::check-split: no shard logs" >&2; exit 1; }
[[ $part =~ ^([0-9]+)/([1-9][0-9]*)$ ]] && [ "${BASH_REMATCH[1]}" -lt "${BASH_REMATCH[2]}" ] ||
  { echo "::error::check-split: part $part, want <p>/<P> with 0 <= p < P" >&2; exit 1; }
p=${BASH_REMATCH[1]} parts=${BASH_REMATCH[2]}
n=$((parts * per))

# 1. the plan is the plan, and a partition of the list
want=$(mktemp)
trap 'rm -f "$want"' EXIT
"$(dirname "$0")/real-zfs-plan.sh" "$list" "$(dirname "$0")/real-zfs-weights.txt" "$parts" "$per" >"$want"
cmp -s "$want" "$plan" || {
  echo "::error::check-split: $plan is not what ci/real-zfs-plan.sh makes from $list for $parts parts of $per shards (< made, > given):"
  diff "$want" "$plan" | awk 'NR <= 40' || true
  exit 1
}
awk -v t="$test" -v n="$n" '
  FNR == NR { if ($1 ~ /^(Test|Fuzz|Example)/ && !($1 in listed)) { listed[$1] = 1; nl++ }; next }
  {
    if (NF != 2 || $1 !~ /^[0-9]+$/ || $1 + 0 >= n) { bad = bad sprintf("line %d: %s\n", FNR, $0); next }
    if (!($2 in listed)) bad = bad sprintf("%s is planned but not listed\n", $2)
    if (($1, $2) in at) bad = bad sprintf("%s twice in shard %s\n", $2, $1)
    at[$1, $2] = 1; cnt[$2]++
  }
  END {
    for (x in listed) {
      w = (x == t) ? n : 1
      if (cnt[x] + 0 != w) bad = bad sprintf("%s planned in %d shards, want %d\n", x, cnt[x], w)
    }
    if (bad != "") { printf "::error::check-split: the plan is not a partition of the list over %d shards\n%s", n, bad; exit 1 }
    printf "check-split: the plan puts each of %d listed tests in exactly one of %d shards (%s in all)\n", nl, n, t
  }' "$list" "$plan"
echo "check-split: list sha256 $(sha256sum <"$list" | cut -c1-16), plan sha256 $(sha256sum <"$plan" | cut -c1-16) (every part's job prints the same)"

# 2. this part's logs against the plan
listed=$(awk -v t="$test" '$1 == t { n++ } END { print n + 0 }' "$list")
[ "$listed" != 0 ] || echo "check-split: $test is not in $list; not checking its rows"
# awk reads every line of every input: no early-closing reader in a pipe
awk -v t="$test" -v listed="$listed" -v n="$n" -v lo=$((p * per)) -v hi=$((p * per + per)) -v part="$part" '
  FNR == 1 && NR != FNR {
    # a shard log: its shard from its name, endpoint.<i>.log
    s = FILENAME; sub(/\.log$/, "", s); sub(/^.*\./, "", s)
    if (FILENAME !~ /\.[0-9]+\.log$/ || s + 0 < lo || s + 0 >= hi) { printf "::error::check-split: %s is not the log of one of part %s'"'"'s shards %d..%d\n", FILENAME, part, lo, hi - 1; exit 1 }
    s += 0
    if (s in given) { printf "::error::check-split: shard %d given twice (%s)\n", s, FILENAME; exit 1 }
    given[s] = FILENAME; shards++
  }
  FNR == NR { if ($1 >= lo && $1 < hi && $2 != t) plan[$2] = $1; else if ($1 >= lo && $1 < hi) plan[$2] = "all"; next }
  # the partition line, wherever t.Logf put it on the line
  match($0, /PROPRT_SHARD [0-9]+\/[0-9]+: [0-9]+ of [0-9]+ rows/) {
    split(substr($0, RSTART, RLENGTH), f, /[ \/:]+/)
    i = f[2]; k = f[4]; tot = f[6]
    parts++
    if (i in seen) { bad = bad sprintf("shard %d reported twice (%s)\n", i, FILENAME) }
    seen[i] = 1
    if (i != s) bad = bad sprintf("%s (shard %d) says shard %d\n", FILENAME, s, i)
    if (f[3] != n) bad = bad sprintf("%s says %d/%d, the plan has %d shards\n", FILENAME, i, f[3], n)
    if (tot0 == "") tot0 = tot
    else if (tot != tot0) bad = bad sprintf("%s says %d rows, another shard %d\n", FILENAME, tot, tot0)
    want = (i < tot) ? int((tot - 1 - i) / n) + 1 : 0
    if (k != want) bad = bad sprintf("%s ran %d rows, rows r < %d with r mod %d = %d number %d\n", FILENAME, k, tot, n, i, want)
    sum += k
    next
  }
  # a subtest result, anchored nowhere: it can be glued to output
  match($0, /--- (PASS|FAIL|SKIP): [^ ]+ \(/) {
    name = substr($0, RSTART, RLENGTH)
    sub(/^--- [A-Z]+: /, "", name)
    sub(/ \($/, "", name)
    if (index(name, "/") == 0) {
      # a top-level test: once per shard log, in the shard the plan gives
      # it (the split test: in every log)
      if ((name, s) in top) again = again sprintf("%s twice in %s\n", name, FILENAME)
      else {
        top[name, s] = 1; nlogs[name]++
        if (!(name in plan)) again = again sprintf("%s ran in %s, but the plan gives it to no shard of part %s\n", name, FILENAME, part)
        else if (name != t && plan[name] != s) again = again sprintf("%s ran in %s, the plan gives it to shard %s\n", name, FILENAME, plan[name])
      }
      next
    }
    if (index(name, t "/") != 1) next
    if (name in where) dup = dup sprintf("%s in %s and %s\n", name, where[name], FILENAME)
    else { where[name] = FILENAME; subs++ }
  }
  END {
    if (shards != hi - lo) again = again sprintf("%d shard logs for part %s, which has shards %d..%d\n", shards, part, lo, hi - 1)
    tops = 0
    for (x in plan) {
      if (x == t) { if (listed && nlogs[x] != shards) again = again sprintf("%s reported in %d of %d shard logs\n", x, nlogs[x], shards) }
      else if (!(x in nlogs)) again = again sprintf("%s is planned in shard %s but reported no result\n", x, plan[x])
      else tops++
    }
    if (again != "") { printf "::error::check-split: a test ran outside its planned shard, twice, or not at all\n%s", again; exit 1 }
    if (!listed) { printf "check-split: part %s: %d top-level tests over %d shard logs, each in exactly its planned shard\n", part, tops, shards; exit 0 }
    if (parts == 0) { printf "::error::check-split: no PROPRT_SHARD line for %s in any shard log\n", t; exit 1 }
    for (i = lo; i < hi; i++) if (!(i in seen)) bad = bad sprintf("shard %d/%d never ran %s\n", i, n, t)
    if (subs != sum) bad = bad sprintf("%d subtests reported a result, the shards ran %d rows\n", subs, sum)
    if (bad != "" || dup != "") {
      printf "::error::check-split: %s was not split into exactly one run per subtest\n%s", t, bad
      if (dup != "") printf "ran twice:\n%s", dup
      exit 1
    }
    printf "check-split: part %s: %d top-level tests over %d shard logs, each in exactly its planned shard (%s in all); %s: %d of %d subtests over shards %d..%d of %d, each exactly once, the other parts'"'"' shards run the rest\n", part, tops, shards, t, t, subs, tot0, lo, hi - 1, n
  }' "$plan" "$@"
