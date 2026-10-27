#!/usr/bin/env bash
# exercise ci/real-zfs-plan.sh, ci/check-split.sh, ci/check-skips.sh on
# real logs (run 40513522394, 3 shards) and synthetic 2-part logs
set -uo pipefail
R=/home/user/empty/.claude/worktrees/agent-a72cce936e66b59ef
D=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/split3
W=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/latent/work
rm -rf "${W:?}" && mkdir -p "$W"
cd "$R"
pass=0 fail=0
expect() { # expect <ok|fail> <label> <cmd...>
  local want=$1 label=$2; shift 2
  local out rc
  out=$("$@" 2>&1); rc=$?
  if { [ "$want" = ok ] && [ $rc -eq 0 ]; } || { [ "$want" = fail ] && [ $rc -ne 0 ]; }; then
    pass=$((pass + 1)); echo "PASS [$want] $label"
  else
    fail=$((fail + 1)); echo "FAIL [$want, rc=$rc] $label"
  fi
  printf '%s\n' "$out" | awk '/::error::|check-split:|skip budget|^[a-zA-Z]+.* (ran|planned|twice|reported)/ && n++ < 4 { print "    " $0 }'
}

# --- 1. the real run: 3 shards, part 0/1 ------------------------------------
real=$W/real; mkdir -p "$real"
cp "$D"/sample/endpoint.[0-2].log "$real/"
# the run's list: its top-level results (the binary's -test.list is a
# newer commit's); order does not matter to the plan
cat "$real"/endpoint.[0-2].log | grep -oE -- '--- (PASS|FAIL|SKIP): [^ /]+ \(' | awk '{ print $3 }' | sort -u >"$real/endpoint.list"
ci/real-zfs-plan.sh "$real/endpoint.list" ci/real-zfs-weights.txt 1 3 >"$real/endpoint.plan" 2>/dev/null
expect ok "real run 40513522394: 3 shard logs, part 0/1" \
  ci/check-split.sh TestPropertyRoundTrip "$real/endpoint.list" "$real/endpoint.plan" 0/1 "$real"/endpoint.0.log "$real"/endpoint.1.log "$real"/endpoint.2.log
cat "$real"/endpoint.[0-2].log >"$real/endpoint.log"
: >"$real/other"
expect ok "real run: check-skips, whole list, nothing elsewhere" \
  env SKIPS_ELSEWHERE="$real/other" ci/check-skips.sh vm endpoint "$real/endpoint.log" "$real/endpoint.list"
expect ok "real run: check-skips as before (no SKIPS_ELSEWHERE)" \
  ci/check-skips.sh vm endpoint "$real/endpoint.log" "$real/endpoint.list"

# --- 2. synthetic 2 parts x 3 shards from the same list ----------------------
syn=$W/syn; mkdir -p "$syn"
cp "$real/endpoint.list" "$syn/"
ci/real-zfs-plan.sh "$syn/endpoint.list" ci/real-zfs-weights.txt 2 3 >"$syn/endpoint.plan"
# the real subtest names of TestPropertyRoundTrip, as rows 0..total-1
cat "$real"/endpoint.[0-2].log | grep -oE -- '--- PASS: TestPropertyRoundTrip/[^ ]+ \(' | awk '{ print $3 }' | sort -u >"$syn/rows"
tot=$(wc -l <"$syn/rows")
skips=$(sed 's/#.*//' ci/expected-skips.txt | awk '$1 == "vm" && $2 == "endpoint" { print $3 }')
mklog() { # mklog <shard> <n>
  local s=$1 n=$2
  awk -v s="$s" '$1 == s && $2 != "TestPropertyRoundTrip" { print $2 }' "$syn/endpoint.plan" | while read -r t; do
    echo "=== RUN   $t"
    if grep -qxF "$t" <<<"$skips"; then echo "--- SKIP: $t (0.00s)"; else echo "--- PASS: $t (0.10s)"; fi
  done
  echo "=== RUN   TestPropertyRoundTrip"
  k=$(awk -v s="$s" -v n="$n" '(NR - 1) % n == s' "$syn/rows" | wc -l)
  echo "    props_roundtrip_test.go:103: PROPRT_SHARD $s/$n: $k of $tot rows"
  awk -v s="$s" -v n="$n" '(NR - 1) % n == s { print "    --- PASS: " $0 " (0.01s)" }' "$syn/rows"
  echo "--- PASS: TestPropertyRoundTrip (1.00s)"
}
for s in 0 1 2 3 4 5; do mklog "$s" 6 >"$syn/endpoint.$s.log"; done
split_lists() { # split_lists <p>: endpoint.part<p>.list, endpoint.other<p>.list as real-zfs-vm.sh makes them
  local lo=$(($1 * 3)) hi=$(($1 * 3 + 3))
  : >"$syn/part$1"; : >"$syn/other$1"
  awk -v lo="$lo" -v hi="$hi" '$1 >= lo && $1 < hi { mine[$2] = 1 } { all[++n] = $2 }
    END { for (i = 1; i <= n; i++) { t = all[i]; if (seen[t]++) continue
            print t >(t in mine ? part : other) } }' part="$syn/part$1" other="$syn/other$1" "$syn/endpoint.plan"
}
split_lists 0; split_lists 1
echo "part lists: $(wc -l <"$syn/part0") + $(wc -l <"$syn/part1") - 1 (the split test in both) = $(($(wc -l <"$syn/part0") + $(wc -l <"$syn/part1") - 1)) of $(wc -l <"$syn/endpoint.list") listed"
L0=("$syn"/endpoint.0.log "$syn"/endpoint.1.log "$syn"/endpoint.2.log)
L1=("$syn"/endpoint.3.log "$syn"/endpoint.4.log "$syn"/endpoint.5.log)
cs() { ci/check-split.sh TestPropertyRoundTrip "$syn/endpoint.list" "$@"; }
expect ok "part 0/2: shards 0..2" cs "$syn/endpoint.plan" 0/2 "${L0[@]}"
expect ok "part 1/2: shards 3..5" cs "$syn/endpoint.plan" 1/2 "${L1[@]}"
cat "${L0[@]}" >"$syn/log0"; cat "${L1[@]}" >"$syn/log1"
expect ok "part 0/2 check-skips (its own skip budget)" env SKIPS_ELSEWHERE="$syn/other0" ci/check-skips.sh vm endpoint "$syn/log0" "$syn/part0"
expect ok "part 1/2 check-skips (its own skip budget)" env SKIPS_ELSEWHERE="$syn/other1" ci/check-skips.sh vm endpoint "$syn/log1" "$syn/part1"
expect fail "part 0/2 check-skips without SKIPS_ELSEWHERE: part 1's allowed skips look stale" ci/check-skips.sh vm endpoint "$syn/log0" "$syn/part0"
expect fail "part 1/2 check-skips with part 1's own list as elsewhere: its skips are unexpected" env SKIPS_ELSEWHERE="$syn/part1" ci/check-skips.sh vm endpoint "$syn/log1" "$syn/part1"

# --- 3. dropped tests -------------------------------------------------------
victim=$(awk '$1 == 4 && $2 != "TestPropertyRoundTrip" { print $2; exit }' "$syn/endpoint.plan")
echo "victim (planned in shard 4, part 1): $victim"
grep -vF -- "--- PASS: $victim (" "$syn/endpoint.4.log" >"$W/drop.4.log"; mkdir -p "$W/drop"; mv "$W/drop.4.log" "$W/drop/endpoint.4.log"
expect fail "dropped: part 1 never reports $victim" cs "$syn/endpoint.plan" 1/2 "$syn/endpoint.3.log" "$W/drop/endpoint.4.log" "$syn/endpoint.5.log"
grep -vE "^4 $victim\$" "$syn/endpoint.plan" >"$W/plan-drop"
expect fail "dropped: a plan without $victim (neither part runs it)" cs "$W/plan-drop" 0/2 "${L0[@]}"
expect fail "dropped: a whole shard's log missing from part 1" cs "$syn/endpoint.plan" 1/2 "$syn/endpoint.3.log" "$syn/endpoint.5.log"
expect fail "dropped: part 1 run against a 1-part plan (shards 3..5 do not exist)" cs "$syn/endpoint.plan" 0/1 "${L1[@]}"
mkdir -p "$W/droprow"; awk '/--- PASS: TestPropertyRoundTrip\// && !d++ { next } 1' "$syn/endpoint.5.log" >"$W/droprow/endpoint.5.log"
expect fail "dropped: one TestPropertyRoundTrip row in shard 5" cs "$syn/endpoint.plan" 1/2 "$syn/endpoint.3.log" "$syn/endpoint.4.log" "$W/droprow/endpoint.5.log"

# --- 4. duplicated tests ----------------------------------------------------
# the other job's test also run here (the guest ran the wrong shard set)
mkdir -p "$W/dup"; { cat "$syn/endpoint.0.log"; echo "--- PASS: $victim (0.10s)"; } >"$W/dup/endpoint.0.log"
expect fail "duplicated: part 0 also runs $victim, which the plan gives part 1" cs "$syn/endpoint.plan" 0/2 "$W/dup/endpoint.0.log" "$syn/endpoint.1.log" "$syn/endpoint.2.log"
{ cat "$syn/endpoint.plan"; echo "1 $victim"; } >"$W/plan-dup"
expect fail "duplicated: a plan with $victim in shards 4 and 1 (both parts)" cs "$W/plan-dup" 0/2 "${L0[@]}"
mkdir -p "$W/dup2"; { cat "$syn/endpoint.3.log"; echo "--- PASS: $victim (0.10s)"; } >"$W/dup2/endpoint.3.log"
expect fail "duplicated: $victim in shards 3 and 4 of part 1" cs "$syn/endpoint.plan" 1/2 "$W/dup2/endpoint.3.log" "$syn/endpoint.4.log" "$syn/endpoint.5.log"
expect fail "duplicated: the same shard log given twice" cs "$syn/endpoint.plan" 1/2 "$syn/endpoint.3.log" "$syn/endpoint.3.log" "$syn/endpoint.5.log"
# a row of shard 0 (part 0) also run in shard 3 (part 1): k and the subtest count disagree
row0=$(awk 'NR == 1' "$syn/rows")
mkdir -p "$W/duprow"; { cat "$syn/endpoint.3.log"; echo "    --- PASS: $row0 (0.01s)"; } >"$W/duprow/endpoint.3.log"
expect fail "duplicated: row $row0 (shard 0's) also in shard 3" cs "$syn/endpoint.plan" 1/2 "$W/duprow/endpoint.3.log" "$syn/endpoint.4.log" "$syn/endpoint.5.log"
# shard 3 ran the rows of a 3-way split (PROPRT_SHARD 0/3 instead of 3/6)
mkdir -p "$W/badn"; for s in 3 4 5; do cp "$syn/endpoint.$s.log" "$W/badn/"; done
sed -i 's#PROPRT_SHARD 3/6#PROPRT_SHARD 0/3#' "$W/badn/endpoint.3.log"
expect fail "wrong split: shard 3 ran PROPRT_SHARD 0/3" cs "$syn/endpoint.plan" 1/2 "$W/badn"/endpoint.{3,4,5}.log
# a plan that is a partition but not the planner's (a job with other weights)
awk '$2 == "TestRealFacts" { $1 = ($1 + 3) % 6 } 1' "$syn/endpoint.plan" >"$W/plan-moved"
expect fail "a plan moved by hand (not ci/real-zfs-plan.sh's)" cs "$W/plan-moved" 0/2 "${L0[@]}"

# --- 5. a planner bug: the partition check stands on its own ---------------
bp=$W/brokenci; mkdir -p "$bp"
cp ci/check-split.sh ci/real-zfs-weights.txt "$bp/"
for bug in drop dup; do
  { echo '#!/usr/bin/env bash'
    echo "\"$R/ci/real-zfs-plan.sh\" \"\$@\" | awk -v bug=$bug '\$2 == \"TestRealFacts\" { if (bug == \"drop\") next; print; print (\$1 + 3) % 6, \$2; next } 1'"
  } >"$bp/real-zfs-plan.sh"; chmod +x "$bp/real-zfs-plan.sh"
  "$bp/real-zfs-plan.sh" "$syn/endpoint.list" ci/real-zfs-weights.txt 2 3 >"$W/plan-$bug-bug" 2>/dev/null
  expect fail "planner bug ($bug TestRealFacts): the plan's partition check" \
    "$bp/check-split.sh" TestPropertyRoundTrip "$syn/endpoint.list" "$W/plan-$bug-bug" 0/2 "${L0[@]}"
done
grep -E " (TestNFSReplay|TestZFSDiffReplay|TestCrashAtEveryMutationConverges|TestRealFacts|TestModel)$" "$syn/endpoint.plan"
. /tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/latent/new.sh
echo "--- $pass as expected, $fail not"
[ "$fail" = 0 ]
