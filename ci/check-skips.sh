#!/usr/bin/env bash
# Enforce the skip budget in ci/expected-skips.txt on one test binary's
# -test.v output: a green run with unexpected skips is not full coverage.
#
# usage: ci/check-skips.sh <leg> <package> <log>
#   ci/check-skips.sh vm endpoint "$work/endpoint.log"
# Fails when the log has a `--- SKIP:` line for a test not allowed for
# <leg> <package>, or when it has no `--- PASS:` line (nothing ran).
set -euo pipefail

leg=${1:?leg, e.g. vm or noble}
pkg=${2:?package under internal/, e.g. endpoint}
log=${3:?test log}
list=$(dirname "$0")/expected-skips.txt

allowed=$(sed 's/#.*//' "$list" | awk -v l="$leg" -v p="$pkg" '$1 == l && $2 == p { print $3 }' | sort -u)
skipped=$(sed -nE 's/^[[:space:]]*--- SKIP: ([^ ]+).*/\1/p' "$log" | sort -u)
unexpected=$(comm -23 <(printf '%s\n' "$skipped" | sed '/^$/d') <(printf '%s\n' "$allowed" | sed '/^$/d'))
stale=$(comm -13 <(printf '%s\n' "$skipped" | sed '/^$/d') <(printf '%s\n' "$allowed" | sed '/^$/d'))

n=$(printf '%s\n' "$skipped" | sed '/^$/d' | wc -l)
m=$(printf '%s\n' "$allowed" | sed '/^$/d' | wc -l)
echo "skip budget $leg/$pkg: $n skipped, $m allowed"
{
  echo "| $leg | $pkg | $n skipped / $m allowed | ${skipped//$'\n'/ } |"
} >>"${GITHUB_STEP_SUMMARY:-/dev/null}"

rc=0
if ! grep -qE '^[[:space:]]*--- PASS: ' "$log"; then
  echo "::error::$leg/$pkg: no test passed (empty or failed run?)"
  rc=1
fi
if [ -n "$unexpected" ]; then
  for t in $unexpected; do
    echo "::error::$leg/$pkg: $t skipped but is not in ci/expected-skips.txt"
    grep -B1 -E "^[[:space:]]*--- SKIP: $t " "$log" || true
  done
  rc=1
fi
for t in $stale; do
  # not a failure: a test that stopped skipping is more coverage
  echo "::notice::$leg/$pkg: $t is allowed to skip but did not; if that holds, drop it from ci/expected-skips.txt"
done
exit $rc
