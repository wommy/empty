#!/usr/bin/env bash
# Fuzz each FuzzNFS* target for a fixed time; log next to this script.
# usage: fuzz.sh <worktree> [fuzztime]
set -u
W=$(cd "$(dirname "$0")" && pwd)
cd "$1"
t=${2:-20s}
rc=0
for f in FuzzNFSRoundTrip FuzzNFSClassifier FuzzNFSVerifier FuzzNFSEtabParse; do
  echo "== $f ($t)"
  GOTOOLCHAIN=go1.27.3 go test -run '^$' -fuzz "^$f\$" -fuzztime "$t" ./internal/endpoint 2>&1 | tail -n 4 || rc=1
done 2>&1 | tee "$W/fuzz.log"
git status --short -- internal/endpoint/testdata
exit $rc
