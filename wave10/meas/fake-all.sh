#!/usr/bin/env bash
# Run every fake-wait.sh case and show only errors and exit codes.
S=$(cd "$(dirname "$0")" && pwd)
for c in good kmod-2.3 missing-test stale-kernel; do
  echo "=== $c"
  bash "$S/fake-wait.sh" "$1" "$c" 2>&1 | grep -E '::error|^exit'
done
