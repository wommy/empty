#!/usr/bin/env bash
# stub-testtimes.sh <repo>: the old (7eb878a) and new ci/test-times.awk on
# real shard logs (run 40497090324, run 40494213342), under mawk as in the
# guest. Only rows whose parallel subtests sit under a sequential subtest
# may change (TestModel: cost 9.10 -> 3.05); everything else identical.
# Plus a synthetic log: nested parallel under parallel still counts.
set -euo pipefail
repo=$1
here=$(cd "$(dirname "$0")" && pwd)
aw=$(command -v mawk || command -v awk)
t=$(mktemp -d)
trap 'rm -rf "$t"' EXIT
for d in "$here/logs/r54" /tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave13/cp/shards; do
  for f in "$d"/endpoint.*.log "$d"/csi-*.log; do
    [ -f "$f" ] || continue
    s=$(basename "$f" .log)
    "$aw" -v shard="$s" -f "$here/test-times.old.awk" "$f" >>"$t/old.$(basename "$d")"
    "$aw" -v shard="$s" -f "$repo/ci/test-times.awk" "$f" >>"$t/new.$(basename "$d")"
  done
  echo "== $(basename "$d"): rows that changed"
  diff "$t/old.$(basename "$d")" "$t/new.$(basename "$d")" || true
done
changed=$({ diff "$t/old.r54" "$t/new.r54" || true; } | awk '/^>/ { print $3 }' | sort -u | tr '\n' ' ')
[ "$changed" = "TestModel " ] || { echo "FAIL: changed rows: $changed"; exit 1; }
awk -F '\t' '$2 == "TestModel" && $6 == "3.050" { ok = 1 } END { exit !ok }' "$t/new.r54" || { echo "FAIL: TestModel cost"; exit 1; }
diff -q "$t/old.shards" "$t/new.shards" >/dev/null || { echo "FAIL: run 40494213342 rows changed"; exit 1; }
# synthetic: P parallel top with parallel child P/a (1s) holding parallel P/a/x (2s);
# S sequential top with sequential S/g holding parallel S/g/y (3s), own 3s
cat >"$t/syn.log" <<'EOF'
=== RUN   P
=== PAUSE P
=== RUN   S
=== RUN   S/g
=== RUN   S/g/y
=== PAUSE S/g/y
=== CONT  S/g/y
--- PASS: S (3.00s)
    --- PASS: S/g (0.00s)
        --- PASS: S/g/y (3.00s)
=== CONT  P
=== RUN   P/a
=== PAUSE P/a
=== CONT  P/a
=== RUN   P/a/x
=== PAUSE P/a/x
=== CONT  P/a/x
--- PASS: P (0.00s)
    --- PASS: P/a (1.00s)
        --- PASS: P/a/x (2.00s)
EOF
"$aw" -v shard=x -f "$repo/ci/test-times.awk" "$t/syn.log" | tee "$t/syn.tsv"
awk -F '\t' '$2 == "S" && $6 != "3.000" { bad = 1 } $2 == "P" && $6 != "3.000" { bad = 1 } END { exit bad }' "$t/syn.tsv" ||
  { echo "FAIL: synthetic costs"; exit 1; }
echo "PASS: stub-testtimes ($aw)"
