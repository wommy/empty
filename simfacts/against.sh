#!/usr/bin/env bash
# against.sh WORKTREE CURRENT_SIM PATTERN SIM...: run PATTERN in
# internal/endpoint once with each SIM in place of sim.go, then restore
# CURRENT_SIM. No git.
set -u
wt=${1:?worktree} cur=${2:?current sim} pat=${3:?pattern}
shift 3
cd "${wt:?}" || exit 1
for s in "$@"; do
  echo "== sim: $(basename "$s")"
  cp "$s" internal/zfs/zfstest/sim.go
  go test -count=1 -run "$pat" ./internal/endpoint/ 2>&1 | grep -E "_test.go:|^(ok|FAIL|---)"
done
cp "$cur" internal/zfs/zfstest/sim.go
