#!/usr/bin/env bash
# Fail unless the nightly explore job's `go test -v` log shows every
# TestExplore* test of the repository passed: a green exit code with
# "[no tests to run]" (a lost `//go:build nightly`, a run pattern that no
# longer matches) is not coverage, and neither is one test of several.
# nightly.yml runs them with -run '^TestExplore'.
# usage: ci/check-explore-ran.sh <log>
set -euo pipefail
log=$(realpath "${1:?go test -v log}")
cd "$(dirname "$0")/.."
# every TestExplore* function in a test file (they all carry the nightly tag)
want=$(grep -rhoE '^func TestExplore[A-Za-z0-9_]*' --include='*_test.go' . | awk '{ print $2 }' | sort -u)
# top-level results only (subtests have a '/'); a SKIP does not count as ran
got=$(grep -oE -- '^--- PASS: TestExplore[^/ ]* \(' "$log" | awk '{ print $3 }' | sort -u || true)
if [ -z "$got" ]; then
  echo "::error::explore: no TestExplore* test passed (build tag nightly, run pattern, or the tests are missing)"
  exit 1
fi
missing=$(comm -23 <(echo "$want") <(echo "$got"))
if [ -n "$missing" ]; then
  echo "::error::explore: TestExplore* tests that did not pass: $(echo "$missing" | tr '\n' ' ')"
  exit 1
fi
echo "explore: $(echo "$got" | wc -l) test(s) passed"
