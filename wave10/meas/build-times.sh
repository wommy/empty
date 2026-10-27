#!/usr/bin/env bash
# Measure test-binary build times for the real-ZFS job (local, 4 vCPU).
set -euo pipefail
repo=$1
S=$(cd "$(dirname "$0")" && pwd)
cd "$repo"
export GOTOOLCHAIN=go1.27.3 CGO_ENABLED=0 GOFLAGS=-mod=mod
mkdir -p "$S/out"
export GOCACHE=$S/gocache
t() { local lbl=$1; shift; local a; a=$(date +%s.%N); "$@" >/dev/null; printf '%6.2fs  %s\n' "$(echo "$(date +%s.%N) - $a" | bc)" "$lbl"; }
rm -rf "$GOCACHE"
t "cold cache, sequential (as today)" bash -c "for p in endpoint csi; do go test -c -o $S/out/\$p.test ./internal/\$p; done"
rm -rf "$GOCACHE"
t "cold cache, one invocation" go test -c -o "$S/out/" ./internal/endpoint ./internal/csi
t "warm, no change" go test -c -o "$S/out/" ./internal/endpoint ./internal/csi
cp internal/endpoint/plan.go "$S/plan.go.bak"
echo '// x' >>internal/endpoint/plan.go
t "warm, endpoint/plan.go touched" go test -c -o "$S/out/" ./internal/endpoint ./internal/csi
cp "$S/plan.go.bak" internal/endpoint/plan.go
cp internal/endpoint/plan_test.go "$S/plan_test.go.bak"
echo '// x' >>internal/endpoint/plan_test.go
t "warm, endpoint test file touched" go test -c -o "$S/out/" ./internal/endpoint ./internal/csi
cp "$S/plan_test.go.bak" internal/endpoint/plan_test.go
ls -la "$S/out"
du -sh "$GOCACHE"
"$S/out/endpoint.test" -test.list '.*' | grep -c '^Test'
"$S/out/csi.test" -test.list '.*' | grep -c '^Test'
