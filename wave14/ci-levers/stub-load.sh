#!/usr/bin/env bash
# stub-load.sh <repo>: runs ci/real-zfs-guest.sh's load snapshot lines
# (verbatim: from "snap() {" up to "pids=() names=()", then from
# "snap >"$out/load.after"" to "cat "$out/load.txt"") around a 3s phase
# that keeps two CPUs busy, on this machine's /proc; with mawk if present,
# since the guest has only mawk
set -euo pipefail
repo=$1
t=$(mktemp -d)
trap 'rm -rf "$t"' EXIT
g=$repo/ci/real-zfs-guest.sh
{
  echo 'set -uo pipefail'
  echo "out=$t/out; mkdir -p \"\$out\""
  echo 'up() { cut -d" " -f1 /proc/uptime; }'
  awk '/^snap\(\) \{/ { p = 1 } /^pids=\(\) names=\(\)/ { p = 0 } p' "$g"
  echo 'end=$((SECONDS + 3)); for _ in 1 2; do ( while [ $SECONDS -lt $end ]; do :; done ) & done; wait $(jobs -p | awk "\$0 != \"$vmstat_pid\"" ) 2>/dev/null; sleep 0'
  echo 'for j in $(jobs -p); do [ "$j" = "$vmstat_pid" ] || wait "$j"; done'
  awk '/^snap >"\$out\/load.after"/ { p = 1 } p { print } /^cat "\$out\/load.txt"/ { exit }' "$g"
  echo 'echo "--- vmstat ($(wc -l <"$out/vmstat.txt") lines)"; awk "NR <= 4" "$out/vmstat.txt"'
} >"$t/run.sh"
if command -v mawk >/dev/null; then
  mkdir "$t/bin"
  ln -s "$(command -v mawk)" "$t/bin/awk"
  PATH=$t/bin:$PATH bash "$t/run.sh" | tee "$t/o"
else
  echo "(no mawk here: running with $(awk --version 2>&1 | awk 'NR == 1'))"
  bash "$t/run.sh" | tee "$t/o"
fi
grep -q '^guest load over the test phase: [0-9.]*s on [0-9]* vCPUs$' "$t/o" || { echo "FAIL: no load line"; exit 1; }
grep -q '^cpu: busy [0-9]*% (user [0-9]*%, sys+irq [0-9]*%, steal [0-9]*%), idle [0-9]*%, iowait [0-9]*%$' "$t/o" || { echo "FAIL: cpu line"; exit 1; }
grep -q '^pressure (stalled share of the phase): ' "$t/o" || { echo "FAIL: pressure line"; exit 1; }
echo "PASS: stub-load"
