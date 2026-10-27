#!/usr/bin/env bash
# Run the branch's export tests against HEAD's verifier (export.go and
# endpoint.go from the base branch; the fuzz and replay files, which use
# the new verifier API, set aside), then restore the branch.
# usage: head-check.sh <worktree> <base-rev>
set -u
W=$(cd "$(dirname "$0")" && pwd)
cd "$1"
base=$2
git show "$base:internal/endpoint/export.go" >internal/endpoint/export.go
git show "$base:internal/endpoint/endpoint.go" >internal/endpoint/endpoint.go
mv internal/endpoint/nfs_diff_test.go internal/endpoint/nfs_replay_test.go "$W/"
GOTOOLCHAIN=go1.27.3 go test -count=1 -run 'TestFileExport|TestEtabServes|TestPlan.*ublishExport' ./internal/endpoint >"$W/revert-head.log" 2>&1
grep -E '^\s*--- FAIL|^ok|^FAIL' "$W/revert-head.log"
mv "$W/nfs_diff_test.go" "$W/nfs_replay_test.go" internal/endpoint/
git checkout HEAD -- internal/endpoint/export.go internal/endpoint/endpoint.go
git status --short
