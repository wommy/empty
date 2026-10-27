import sys
p = sys.argv[1]
s = open(p).read()

old = '''pids=() names=()
# run <name> <pool> <command...>: one shard on its own pool, in the background
'''
new = '''# guest load over the test phase, for deciding whether the VM job should
# be split across two runners (ADR 0005): the CPU counters of /proc/stat and
# the pressure (PSI) totals of /proc/pressure/{cpu,io,memory} before and
# after, summarised in out/load.txt, and vmstat 1 across the phase in
# out/vmstat.txt. Diagnostics only: nothing here can fail the run
snap() {
  echo "uptime $(up)"
  awk '$1 == "cpu"' /proc/stat
  for r in cpu io memory; do
    [ -r "/proc/pressure/$r" ] && awk -v r="$r" '{ print "psi", r, $0 }' "/proc/pressure/$r"
  done
}
snap >"$out/load.before" 2>/dev/null || true
vmstat_pid=
if command -v vmstat >/dev/null; then
  vmstat -n -t 1 >"$out/vmstat.txt" 2>&1 &
  vmstat_pid=$!
fi
pids=() names=()
# run <name> <pool> <command...>: one shard on its own pool, in the background
'''
assert s.count(old) == 1
s = s.replace(old, new)

old = '''for k in "${!pids[@]}"; do
  wait "${pids[$k]}" || { echo "${names[$k]} failed"; rc=1; }
done
cat "$out/shards.txt"
'''
new = '''for k in "${!pids[@]}"; do
  wait "${pids[$k]}" || { echo "${names[$k]} failed"; rc=1; }
done
snap >"$out/load.after" 2>/dev/null || true
[ -z "$vmstat_pid" ] || { kill "$vmstat_pid" 2>/dev/null; wait "$vmstat_pid" 2>/dev/null; } || true
# busy = all but idle and iowait; steal = a vCPU runnable while the host ran
# something else. PSI "some" = share of the phase in which at least one
# task stalled on that resource, "full" = all non-idle tasks stalled
awk -v nproc="$(nproc)" '
  FNR == 1 { k++ }
  $1 == "uptime" { t[k] = $2 }
  $1 == "cpu" { tot = 0; for (i = 2; i <= NF; i++) tot += $i
                c[k, "tot"] = tot; c[k, "user"] = $2 + $3; c[k, "sys"] = $4 + $7 + $8
                c[k, "idle"] = $5; c[k, "iowait"] = $6; c[k, "steal"] = $9 + 0 }
  $1 == "psi" { for (i = 4; i <= NF; i++) if ($i ~ /^total=/) { v = $i; sub(/^total=/, "", v); ps[k, $2 " " $3] = v; have[$2 " " $3] = 1 } }
  END {
    wall = t[2] - t[1]
    d = c[2, "tot"] - c[1, "tot"]
    if (k < 2 || wall <= 0 || d <= 0) { print "guest load: no before/after snapshot"; exit }
    pct = function_unused
    printf "guest load over the test phase: %.1fs on %d vCPUs\\n", wall, nproc
    printf "cpu: busy %.0f%% (user %.0f%%, sys+irq %.0f%%, steal %.0f%%), idle %.0f%%, iowait %.0f%%\\n",
      100 * (d - (c[2, "idle"] - c[1, "idle"]) - (c[2, "iowait"] - c[1, "iowait"])) / d,
      100 * (c[2, "user"] - c[1, "user"]) / d, 100 * (c[2, "sys"] - c[1, "sys"]) / d,
      100 * (c[2, "steal"] - c[1, "steal"]) / d,
      100 * (c[2, "idle"] - c[1, "idle"]) / d, 100 * (c[2, "iowait"] - c[1, "iowait"]) / d
    n = split("cpu some|cpu full|io some|io full|memory some|memory full", ks, "|")
    line = ""
    for (i = 1; i <= n; i++) if (ks[i] in have)
      line = line sprintf("%s%s %.0f%%", line == "" ? "" : ", ", ks[i], 100 * (ps[2, ks[i]] - ps[1, ks[i]]) / (wall * 1e6))
    print "pressure (stalled share of the phase): " (line == "" ? "PSI unavailable" : line)
  }' "$out/load.before" "$out/load.after" >"$out/load.txt" 2>&1 || true
cat "$out/load.txt" 2>/dev/null || true
cat "$out/shards.txt"
'''
assert s.count(old) == 1
s = s.replace(old, new)
s = s.replace("    pct = function_unused\n", "")
open(p, 'w').write(s)
