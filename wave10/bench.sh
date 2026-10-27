#!/bin/bash
# usage: bench.sh <repo dir> <label>
set -u
cd "$1"
export GOTOOLCHAIN=go1.27.3
label=$2
t() { local s=$(date +%s.%N); "$@" >/dev/null 2>&1; local rc=$?; local e=$(date +%s.%N); printf '%-40s %6.2fs rc=%d\n' "$label $3" "$(echo "$e - $s" | bc)" $rc; }
go build ./... >/dev/null 2>&1
go test -count=1 -run XXXNONE ./... >/dev/null 2>&1   # warm build cache
go test -race -count=1 -run XXXNONE ./... >/dev/null 2>&1
s=$(date +%s.%N); go test -count=1 -run XXXNONE ./... >/dev/null 2>&1; e=$(date +%s.%N); printf '%-40s %6.2fs\n' "$label build+link only" "$(echo "$e - $s" | bc)"
for i in 1 2 3; do s=$(date +%s.%N); go test -count=1 ./... >/dev/null 2>&1; rc=$?; e=$(date +%s.%N); printf '%-40s %6.2fs rc=%d\n' "$label go test ./... #$i" "$(echo "$e - $s" | bc)" $rc; done
s=$(date +%s.%N); go test -race -count=1 -run XXXNONE ./... >/dev/null 2>&1; e=$(date +%s.%N); printf '%-40s %6.2fs\n' "$label race build+link only" "$(echo "$e - $s" | bc)"
for i in 1 2; do s=$(date +%s.%N); go test -race -count=1 ./... >/dev/null 2>&1; rc=$?; e=$(date +%s.%N); printf '%-40s %6.2fs rc=%d\n' "$label go test -race ./... #$i" "$(echo "$e - $s" | bc)" $rc; done
s=$(date +%s.%N); go vet ./... >/dev/null 2>&1; rc=$?; e=$(date +%s.%N); printf '%-40s %6.2fs rc=%d\n' "$label go vet" "$(echo "$e - $s" | bc)" $rc
