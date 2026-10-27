#!/usr/bin/env bash
# Revert each verifier fix in turn (on top of the fixed tree) and run the
# export tests; logs go to revert-<fix>.log next to this script.
# usage: revert-check.sh <worktree> [fixes...]
set -u
W=$(cd "$(dirname "$0")" && pwd)
cd "$1"
shift
run='TestFileExport|TestEtabServes|TestPlan.*ublishExport|TestParseEtab|TestExportConverges|TestNFSReplayOnSim|FuzzNFS'
for v in "${@:-D1 D2 D3}"; do
  cp "$W/fixed/export.go" "$W/fixed/endpoint.go" internal/endpoint/
  python3 -I "$W/revert.py" internal/endpoint "$v" || exit 1
  GOTOOLCHAIN=go1.27.3 go test -count=1 -run "$run" ./internal/endpoint >"$W/revert-$v.log" 2>&1
  echo "== $v reverted"
  grep -E '^\s*--- FAIL|^ok|^FAIL' "$W/revert-$v.log"
done
cp "$W/fixed/export.go" "$W/fixed/endpoint.go" internal/endpoint/
echo "== fixed"
GOTOOLCHAIN=go1.27.3 go test -count=1 -run "$run" ./internal/endpoint 2>&1 | tail -3
