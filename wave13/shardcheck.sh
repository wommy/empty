#!/usr/bin/env bash
# Plan the endpoint shards the way ci/real-zfs-guest.sh does, from a locally
# built endpoint.test, and check: every listed test in exactly one shard,
# the NFS group (TestFileExport*, TestNFSReplay) in one shard. Then feed
# check-skips a synthetic VM log (PASS for all, SKIP for the vm endpoint
# entries of ci/expected-skips.txt) to show the list and the budget agree.
# usage: shardcheck.sh <worktree>
set -euo pipefail
W=$(cd "$(dirname "$0")" && pwd)
cd "$1"
tmp=$W/shardcheck
rm -rf "$tmp"; mkdir -p "$tmp"
CGO_ENABLED=0 GOTOOLCHAIN=go1.27.3 go test -c -o "$tmp/endpoint.test" ./internal/endpoint
"$tmp/endpoint.test" -test.list '.*' >"$tmp/endpoint.list"
# the shard function, verbatim from the guest script
sed -n '/^shard() {/,/^}/p' ci/real-zfs-guest.sh >"$tmp/shard.sh"
# shellcheck disable=SC1091
. "$tmp/shard.sh"
shard "$tmp/endpoint.list" ci/real-zfs-weights.txt 3 >"$tmp/endpoint.plan"
n_list=$(grep -cE '^(Test|Fuzz|Example)' "$tmp/endpoint.list")
n_plan=$(wc -l <"$tmp/endpoint.plan")
dups=$(awk '{ print $2 }' "$tmp/endpoint.plan" | sort | uniq -d)
echo "listed $n_list, planned $n_plan, duplicates: ${dups:-none}"
[ "$n_list" = "$n_plan" ] && [ -z "$dups" ]
echo "NFS group shards: $(awk '$2 ~ /^(TestFileExport|TestNFSReplay$)/ { print $1 }' "$tmp/endpoint.plan" | sort -u | tr '\n' ' ')"
echo "NFS group members: $(awk '$2 ~ /^(TestFileExport|TestNFSReplay$)/ { printf "%s ", $2 }' "$tmp/endpoint.plan")"
for s in 0 1 2; do
  w=$(awk -v s="$s" 'FNR == NR { if ($1 !~ /^#/ && NF == 2) w[$1] = $2; next }
        $1 == s { t += ($2 in w) ? w[$2] : 0.1 } END { printf "%.2f", t }' ci/real-zfs-weights.txt "$tmp/endpoint.plan")
  echo "shard $s: $(awk -v s="$s" '$1 == s' "$tmp/endpoint.plan" | wc -l) tests, ${w}s"
done
# synthetic VM log
skips=$(sed 's/#.*//' ci/expected-skips.txt | awk '$1 == "vm" && $2 == "endpoint" { print $3 }')
grep -E '^(Test|Fuzz|Example)' "$tmp/endpoint.list" | while read -r t; do
  if grep -qxF "$t" <<<"$skips"; then echo "--- SKIP: $t (0.00s)"; else echo "--- PASS: $t (0.00s)"; fi
done >"$tmp/endpoint.log"
GITHUB_STEP_SUMMARY=/dev/null bash ci/check-skips.sh vm endpoint "$tmp/endpoint.log" "$tmp/endpoint.list"
