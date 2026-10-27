#!/usr/bin/env bash
# bite tests for ci/check-specs.sh; run from the worktree root
S=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad
L=internal/csi/testdata/sanity-passed-nfs.txt
echo "== match"; ci/check-specs.sh vm csi-nfs "$S/csi-nfs.passed" "$L"; echo "rc=$?"
grep -v 'GetCapacity should return capacity (no optional' "$L" >"$S/drop.txt"
echo "== drop one"; ci/check-specs.sh vm csi-nfs "$S/csi-nfs.passed" "$S/drop.txt"; echo "rc=$?"
cp "$L" "$S/add.txt"; echo 'Controller Service [Controller Server] bogus spec' >>"$S/add.txt"
echo "== add bogus"; ci/check-specs.sh vm csi-nfs "$S/csi-nfs.passed" "$S/add.txt"; echo "rc=$?"
echo "== no passed file"; ci/check-specs.sh vm csi-nfs "$S/missing.passed" "$L"; echo "rc=$?"
: >"$S/empty.passed"
echo "== empty passed file"; ci/check-specs.sh vm csi-nfs "$S/empty.passed" "$L" >"$S/empty.out"; echo "rc=$?"; sed -n 1,3p "$S/empty.out"
