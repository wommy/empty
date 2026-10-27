#!/usr/bin/env bash
# Emulate a CI run: dependency (std + third-party) build cache warm, this module's own packages changed.
set -u
S=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave10
W=/home/user/empty/.claude/worktrees/agent-a3777e1ab243b4e78
export GOTOOLCHAIN=go1.27.3
C=$S/wt-copy
rm -rf "$C"; mkdir -p "$C"
tar -C "$W" --exclude=.git -cf - . | tar -C "$C" -xf -
# touch one non-test source file per package dir so every own package misses the cache
for d in $(find "$C" -name '*.go' -not -name '*_test.go' -printf '%h\n' | sort -u); do
  f=$(ls "$d"/*.go | grep -v _test.go | head -1)
  printf '\n// cisim-touch\n' >> "$f"
done
t() { local s=$(date +%s.%N); "$@" > "$S/cisim-$LBL.out" 2>&1; local rc=$?; local e=$(date +%s.%N); printf '%s rc=%s %.2fs\n' "$LBL" $rc "$(echo "$e - $s" | bc)" | tee -a "$S/cisim.txt"; }
cd "$C"
: > "$S/cisim.txt"

# (a) unit job steps, with race cache from the cold race run (deps warm)
rm -rf "$S/gocache-ci"; cp -r "$S/gocache-cold-race-cold" "$S/gocache-ci"
export GOCACHE=$S/gocache-ci
LBL=gofmt;       t bash -c 'test -z "$(gofmt -l .)"'
LBL=tidy;        t go mod tidy -diff
LBL=vet;         t go vet ./...
LBL=test-race-ownchanged; t go test -race -count=1 ./...
LBL=test-race-rerun-warm; t go test -race -count=1 ./...
# (b) same without -race (what local dev sees after an edit)
rm -rf "$S/gocache-ci2"; cp -r "$S/gocache-cold-plain-cold" "$S/gocache-ci2"
export GOCACHE=$S/gocache-ci2
for d in $(find "$C" -name '*.go' -not -name '*_test.go' -printf '%h\n' | sort -u); do
  f=$(ls "$d"/*.go | grep -v _test.go | head -1); printf '\n// cisim-touch2\n' >> "$f"; done
LBL=test-plain-ownchanged; t go test -count=1 ./...
# (c) VM binaries, CGO_ENABLED=0, cold then own-changed
export GOCACHE=$S/gocache-vm; rm -rf "$GOCACHE"
mkdir -p "$S/vmbin"
LBL=vm-compile-endpoint-cold; t env CGO_ENABLED=0 go test -c -o "$S/vmbin/endpoint.test" ./internal/endpoint
LBL=vm-compile-csi-after-endpoint; t env CGO_ENABLED=0 go test -c -o "$S/vmbin/csi.test" ./internal/csi
for d in $(find "$C" -name '*.go' -not -name '*_test.go' -printf '%h\n' | sort -u); do
  f=$(ls "$d"/*.go | grep -v _test.go | head -1); printf '\n// cisim-touch3\n' >> "$f"; done
LBL=vm-compile-endpoint-deps-warm; t env CGO_ENABLED=0 go test -c -o "$S/vmbin/endpoint.test" ./internal/endpoint
LBL=vm-compile-csi-deps-warm; t env CGO_ENABLED=0 go test -c -o "$S/vmbin/csi.test" ./internal/csi
ls -l "$S/vmbin" | tee -a "$S/cisim.txt"
echo done >> "$S/cisim.txt"
