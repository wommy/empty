#!/bin/bash
# run.sh <id...>: per revert, the existing suite and the model test
cd "$(dirname "$0")/../model-copy"
export GOTOOLCHAIN=go1.27.3
for r in "$@"; do
  python3 ../reverts/reverts.py . $r
  echo "--- existing tests (everything but TestModel/FuzzModel):"
  go test ./internal/endpoint/ ./internal/csi/ ./internal/zfs/... -skip 'TestModel|FuzzModel' -count=1 2>&1 | grep -E "^(--- FAIL|ok|FAIL)" | sort | uniq | head -20
  echo "--- model:"
  MODEL_SEQS=${SEQS:-20000} timeout 600 go test ./internal/endpoint/ -run TestModel -count=1 2>&1 | grep -A14 "model check failed\|^ok"
  git checkout -q -- internal
done
