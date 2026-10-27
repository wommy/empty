#!/usr/bin/env bash
# Check one real-ZFS test binary's -test.v output against what it should
# have measured: a green exit code is not coverage.
#
# usage: ci/check-skips.sh <leg> <package> <log> [<test list> [<min specs>]]
#   ci/check-skips.sh vm endpoint "$work/endpoint.log" "$work/endpoint.list"
#   ci/check-skips.sh vm csi "$work/csi-block.log" "" 44
#
# Fails when
# - a test skipped that ci/expected-skips.txt does not allow for <leg> <package>
#   (an entry for TestX also covers its subtests TestX/..., and is met when
#   only those subtests skip);
# - an allowed skip did not skip: on the real leg every entry is a simulator-
#   only test, so a missing skip means the binary ran on the simulator;
# - the log shows a failure or panic (the exit code already carries this;
#   checking twice costs nothing);
# - given a list (`<bin> -test.list '.*'`), a top-level test in it neither
#   passed nor skipped, i.e. did not run;
# - given a minimum, Ginkgo ran fewer specs ("Ran N of M Specs").
set -euo pipefail

leg=${1:?leg, e.g. vm}
pkg=${2:?package under internal/, e.g. endpoint}
log=${3:?test log}
tests=${4:-}
minspecs=${5:-}
list=$(dirname "$0")/expected-skips.txt

lines() { sed '/^$/d' | sort -u; }
allowed=$(sed 's/#.*//' "$list" | awk -v l="$leg" -v p="$pkg" '$1 == l && $2 == p { print $3 }' | lines)
# no leading anchor: a result line can be glued to unterminated output
skipped=$(sed -nE 's/.*--- SKIP: ([^ ]+) \(.*/\1/p' "$log" | lines)
passed=$(sed -nE 's/.*--- PASS: ([^ ]+) \(.*/\1/p' "$log" | lines)

rc=0
err() { echo "::error::$leg/$pkg: $*"; rc=1; }

unexpected=
for t in $skipped; do
  grep -qxF "$t" <<<"$allowed" || grep -qxF "${t%%/*}" <<<"$allowed" || unexpected+="$t "
done
stale=
for t in $allowed; do
  grep -qxF "$t" <<<"$skipped" || grep -qF "$t/" <<<"$skipped" || stale+="$t "
done

n=$(printf '%s\n' "$skipped" | lines | wc -l)
m=$(printf '%s\n' "$allowed" | lines | wc -l)
echo "skip budget $leg/$pkg: $n skipped, $m allowed"
echo "| $leg | $pkg | $n skipped / $m allowed | ${skipped//$'\n'/ } |" >>"${GITHUB_STEP_SUMMARY:-/dev/null}"

[ -n "$passed" ] || err "no test passed (empty or failed run?)"
for t in $unexpected; do
  err "$t skipped but is not in ci/expected-skips.txt"
  grep -B1 -F -- "--- SKIP: $t " "$log" || true
done
for t in $stale; do
  err "$t is allowed to skip only because it needs the simulator, but it ran: is this binary on the simulator?"
done
if grep -nE -- '--- FAIL: |^panic: |^FAIL' "$log"; then err "failure or panic in the log"; fi
if [ -n "$tests" ]; then
  for t in $(grep -E '^(Test|Fuzz|Example)' "$tests" | lines); do
    grep -qxF "$t" <<<"$passed" || grep -qxF "$t" <<<"$skipped" || err "$t never reported PASS or SKIP: it did not run"
  done
fi
if [ -n "$minspecs" ]; then
  ran=$(sed -nE 's/.*Ran ([0-9]+) of [0-9]+ Specs.*/\1/p' "$log" | tail -n 1)
  [ -n "$ran" ] || err "no Ginkgo summary in the log"
  [ -z "$ran" ] || [ "$ran" -ge "$minspecs" ] || err "Ginkgo ran $ran specs, fewer than the floor of $minspecs"
fi
exit $rc
