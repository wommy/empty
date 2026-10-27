#!/bin/bash
# usage: bench2.sh <repo dir> <label> <reps>
# Wall and CPU (user+sys, children included) seconds per phase, warm build
# cache. CPU is the load-independent number on a shared host.
set -u
cd "$1"
export GOTOOLCHAIN=go1.27.3
label=$2; reps=${3:-3}
RACE_PKGS="./internal/endpoint ./internal/server ./internal/zfs ./internal/lock ./cmd/zfs-endpoint"
PLAIN_PKGS="./internal/csi ./internal/unixsock"
measure() { # name, command...
  local name=$1; shift
  local t0 c0 t1 c1
  t0=$(date +%s.%N); c0=$(awk '{print $14+$15+$16+$17}' /proc/$$/stat)
  "$@" >/dev/null 2>&1; local rc=$?
  t1=$(date +%s.%N); c1=$(awk '{print $14+$15+$16+$17}' /proc/$$/stat)
  local hz; hz=$(getconf CLK_TCK)
  printf '%-14s %-34s wall %6.2fs  cpu %7.2fs  rc=%d\n' "$label" "$name" "$(echo "$t1 - $t0" | bc)" "$(echo "($c1 - $c0) / $hz" | bc -l)" $rc
}
# warm everything once
go test -count=1 ./... >/dev/null 2>&1
go test -race -count=1 ./... >/dev/null 2>&1
go vet ./... >/dev/null 2>&1
for i in $(seq "$reps"); do
  measure "go test ./... #$i" go test -count=1 ./...
  measure "go test -race ./... #$i" go test -race -count=1 ./...
  measure "race subset + plain rest #$i" bash -c "go test -race -count=1 $RACE_PKGS && go test -count=1 $PLAIN_PKGS"
  measure "build+link only (no run) #$i" go test -count=1 -run XXXNONE ./...
done
measure "go vet ./..." go vet ./...
