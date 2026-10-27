#!/usr/bin/env bash
# synthetic 2x3 shard logs from the committed plan, then ci/check-split.sh on both parts
set -euo pipefail
repo=/home/user/empty/.claude/worktrees/agent-aa3b18c2bedd0e0b7
wn=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wn
o=$wn/synth
rm -rf "${o:?}"
mkdir -p "$o"
bash "$repo/ci/real-zfs-plan.sh" "$wn/endpoint.list" "$repo/ci/real-zfs-weights.txt" 2 3 >"$o/endpoint.plan" 2>/dev/null
for s in 0 1 2 3 4 5; do
  awk -v s="$s" -v T=196 -v n=6 '
    $1 == s && $2 == "TestPropertyRoundTrip" {
      k = 0; for (r = s; r < T; r += n) { print "    --- PASS: TestPropertyRoundTrip/row" r " (0.00s)"; k++ }
      print "    props_roundtrip_test.go:1: PROPRT_SHARD " s "/" n ": " k " of " T " rows"
      print "--- PASS: TestPropertyRoundTrip (0.00s)"; next }
    $1 == s { print "--- PASS: " $2 " (0.00s)" }' "$o/endpoint.plan" >"$o/endpoint.$s.log"
done
cd "$repo"
bash ci/check-split.sh TestPropertyRoundTrip "$wn/endpoint.list" "$o/endpoint.plan" 0/2 "$o"/endpoint.0.log "$o"/endpoint.1.log "$o"/endpoint.2.log
bash ci/check-split.sh TestPropertyRoundTrip "$wn/endpoint.list" "$o/endpoint.plan" 1/2 "$o"/endpoint.3.log "$o"/endpoint.4.log "$o"/endpoint.5.log
