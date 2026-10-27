#!/usr/bin/env bash
# Runs inside the real-ZFS guest, as root, from the directory ci/real-zfs-vm.sh
# copied the test binaries, this script and (endpoint) endpoint.plan into.
# Runs the real-pool tests as concurrent shards, each on its own pool, and
# leaves their -test.v logs and -test.list output in out/, plus
# out/test-times.tsv: per top-level test its own time, its time with
# parallel subtests summed, and its wall (ci/test-times.awk, which must be
# next to this script), which ci/real-zfs-weights.sh turns into
# ci/real-zfs-weights.txt. Pass or fail is
# decided on the host by ci/check-skips.sh on the merged logs; the exit code
# here only says whether every shard exited 0.
#
# ZFSVM_PKGS (ci/real-zfs-vm.sh, from the job's suite in
# ci/real-zfs-suites.txt) names the binaries this guest runs: endpoint, csi
# or both. Each CI job runs one suite on its own runner (ADR 0005).
#
# Shards: the endpoint binary's shards of this job's part, from
# endpoint.plan (ci/real-zfs-plan.sh, made on the host from
# ci/real-zfs-weights.txt): ZFSVM_EP_PART=<p>/<P> runs shards p*S..p*S+S-1
# of P*S, S=ZFSVM_EP_PER (3), so a suite split over P jobs (ci.yml) runs
# each shard in one of them. TestPropertyRoundTrip is in every shard, each
# running the rows i mod P*S == shard (PROPRT_SHARD; ci/check-split.sh
# checks that every row ran exactly once)
# (the NFS tests, TestFileExport* and TestNFSReplay, kept together: one
# nfsd, one export table; the replay writes malformed zfs.exports lines,
# which end exportfs's read of the file for every dataset; nightly, with
# ZFSDIFF_FULL=1, TestZFSDiffReplay joins them: its whole corpus turns
# shares on, which runs exportfs -ra on the same table), the csi
# tests other than csi-sanity, csi-sanity on filesystems, csi-sanity on
# zvols. One process per shard, each on its own pool, so no shard's
# datasets, space accounting or txg syncs meet another's.
#
# usage: sudo bash real-zfs-guest.sh
set -uo pipefail
out=$PWD/out
rm -rf "$out"
mkdir -p "$out"
up() { cut -d' ' -f1 /proc/uptime; }
echo "start at uptime $(up)s"

# pools on a tmpfs: real vdevs to the real kmod, without the guest disk,
# qcow2 and host page cache in every txg sync. Sparse 10G files, as before;
# the tests write little, and the tmpfs cap makes a runaway write an
# ENOSPC (pool suspends, test times out) rather than a guest OOM
vd=/run/zfsci
mkdir -p "$vd"
mountpoint -q "$vd" || mount -t tmpfs -o size=60% zfsci "$vd"
mkpool() { truncate -s 10G "$vd/$1.vdev" && zpool create -f -O compression=lz4 "$1" "$vd/$1.vdev"; }

pkgs=${ZFSVM_PKGS:-endpoint csi}
has() { case " $pkgs " in *" $1 "*) return 0 ;; esac; return 1; }
for p in $pkgs; do
  case $p in endpoint | csi) ;; *) echo "unknown package $p in ZFSVM_PKGS"; exit 2 ;; esac
  "./$p.test" -test.list '.*' >"$out/$p.list"
done

# the endpoint plan (ci/real-zfs-plan.sh): "<shard> <test>" over all P*S
# shards; this job runs shards p*S .. p*S+S-1
ep_part=${ZFSVM_EP_PART:-0/1} ep_per=${ZFSVM_EP_PER:-3}
ep_p=${ep_part%/*} ep_parts=${ep_part#*/}
ep_shards=$((ep_parts * ep_per))
if has endpoint; then
  [ -s endpoint.plan ] || { echo "no endpoint.plan beside the binaries"; exit 2; }
  cp endpoint.plan "$out/endpoint.plan"
fi

rc=0
t=(-test.count=1 -test.v -test.timeout=4m)
# TestNFSReplay's report (internal/endpoint/nfs_replay_test.go), copied back
# with the logs; ci/real-zfs-vm.sh puts it in the job summary
export NFS_REPLAY_REPORT=$out/nfs-replay.log
# TestZFSDiffReplay's report (internal/endpoint/zfsdiff_replay_test.go):
# real pool vs simulator vs reference model. ZFSDIFF_FULL=1 (nightly)
# replays the whole corpus instead of the PR sample, which turns no share
# on (zfsdiff.PRSample) and so need not share the NFS tests' shard
export ZFSDIFF_REPORT=$out/zfsdiff.log
export ZFSDIFF_FULL=${ZFSDIFF_FULL:-}
# ZFS_CRASH_FULL=1 (nightly): every crash point of every case on the real
# pool (internal/endpoint/crash_test.go crashSample), not the PR's rotation,
# and every destroy of TestDestroyNeverHitsAReplacedDataset (swapSweep), not
# only the first of each case
export ZFS_CRASH_FULL=${ZFS_CRASH_FULL:-}
# stamp: "@<unix seconds> " before each line, for ci/test-times.awk's wall
# column. Only the RUN/PAUSE/CONT and result lines reach it, so the shard
# log itself is byte-for-byte what the test binary wrote
stamp() {
  local l
  while IFS= read -r l; do printf '@%s %s\n' "$EPOCHREALTIME" "$l"; done
}
marks='=== (RUN|PAUSE|CONT) |--- (PASS|FAIL|SKIP): '
# guest load over the test phase, for deciding whether the VM job should
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
# where the phase's CPU went, by source (ADR 0005, "quiet guest"): forks
# (processes), context switches and interrupts from /proc/stat, interrupt
# totals from /proc/interrupts, block reads (pgpgin counts every block
# device; vda's sectors read come from /proc/diskstats, and the rest is
# zvols, which leave diskstats once destroyed), and the cgroup v2 CPU
# (cpu.stat) of the root, of the units that react to zfs(8) and its
# uevents, and of the session scope this script, and so the shards' tests
# and their zfs(8), run in. One awk per snapshot over the files that exist:
# an absent file or cgroup (a unit stopped, cgroup v1) is left out and
# prints as -. Diagnostics only, like snap: nothing here can fail the run
cg=/sys/fs/cgroup
cgself=$(awk -F: '$1 == "0" { print $3 }' /proc/self/cgroup 2>/dev/null) || cgself=
counters() {
  local a=() n f
  for f in up=/proc/uptime stat=/proc/stat irq=/proc/interrupts vm=/proc/vmstat disk=/proc/diskstats; do
    [ -r "${f#*=}" ] && a+=("tag=${f%%=*}" "${f#*=}")
  done
  local cgs=(root=. init=init.scope system=system.slice)
  [ -z "$cgself" ] || cgs+=("tests=${cgself#/}")
  for n in zfs-zed udisks2 systemd-udevd systemd-journald rsyslog; do cgs+=("$n=system.slice/$n.service"); done
  for n in "${cgs[@]}"; do
    f=$cg/${n#*=}/cpu.stat
    [ -r "$f" ] && a+=("tag=cg:${n%%=*}" "$f")
  done
  [ ${#a[@]} -gt 0 ] || return 0
  awk '
    tag == "up" { print "uptime", $1 }
    tag == "stat" && $1 == "cpu" { print "busy_ticks", $2 + $3 + $4 + $7 + $8 + $9 }
    tag == "stat" && ($1 == "processes" || $1 == "ctxt" || $1 == "intr") { print $1, $2 }
    tag == "irq" && FNR == 1 { ncpu = NF; next }
    tag == "irq" { s = 0; for (i = 2; i <= ncpu + 1 && $i ~ /^[0-9]+$/; i++) s += $i
                   irq += s; n = $1; sub(/:$/, "", n); if (n ~ /^(LOC|RES|CAL|TLB)$/) print "irq." n, s }
    tag == "vm" && ($1 == "pgpgin" || $1 == "pgfault" || $1 == "pgmajfault") { print $1, $2 }
    tag == "disk" && $3 == "vda" { print "vda_sectors_read", $6 }
    tag ~ /^cg:/ && ($1 == "usage_usec" || $1 == "system_usec") { print tag "." $1, $2 }
    END { if (ncpu) print "irq.total", irq }' "${a[@]}"
}
# counters.before, counters.after -> the table appended to load.txt (so to
# the job summary): rates over the phase, CPU-ms per fork (all busy CPU /
# forks, USER_HZ 100), block reads vda vs other (zvols), CPU-s per cgroup
counters_table() {
  [ -s "$1" ] && [ -s "$2" ] || { echo "guest counters: no before/after snapshot"; return 0; }
  awk '
    FNR == 1 { k++ }
    NF == 2 { v[k, $1] = $2 }
    function d(key) { return ((1, key) in v && (2, key) in v) ? v[2, key] - v[1, key] : "" }
    function f(x, fmt) { return x == "" ? "-" : sprintf(fmt, x) }
    function r(x, by) { return x == "" ? "" : x / by }
    END {
      wall = d("uptime")
      if (k < 2 || wall == "" || wall <= 0) { print "guest counters: no before/after snapshot"; exit }
      fk = d("processes"); cs = d("ctxt"); bt = d("busy_ticks")
      printf "forks %s (%s/s, %s CPU-ms each), context switches %s (%s/s)\n", f(fk, "%d"), f(r(fk, wall), "%.0f"),
        f(fk != "" && fk > 0 && bt != "" ? bt * 10 / fk : "", "%.1f"), f(cs, "%d"), f(r(cs, wall), "%.0f")
      printf "interrupts %s (/proc/stat); /proc/interrupts %s: LOC %s RES %s CAL %s TLB %s\n", f(d("intr"), "%d"),
        f(d("irq.total"), "%d"), f(d("irq.LOC"), "%d"), f(d("irq.RES"), "%d"), f(d("irq.CAL"), "%d"), f(d("irq.TLB"), "%d")
      pg = d("pgpgin"); vs = d("vda_sectors_read")
      printf "block reads: pgpgin %s MB, vda %s MB, other (zvols) %s MB; page faults %s (major %s)\n",
        f(r(pg, 1024), "%.1f"), f(r(vs, 2048), "%.1f"), f(pg == "" || vs == "" ? "" : pg / 1024 - vs / 2048, "%.1f"),
        f(d("pgfault"), "%d"), f(d("pgmajfault"), "%d")
      print "cgroup CPU-s over the phase (cpu.stat usage, of it system):"
      n = split("root tests init system zfs-zed udisks2 systemd-udevd systemd-journald rsyslog", cgs, " ")
      for (i = 1; i <= n; i++)
        printf "  %-17s %8s  (sys %s)\n", cgs[i], f(r(d("cg:" cgs[i] ".usage_usec"), 1e6), "%.2f"),
          f(r(d("cg:" cgs[i] ".system_usec"), 1e6), "%.2f")
    }' "$@"
}
snap >"$out/load.before" 2>/dev/null || true
counters >"$out/counters.before" 2>/dev/null || true
vmstat_pid=
if command -v vmstat >/dev/null; then
  vmstat -n -t 1 >"$out/vmstat.txt" 2>&1 &
  vmstat_pid=$!
fi
# the unit and its hardening drop-in (ci/real-zfs-unit.sh), beside the
# shards on a pool of its own: zfs-endpoint under systemd, a volume made
# and deleted, the admin socket's refusal of uid 65534. Its last log line
# is the verdict; it touches no shard's pool and turns no share on
# (ZFSVM_UNIT: one suite's job runs it, ci/real-zfs-vm.sh)
unit_pid=
if [ -n "${ZFSVM_UNIT:-}" ]; then
  unit_s=$(up)
  bash real-zfs-unit.sh "$PWD" >"$out/unit-hardening.log" 2>&1 &
  unit_pid=$!
fi
pids=() names=()
# run <name> <pool> <command...>: one shard on its own pool, in the background
run() {
  local name=$1 pool=$2
  shift 2
  mkpool "$pool" || { echo "zpool create $pool failed" | tee "$out/$name.log"; rc=1; return; }
  (
    s=$(up)
    env ZFS_TEST_POOL="$pool" ZFS_REQUIRE_REAL=1 "$@" 2>&1 | tee "$out/$name.log" |
      grep --line-buffered -E "$marks" | stamp >"$out/$name.marks"
    r=${PIPESTATUS[0]}
    echo "$name rc=$r ${s}s..$(up)s" >>"$out/shards.txt"
    exit $r
  ) &
  pids+=($!) names+=("$name")
}
# -test.parallel=8 for the endpoint shards: their parallel tests wait on
# zfs(8) and the kernel more than on CPU, and a shard's solo tail ran 4 at a
# time on 4 vCPUs (run 40494213342); the csi shards are mostly sequential
if has endpoint; then
  for i in $(seq $((ep_p * ep_per)) $((ep_p * ep_per + ep_per - 1))); do
    re=$(awk -v s="$i" '$1 == s { printf "%s%s", sep, $2; sep = "|" }' "$out/endpoint.plan")
    # a shard the plan gives no test: an empty log, which ci/check-split.sh
    # takes as exactly that (a missing log fails it)
    [ -n "$re" ] || { : >"$out/endpoint.$i.log"; continue; }
    run "endpoint.$i" "zep$i" env PROPRT_SHARD="$i/$ep_shards" \
      ./endpoint.test "${t[@]}" -test.parallel=8 -test.run "^($re)\$"
  done
fi
# csi: the non-sanity tests do not depend on the access type and run once,
# with mount as before; csi-sanity runs for filesystems and for zvols
if has csi; then
  run csi-other zcsi0 env CSI_SANITY_ACCESS_TYPE=mount ./csi.test "${t[@]}" -test.skip '^TestSanity$'
  run csi-mount zcsi1 env CSI_SANITY_ACCESS_TYPE=mount ./csi.test "${t[@]}" -test.run '^TestSanity$'
  run csi-block zcsi2 env CSI_SANITY_ACCESS_TYPE=block ./csi.test "${t[@]}" -test.run '^TestSanity$'
fi

for k in "${!pids[@]}"; do
  wait "${pids[$k]}" || { echo "${names[$k]} failed"; rc=1; }
done
if [ -n "$unit_pid" ]; then
  wait "$unit_pid" || rc=1
  echo "unit check (started at ${unit_s}s): $(tail -n 1 "$out/unit-hardening.log")"
fi
snap >"$out/load.after" 2>/dev/null || true
counters >"$out/counters.after" 2>/dev/null || true
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
    printf "guest load over the test phase: %.1fs on %d vCPUs\n", wall, nproc
    printf "cpu: busy %.0f%% (user %.0f%%, sys+irq %.0f%%, steal %.0f%%), idle %.0f%%, iowait %.0f%%\n",
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
counters_table "$out/counters.before" "$out/counters.after" >>"$out/load.txt" 2>&1 || true
cat "$out/load.txt" 2>/dev/null || true
cat "$out/shards.txt"
# per top-level test: own time, time with parallel subtests summed, wall
# (ci/test-times.awk); ci/real-zfs-weights.sh turns it into the weights.
# mode=full marks a nightly run, which the weights must not learn from
mode=pr
[ -z "$ZFSDIFF_FULL$ZFS_CRASH_FULL" ] || mode=full
{
  echo "# shard test status parallel own_s cost_s wall_s subtests (ci/test-times.awk)"
  # run: the run (and attempt) whose parts sum into one measurement
  # (ci/real-zfs-weights.sh); part: which part of the endpoint plan
  echo "# nproc=$(nproc) mode=$mode run=${ZFSVM_RUN:-local} part=$ep_part"
  for f in "$out"/*.marks; do
    awk -v shard="$(basename "$f" .marks)" -f test-times.awk "$f"
  done
} >"$out/test-times.tsv" || echo "test-times.tsv incomplete (timing only, not a failure)"
rm -f "$out"/*.marks
# as before: a pool that will not go means a test left something busy
for p in $(zpool list -H -o name); do
  case $p in zep[0-9] | zcsi[0-9]) zpool destroy -f "$p" || { echo "zpool destroy $p failed"; rc=1; } ;; esac
done
chown -R "${SUDO_UID:-0}:${SUDO_GID:-0}" "$out"
echo "end at uptime $(up)s, rc $rc"
exit "$rc"
