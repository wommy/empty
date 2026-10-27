#!/usr/bin/env bash
# full gate for the scan-cut worktree
set -uo pipefail
cd /home/user/empty/.claude/worktrees/agent-aec3b828c6a86e68b || exit 1
rc=0
step() { echo "== $*"; "$@" || { echo "FAIL: $*"; rc=1; }; }
fmt() { out=$(gofmt -l $(git ls-files -co --exclude-standard '*.go')); [ -z "$out" ] || { echo "$out"; return 1; }; }
step fmt
step go vet ./...
step go vet -tags nightly ./...
step go mod tidy -diff
step go test -count=1 -race ./...
step env MODEL_SEQS=1000 go test -count=1 -run TestModel ./internal/endpoint
step go test -count=1 -tags nightly -run 'TestExploreModelMasksStillBite|TestExploreLifecycle' ./internal/endpoint
step go test -count=1 -tags nightly ./internal/zfs/zfsdiff/...
step bash ci/check-suites.sh
echo "GATE rc=$rc"
exit $rc
