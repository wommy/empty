
# --- 6. the row total against the committed pin (propRTRows) ---------------
pin=$(awk '$1 == "const" && $2 == "propRTRows" { print $4 }' internal/endpoint/props_roundtrip_test.go)
echo "pin propRTRows=$pin, rows in the real logs: $tot"
# the other part's job split one more row (another allowlist), consistently
# within itself: its shards' k, total and subtests all agree
t197=$W/t197; mkdir -p "$t197"
cp "$syn/endpoint.plan" "$syn/endpoint.list" "$t197/"
{ cat "$syn/rows"; echo "TestPropertyRoundTrip/filesystem/atime=extra"; } >"$t197/rows"
(syn=$t197; tot=$(wc -l <"$t197/rows"); for s in 3 4 5; do mklog "$s" 6 >"$t197/endpoint.$s.log"; done)
expect fail "mismatched total: part 1 split 197 rows, the pin is $pin" cs "$syn/endpoint.plan" 1/2 "$t197"/endpoint.{3,4,5}.log
expect ok "part 0 at the pin still passes" cs "$syn/endpoint.plan" 0/2 "${L0[@]}"
# a check-split without the pin's source (listed split test): an error, not a pass
np=$W/nopin/ci; mkdir -p "$np"; cp ci/check-split.sh ci/real-zfs-plan.sh ci/real-zfs-weights.txt "$np/"
expect fail "no propRTRows to read: refuses" "$np/check-split.sh" TestPropertyRoundTrip "$syn/endpoint.list" "$syn/endpoint.plan" 1/2 "${L1[@]}"

# --- 7. empty shards: a list too short for 6 shards, no split test ----------
em=$W/empty; mkdir -p "$em/out"
printf '%s\n' TestRealFacts TestModel >"$em/endpoint.list"
ci/real-zfs-plan.sh "$em/endpoint.list" ci/real-zfs-weights.txt 2 3 >"$em/endpoint.plan" 2>/dev/null
cat "$em/endpoint.plan"; cp "$em/endpoint.plan" "$em/out/"
# the guest's own endpoint loop (ci/real-zfs-guest.sh), with run() faked
guestloop=$(awk '/^  for i in \$\(seq \$\(\(ep_p/ { on = 1 } on { print } on && /^  done$/ { exit }' ci/real-zfs-guest.sh)
printf '%s\n' "$guestloop" | awk '{ print "    guest| " $0 }'
for p in 0 1; do
  (
    out=$em/out ep_p=$p ep_per=3 ep_shards=6 t=()
    has() { [ "$1" = endpoint ]; }
    run() { # run <name> <pool> env PROPRT_SHARD=.. ./endpoint.test ... -test.run ^(re)$
      local name=$1 re=${*: -1}; re=${re#^(}; re=${re%)\$}
      tr '|' '\n' <<<"$re" | while read -r x; do echo "=== RUN   $x"; echo "--- PASS: $x (0.10s)"; done >"$out/$name.log"
    }
    eval "$guestloop"
  )
done
ls -l "$em/out" | awk 'NR > 1 { print "    " $5, $NF }'
rm -f "$em/out/endpoint.plan"
E0=("$em"/out/endpoint.{0,1,2}.log) E1=("$em"/out/endpoint.{3,4,5}.log)
ecs() { ci/check-split.sh TestPropertyRoundTrip "$em/endpoint.list" "$em/endpoint.plan" "$@"; }
expect ok "empty shards, part 0: the guest's empty logs accepted" ecs 0/2 "${E0[@]}"
expect ok "empty shards, part 1: the guest's empty logs accepted" ecs 1/2 "${E1[@]}"
expect ok "empty shards: real-zfs-vm.sh's cat of the part's logs" cat "${E0[@]}" "${E1[@]}"
full0=$(awk '$1 < 3 { print $1; exit }' "$em/endpoint.plan")
empty0=$(for i in 0 1 2; do awk -v s="$i" '$1 == s { f = 1 } END { exit f }' "$em/endpoint.plan" && echo "$i"; done | awk 'NR == 1')
echo "part 0: shard $full0 has a test, shard $empty0 none"
mkdir -p "$em/miss"; for i in 0 1 2; do [ "$i" = "$empty0" ] || cp "$em/out/endpoint.$i.log" "$em/miss/"; done
expect fail "empty shard $empty0's log missing" ecs 0/2 "$em"/miss/endpoint.*.log
mkdir -p "$em/miss2"; for i in 0 1 2; do [ "$i" = "$full0" ] || cp "$em/out/endpoint.$i.log" "$em/miss2/"; done
expect fail "non-empty shard $full0's log missing" ecs 0/2 "$em"/miss2/endpoint.*.log
mkdir -p "$em/blank"; cp "${E0[@]}" "$em/blank/"; : >"$em/blank/endpoint.$full0.log"
expect fail "non-empty shard $full0's log empty" ecs 0/2 "$em"/blank/endpoint.{0,1,2}.log
mkdir -p "$em/stray"; cp "${E0[@]}" "$em/stray/"
x=$(awk -v s="$full0" '$1 == s { print $2; exit }' "$em/endpoint.plan")
echo "--- PASS: $x (0.10s)" >"$em/stray/endpoint.$empty0.log"
: >"$em/stray/endpoint.$full0.log"
expect fail "a test reported in empty shard $empty0 (planned in $full0)" ecs 0/2 "$em"/stray/endpoint.{0,1,2}.log
