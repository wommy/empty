#!/usr/bin/env bash
# Runs inside the real-ZFS guest, as root, from the directory ci/real-zfs-vm.sh
# copied the test binaries, this script and ci/real-zfs-weights.txt into.
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
# Shards: the endpoint binary split three ways by ci/real-zfs-weights.txt,
# with TestPropertyRoundTrip in all three, each running a third of its rows
# (PROPRT_SHARD; ci/check-split.sh checks that every row ran exactly once)
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

# shard <list> <weights> <n>: "<shard> <test>" per top-level test, fuzz
# target (its seed corpus) and example, as check-skips counts them; longest
# first onto the lightest shard. A split test (split=) runs in every shard,
# each on its own part of its subtests (TestPropertyRoundTrip reads
# PROPRT_SHARD=<i>/<n>, set below), so its weight goes 1/n to each shard
# before the rest are placed
nfs='^(TestFileExport|TestNFSReplay$)'
[ "${ZFSDIFF_FULL:-}" = 1 ] && nfs='^(TestFileExport|TestNFSReplay$|TestZFSDiffReplay$)'
split='^TestPropertyRoundTrip$'
shard() {
  awk -v nfs="$nfs" -v sp="$split" 'FNR == NR { if ($1 !~ /^#/ && NF == 2) w[$1] = $2; next }
       /^(Test|Fuzz|Example)/ { x = ($1 in w) ? w[$1] : 0.1
                 if ($1 ~ sp) { printf "S %.3f %s\n", x, $1; next }
                 g = ($1 ~ nfs) ? "nfs" : $1
                 wt[g] += x; m[g] = m[g] " " $1 }
       END { for (g in wt) printf "%.3f%s\n", wt[g], m[g] }' "$2" "$1" |
    sort -k1,1gr -k2,2 |
    awk -v n="$3" '$1 == "S" { for (i = 0; i < n; i++) { load[i] += $2 / n; print i, $3 }; next }
                   { g[++k] = $0 }
                   END { for (x = 1; x <= k; x++) {
                           c = split(g[x], f, " "); b = 0
                           for (i = 1; i < n; i++) if (load[i] + 0 < load[b] + 0) b = i
                           load[b] += f[1]; for (j = 2; j <= c; j++) print b, f[j] } }'
}
ep_shards=3
! has endpoint || shard "$out/endpoint.list" real-zfs-weights.txt "$ep_shards" >"$out/endpoint.plan"

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
# pool (internal/endpoint/crash_test.go crashSample), not the PR's rotation
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
snap >"$out/load.before" 2>/dev/null || true
vmstat_pid=
if command -v vmstat >/dev/null; then
  vmstat -n -t 1 >"$out/vmstat.txt" 2>&1 &
  vmstat_pid=$!
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
  for i in $(seq 0 $((ep_shards - 1))); do
    re=$(awk -v s="$i" '$1 == s { printf "%s%s", sep, $2; sep = "|" }' "$out/endpoint.plan")
    [ -n "$re" ] || continue
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
cat "$out/load.txt" 2>/dev/null || true
cat "$out/shards.txt"
# per top-level test: own time, time with parallel subtests summed, wall
# (ci/test-times.awk); ci/real-zfs-weights.sh turns it into the weights.
# mode=full marks a nightly run, which the weights must not learn from
mode=pr
[ -z "$ZFSDIFF_FULL$ZFS_CRASH_FULL" ] || mode=full
{
  echo "# shard test status parallel own_s cost_s wall_s subtests (ci/test-times.awk)"
  echo "# nproc=$(nproc) mode=$mode"
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
