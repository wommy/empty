#!/bin/bash
set -eo pipefail
cd /home/user/wt-step1
out=$(gofmt -l $(git ls-files '*.go' | grep -v '^\.claude/')); [ -z "$out" ] || { echo "gofmt: $out"; exit 1; }
go vet ./... && go vet -tags nightly ./...
go mod tidy -diff
go test -race -count=1 ./... 2>&1 | grep -v "no test files"
MODEL_SEQS=1000 go test -count=1 -run 'TestModel$' ./internal/endpoint/
go test -tags nightly -count=1 -run TestExploreModelMasksStillBite ./internal/endpoint/
if [ -n "$LIFECYCLE" ]; then FSM_OUT=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/fsm/r go test -tags nightly -count=1 -run TestExploreLifecycle ./internal/endpoint/ -v 2>&1 | grep -i "finding\|ok\|FAIL" | tail -10; fi
echo GATE-OK
