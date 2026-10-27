#!/usr/bin/env bash
# Runs inside the real-ZFS guest, as root, from the directory ci/real-zfs-vm.sh
# copied the test binaries, this script and ci/real-zfs-weights.txt into.
# Runs the real-pool tests as concurrent shards, each on its own pool, and
# leaves their -test.v logs and -test.list output in out/. Pass or fail is
# decided on the host by ci/check-skips.sh on the merged logs; the exit code
# here only says whether every shard exited 0.
#
# Shards: the endpoint binary split three ways by ci/real-zfs-weights.txt
# (the NFS tests, TestFileExport* and TestNFSReplay, kept together: one
# nfsd, one export table; the replay writes malformed zfs.exports lines,
# which end exportfs's read of the file for every dataset; nightly, with
# ZFSDIFF_FULL=1, TestZFSDiffReplay joins them: its whole corpus turns
# shares on, which runs exportfs -ra on the same table), the csi
# tests other than csi-sanity, csi-sanity on filesystems, csi-sanity on
# zvols. Six processes on six pools, so no shard's datasets, space
# accounting or txg syncs meet another's.
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

./endpoint.test -test.list '.*' >"$out/endpoint.list"
./csi.test -test.list '.*' >"$out/csi.list"

# shard <list> <weights> <n>: "<shard> <test>" per top-level test, fuzz
# target (its seed corpus) and example, as check-skips counts them; longest
# first onto the lightest shard
nfs='^(TestFileExport|TestNFSReplay$)'
[ "${ZFSDIFF_FULL:-}" = 1 ] && nfs='^(TestFileExport|TestNFSReplay$|TestZFSDiffReplay$)'
shard() {
  awk -v nfs="$nfs" 'FNR == NR { if ($1 !~ /^#/ && NF == 2) w[$1] = $2; next }
       /^(Test|Fuzz|Example)/ { g = ($1 ~ nfs) ? "nfs" : $1
                 wt[g] += ($1 in w) ? w[$1] : 0.1; m[g] = m[g] " " $1 }
       END { for (g in wt) printf "%.3f%s\n", wt[g], m[g] }' "$2" "$1" |
    sort -k1,1gr -k2,2 |
    awk -v n="$3" '{ b = 0; for (i = 1; i < n; i++) if (load[i] + 0 < load[b] + 0) b = i
                     load[b] += $1; for (j = 2; j <= NF; j++) print b, $j }'
}
ep_shards=3
shard "$out/endpoint.list" real-zfs-weights.txt "$ep_shards" >"$out/endpoint.plan"

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
pids=() names=()
# run <name> <pool> <command...>: one shard on its own pool, in the background
run() {
  local name=$1 pool=$2
  shift 2
  mkpool "$pool" || { echo "zpool create $pool failed" | tee "$out/$name.log"; rc=1; return; }
  (
    s=$(up)
    env ZFS_TEST_POOL="$pool" ZFS_REQUIRE_REAL=1 "$@" >"$out/$name.log" 2>&1
    r=$?
    echo "$name rc=$r ${s}s..$(up)s" >>"$out/shards.txt"
    exit $r
  ) &
  pids+=($!) names+=("$name")
}
for i in $(seq 0 $((ep_shards - 1))); do
  re=$(awk -v s="$i" '$1 == s { printf "%s%s", sep, $2; sep = "|" }' "$out/endpoint.plan")
  [ -n "$re" ] || continue
  run "endpoint.$i" "zep$i" ./endpoint.test "${t[@]}" -test.run "^($re)\$"
done
# csi: the non-sanity tests do not depend on the access type and run once,
# with mount as before; csi-sanity runs for filesystems and for zvols
run csi-other zcsi0 env CSI_SANITY_ACCESS_TYPE=mount ./csi.test "${t[@]}" -test.skip '^TestSanity$'
run csi-mount zcsi1 env CSI_SANITY_ACCESS_TYPE=mount ./csi.test "${t[@]}" -test.run '^TestSanity$'
run csi-block zcsi2 env CSI_SANITY_ACCESS_TYPE=block ./csi.test "${t[@]}" -test.run '^TestSanity$'

for k in "${!pids[@]}"; do
  wait "${pids[$k]}" || { echo "${names[$k]} failed"; rc=1; }
done
cat "$out/shards.txt"
# as before: a pool that will not go means a test left something busy
for p in $(zpool list -H -o name); do
  case $p in zep[0-9] | zcsi[0-9]) zpool destroy -f "$p" || { echo "zpool destroy $p failed"; rc=1; } ;; esac
done
chown -R "${SUDO_UID:-0}:${SUDO_GID:-0}" "$out"
echo "end at uptime $(up)s, rc $rc"
exit "$rc"
