#!/usr/bin/env bash
# stub-shards.sh (run from the repo root): tests lever 3 off the VM.
# 1. builds the endpoint test binary (simulator), lists its tests;
# 2. runs ci/real-zfs-guest.sh's own shard() (extracted verbatim) on the
#    list and ci/real-zfs-weights.txt, and checks the plan: every listed
#    test in exactly one shard, TestPropertyRoundTrip in all three;
# 3. runs the three endpoint shards on the simulator exactly as the guest
#    does (-test.run from the plan, PROPRT_SHARD=i/3, -test.parallel=8),
#    then the host checks: ci/check-split.sh and, on the merged log,
#    ci/check-skips.sh's list coverage (every listed test PASS or SKIP);
# 4. negative cases for ci/check-split.sh: a duplicated shard, a missing
#    shard, a row run twice, a missing partition line.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
w=$here/stub
rm -rf "$w"
mkdir -p "$w"
fail() { echo "FAIL: $*"; exit 1; }

CGO_ENABLED=0 go test -c -o "$w/" ./internal/endpoint
"$w/endpoint.test" -test.list '.*' >"$w/endpoint.list"
echo "listed: $(awk '/^(Test|Fuzz|Example)/' "$w/endpoint.list" | wc -l) tests"

# shard() and its variables, verbatim from the guest script
awk '/^nfs=/ { p = 1 } p { print } /^}$/ && p { exit }' ci/real-zfs-guest.sh >"$w/shardfn.sh"
grep -q '^split=' "$w/shardfn.sh" || fail "shard() extract has no split="
(
  ZFSDIFF_FULL=
  # shellcheck source=/dev/null
  . "$w/shardfn.sh"
  shard "$w/endpoint.list" ci/real-zfs-weights.txt 3 >"$w/endpoint.plan"
)
awk '/^(Test|Fuzz|Example)/ { print $1 }' "$w/endpoint.list" | sort >"$w/want"
# every listed test: TestPropertyRoundTrip in shards 0,1,2; others in one
awk '{ c[$2]++; s[$2] = s[$2] $1 } END { for (t in c) print t, c[t], s[t] }' "$w/endpoint.plan" | sort >"$w/got"
awk '{ print $1 }' "$w/got" | diff - "$w/want" >/dev/null || fail "plan does not cover the list exactly"
awk '$1 == "TestPropertyRoundTrip" && !($2 == 3 && $3 == "012") { bad = 1 }
     $1 != "TestPropertyRoundTrip" && $2 != 1 { bad = 1; print "twice: " $0 }
     END { exit bad }' "$w/got" || fail "a test is in the wrong number of shards"
grep -q '^TestPropertyRoundTrip 3 012$' "$w/got" || fail "TestPropertyRoundTrip is not in all three shards"
echo "plan: every listed test in exactly one shard; TestPropertyRoundTrip in 0, 1 and 2"
# predicted loads per shard
awk 'FNR == NR { if ($1 !~ /^#/ && NF == 2) w[$1] = $2; next }
     { x = ($2 in w) ? w[$2] : 0.1; if ($2 == "TestPropertyRoundTrip") x /= 3; l[$1] += x }
     END { for (i = 0; i < 3; i++) printf "  shard %d predicted %.2fs\n", i, l[i] }' ci/real-zfs-weights.txt "$w/endpoint.plan"

# the shards, as the guest runs them (simulator: no ZFS_TEST_POOL)
for i in 0 1 2; do
  re=$(awk -v s="$i" '$1 == s { printf "%s%s", sep, $2; sep = "|" }' "$w/endpoint.plan")
  (cd internal/endpoint && env PROPRT_SHARD="$i/3" "$w/endpoint.test" -test.count=1 -test.v -test.timeout=4m \
    -test.parallel=8 -test.run "^($re)\$" >"$w/endpoint.$i.log" 2>&1) || fail "shard $i failed (see $w/endpoint.$i.log)"
done
cat "$w"/endpoint.[0-9].log >"$w/endpoint.log"
ci/check-split.sh TestPropertyRoundTrip "$w/endpoint.list" "$w"/endpoint.[0-9].log || fail "check-split on good logs"
# check-skips' list coverage; the simulator leg's skips differ from the
# vm leg's, so only its "never reported PASS or SKIP" lines matter here
ci/check-skips.sh vm endpoint "$w/endpoint.log" "$w/endpoint.list" >"$w/skips.out" 2>&1 || true
if grep 'never reported PASS or SKIP' "$w/skips.out"; then fail "a listed test did not run"; fi
echo "check-skips: every listed test reported PASS or SKIP across the three shards"
n=$(sed -nE 's/.*--- (PASS|FAIL|SKIP): (TestPropertyRoundTrip\/[^ ]+) \(.*/\2/p' "$w/endpoint.log" | sort | uniq -d | wc -l)
[ "$n" = 0 ] || fail "$n subtests ran twice"

# negative cases: each must fail
neg() {
  local what=$1
  shift
  if ci/check-split.sh TestPropertyRoundTrip "$w/endpoint.list" "$@" >"$w/neg.out" 2>&1; then
    cat "$w/neg.out"
    fail "check-split passed: $what"
  fi
  echo "check-split refuses: $what ($(awk 'NR == 2' "$w/neg.out"))"
}
neg "a shard log missing" "$w/endpoint.0.log" "$w/endpoint.1.log"
neg "one shard twice" "$w/endpoint.0.log" "$w/endpoint.1.log" "$w/endpoint.1.log"
sed 's|PROPRT_SHARD 2/3|PROPRT_SHARD 1/3|' "$w/endpoint.2.log" >"$w/bad.2.log"
neg "two shards claim 1/3" "$w/endpoint.0.log" "$w/endpoint.1.log" "$w/bad.2.log"
{ cat "$w/endpoint.2.log"; awk '/--- PASS: TestPropertyRoundTrip\//' "$w/endpoint.0.log" | awk 'NR == 1'; } >"$w/dup.2.log"
neg "a row run in two shards" "$w/endpoint.0.log" "$w/endpoint.1.log" "$w/dup.2.log"
awk '!/PROPRT_SHARD/' "$w/endpoint.2.log" >"$w/nol.2.log"
neg "a shard without its partition line" "$w/endpoint.0.log" "$w/endpoint.1.log" "$w/nol.2.log"
awk '!(/--- PASS: TestPropertyRoundTrip\// && ++n == 1)' "$w/endpoint.1.log" >"$w/miss.1.log"
neg "a row that never reported" "$w/endpoint.0.log" "$w/miss.1.log" "$w/endpoint.2.log"
printf 'TestOther\n' >"$w/other.list"
ci/check-split.sh TestPropertyRoundTrip "$w/other.list" "$w/endpoint.0.log" | grep -q 'nothing to check' ||
  fail "a test not in the list should not be checked"
echo "PASS: stub-shards"
