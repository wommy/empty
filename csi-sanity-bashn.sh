#!/usr/bin/env bash
# bash -n on every ci/*.sh, one at a time; run from the worktree root
rc=0
for f in ci/*.sh; do bash -n "$f" || { echo "FAIL $f"; rc=1; }; done
command -v shellcheck >/dev/null && shellcheck -S warning ci/check-specs.sh ci/check-skips.sh
echo "bash -n rc=$rc"
exit $rc
