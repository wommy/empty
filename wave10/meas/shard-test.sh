#!/usr/bin/env bash
# Exercise guest-run.sh's shard() on the real endpoint test list, and run the
# endpoint binary on the simulator per shard to prove the union covers the list.
set -euo pipefail
S=$(cd "$(dirname "$0")" && pwd)
W=$1/ci/vm
"$S/out/endpoint.test" -test.list '.*' >"$S/endpoint.list"
sed -n '/^shard() {/,/^}/p' "$W/guest-run.sh" >"$S/shard.sh"
# shellcheck disable=SC1091
source "$S/shard.sh"
shard "$S/endpoint.list" "$W/test-weights.txt" 3 >"$S/plan"
echo "tests in list: $(grep -c '^Test' "$S/endpoint.list"), planned: $(wc -l <"$S/plan"), unique: $(awk '{print $2}' "$S/plan" | sort -u | wc -l)"
awk 'FNR==NR { if ($1 !~ /^#/) w[$1] = $2; next } { l[$1] += ($2 in w) ? w[$2] : 0.1; c[$1]++ }
     END { for (k in l) printf "shard %s: %d tests, %.2fs weight\n", k, c[k], l[k] }' "$W/test-weights.txt" "$S/plan" | sort
echo "file-export shards: $(grep FileExport "$S/plan" | awk '{print $1}' | sort -u | tr '\n' ' ')"
rm -f "$S/sim-all.log"
cd "$S/out"
for i in 0 1 2; do
  re=$(awk -v s="$i" '$1 == s { printf "%s%s", sep, $2; sep = "|" }' "$S/plan")
  a=$(date +%s.%N)
  ./endpoint.test -test.count=1 -test.v -test.run "^($re)\$" >"$S/sim-$i.log" 2>&1 || echo "shard $i exit $?"
  printf 'sim shard %s: %.2fs, regex %d bytes\n' "$i" "$(echo "$(date +%s.%N) - $a" | bc)" "${#re}"
  cat "$S/sim-$i.log" >>"$S/sim-all.log"
done
missing=0
for t in $(grep '^Test' "$S/endpoint.list"); do
  grep -qE -- "--- (PASS|SKIP): $t \(" "$S/sim-all.log" || { echo "MISSING $t"; missing=$((missing + 1)); }
done
echo "missing from the union of shard logs: $missing"
