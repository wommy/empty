#!/usr/bin/env bash
# Check one real-ZFS test binary's -test.v output against what it should
# have measured: a green exit code is not coverage.
#
# usage: ci/check-skips.sh <leg> <package> <log> [<test list>]
#   ci/check-skips.sh vm endpoint "$work/endpoint.log" "$work/endpoint.list"
#   ci/check-skips.sh vm csi "$work/csi-block.log"
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
#   passed nor skipped, i.e. did not run.
# csi-sanity's specs are Ginkgo's, not go test's: ci/check-specs.sh compares
# the ones that passed with the pinned list.
#
# SKIPS_ELSEWHERE=<file>, one top-level test per line: the tests another
# job runs (the endpoint suite split over parts, ci/real-zfs-plan.sh). An
# allowed skip among them is that job's to meet, not this log's; one that
# shows up here at all is an unexpected skip. The jobs' budgets add up to
# the whole entry list, and ci/check-split.sh proves the parts disjoint.
set -euo pipefail

leg=${1:?leg, e.g. vm}
pkg=${2:?package under internal/, e.g. endpoint}
log=${3:?test log}
tests=${4:-}
list=$(dirname "$0")/expected-skips.txt

lines() { sed '/^$/d' | sort -u; }
allowed=$(sed 's/#.*//' "$list" | awk -v l="$leg" -v p="$pkg" '$1 == l && $2 == p { print $3 }' | lines)
elsewhere=0
if [ -n "${SKIPS_ELSEWHERE:-}" ]; then
  [ -f "$SKIPS_ELSEWHERE" ] || { echo "::error::$leg/$pkg: SKIPS_ELSEWHERE=$SKIPS_ELSEWHERE does not exist"; exit 1; }
  # awk reads both inputs whole: no early-closing reader in a pipe
  all=$allowed
  allowed=$(awk 'FILENAME == ARGV[1] { other[$1] = 1; next } { t = $0; sub(/\/.*/, "", t); if (!(t in other)) print }' \
    "$SKIPS_ELSEWHERE" - <<<"$all" | lines)
  elsewhere=$(($(printf '%s\n' "$all" | lines | wc -l) - $(printf '%s\n' "$allowed" | lines | wc -l)))
fi
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
echo "skip budget $leg/$pkg: $n skipped, $m allowed$([ "$elsewhere" = 0 ] || echo " (and $elsewhere allowed in the tests another job runs)")"
echo "| $leg | $pkg | $n skipped / $m allowed | ${skipped//$'\n'/ } |" >>"${GITHUB_STEP_SUMMARY:-/dev/null}"

[ -n "$passed" ] || err "no test passed (empty or failed run?)"
for t in $unexpected; do
  err "$t skipped but is not in ci/expected-skips.txt"
  grep -B1 -F -- "--- SKIP: $t " "$log" || true
done
for t in $stale; do
  err "$t is allowed to skip only because it needs the simulator, but it ran: is this binary on the simulator?"
done
if grep -nE -- '--- FAIL: |^panic: |^FAIL' "$log"; then
  err "failure or panic in the log"
  # the failing tests' own output in the job log itself, since the uploaded
  # logs may be unreachable: -v streams a test's log lines under its
  # "=== RUN/NAME/CONT" header as they happen, not under its --- FAIL line
  echo "::group::$leg/$pkg: failing test output"
  awk 'NR == FNR { if (match($0, /--- FAIL: [^ ]+/)) bad[substr($0, RSTART + 10, RLENGTH - 10)] = 1; next }
       /^panic: / { p = 1; n = 0 }
       /^=== (RUN|NAME|CONT|PAUSE) / { t = $3; p = 0; for (b in bad) if (t == b) p = 1; n = 0; if (p) print; next }
       /--- (PASS|SKIP|FAIL): / { p = 0 }
       p && n++ < 200' "$log" "$log"
  echo "::endgroup::"
fi
if [ -n "$tests" ]; then
  for t in $(grep -E '^(Test|Fuzz|Example)' "$tests" | lines); do
    grep -qxF "$t" <<<"$passed" || grep -qxF "$t" <<<"$skipped" || err "$t never reported PASS or SKIP: it did not run"
  done
fi
exit $rc
