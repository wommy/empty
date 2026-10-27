#!/bin/bash
# finding 5's orphan check: drain times out under a long zfs, 8 runs per mode
H=$(dirname "$(readlink -f "$0")")
for mode in main group kill9; do
  orphans=0
  for i in $(seq 8); do
    out=$("$H/run-shutdown.sh" "$mode" 30 1s 2>&1)
    echo "$out" | sed -n '/still alive/{n;p}' | grep -qv '^none$' && orphans=$((orphans + 1))
  done
  echo "$mode: runs with a zfs child alive after the daemon exited: $orphans/8"
done
