#!/bin/bash
set -eo pipefail
S=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad
cd /home/user/empty
out=$(gofmt -l $(git ls-files -co --exclude-standard '*.go' | awk '!/^\.claude\//')); [ -z "$out" ] || { echo "gofmt: $out"; exit 1; }
go vet ./... && go vet -tags nightly ./...
go mod tidy -diff
for f in ci/*.sh; do bash -n "$f"; done
bash ci/check-suites.sh >/dev/null
$S/bin/actionlint
go test -race -count=1 ./... 2>&1 | awk '!/no test files/'
MODEL_SEQS=1000 go test -count=1 -run 'TestModel$' ./internal/endpoint/
go test -tags nightly -count=1 -run 'TestExploreModelMasksStillBite|TestExploreLifecycle' ./internal/endpoint/
go test -tags nightly -count=1 ./internal/zfs/zfsdiff/...
CSI_SANITY_NFS=1 go test -count=1 -run TestSanity ./internal/csi/
CSI_SANITY_ACCESS_TYPE=block go test -count=1 -run TestSanity ./internal/csi/
go test -count=1 -run TestSanity ./internal/csi/
echo GATE-OK
