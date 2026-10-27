#!/usr/bin/env bash
# Fail when the nightly explore job's `go test -v` log shows no Scale/Explore
# test passed: a green exit code with "[no tests to run]" is not coverage.
# usage: ci/check-explore-ran.sh <log>
set -euo pipefail
log=${1:?go test -v log}
# top-level results only (subtests have a '/'); a SKIP does not count as ran
n=$(grep -cE -- '^--- PASS: Test(Scale|Explore)[^/ ]* \(' "$log" || true)
if [ "$n" -eq 0 ]; then
  echo "::error::explore: no TestScale*/TestExplore* test passed (build tag nightly, run pattern, or the tests are missing)"
  exit 1
fi
echo "explore: $n test(s) passed"
