#!/usr/bin/env bash
# Runs inside the baked guest as root (zfsci-boot, from the host's 9p share at
# /mnt/ci), once the host has built the test binaries into /mnt/ci/bin.
# Records what the host asserts on (out/version.txt), then runs the real-pool
# tests as concurrent shards, each on its own pool, and writes their -test.v
# logs and -test.list output to out/. Pass or fail is decided on the host by
# ci/real-zfs-vm.sh and ci/check-skips.sh, never here.
#
# usage: guest-run.sh full|smoke
#   smoke: version facts and one pool create/destroy only (the bake self-test)
set -uo pipefail
mode=${1:-full}
ci=/mnt/ci
out=$ci/out
mkdir -p "$out"
up() { cut -d' ' -f1 /proc/uptime; }
echo "guest-run start at uptime $(up)s"

# the facts the host's version assert reads, gathered on every boot: the
# image is cached, so this is what proves the kernel module and the
# userland in this run are OpenZFS 2.4
{
  zfs version 2>&1
  echo "sysfs: $(cat /sys/module/zfs/version 2>&1)"
  echo "uname: $(uname -r)"
  # shellcheck disable=SC1091
  . /etc/os-release
  echo "os: $VERSION_ID $VERSION_CODENAME"
  echo "exportfs: $(command -v exportfs || echo missing)"
} >"$out/version.txt"
cp /etc/zfsci/provenance "$out/provenance"
# nfs-server is ordered before us; poll briefly, never a fixed sleep
n=0
for _ in $(seq 1 300); do
  n=$(cat /proc/fs/nfsd/threads 2>/dev/null || echo 0)
  [ "$n" -gt 0 ] && break
  sleep 0.1
done
echo "nfsd: $n" >>"$out/version.txt"
ls -ld /etc/exports.d /var/lib/nfs/etab >"$out/nfs-context.txt" 2>&1

# pools on a guest tmpfs: real vdevs to the real kmod, with no virtio disk or
# host page cache in the txg sync path. Sparse 10G files; the tests write
# little, and the tmpfs cap keeps a runaway write an ENOSPC, not an OOM
vd=/run/zfsci
mkdir -p "$vd"
mount -t tmpfs -o size=70% zfsci "$vd"
mkpool() { truncate -s 10G "$vd/$1.vdev" && zpool create -f -O compression=lz4 "$1" "$vd/$1.vdev"; }

rc=0
if [ "$mode" = smoke ]; then
  mkpool zsmoke && zfs create zsmoke/x && zpool destroy -f zsmoke || rc=1
  echo "$rc" >"$out/rc.tmp" && mv "$out/rc.tmp" "$out/rc"
  exit "$rc"
fi

# exec from tmpfs, not 9p
mkdir -p "$vd/bin"
cp "$ci"/bin/*.test "$vd/bin/"
cd "$vd/bin" || exit 1
./endpoint.test -test.list '.*' >"$out/endpoint.list"
./csi.test -test.list '.*' >"$out/csi.list"

# shard <list> <weights> <n>: "<shard> <test>" per top-level test; longest
# first onto the lightest shard. The file-export tests stay in one shard:
# they share the one nfsd and its export table
shard() {
  awk 'FNR == NR { if ($1 !~ /^#/ && NF == 2) w[$1] = $2; next }
       /^(Test|Fuzz|Example)/ { g = ($1 ~ /^TestFileExport/) ? "TestFileExport*" : $1
                 wt[g] += ($1 in w) ? w[$1] : 0.1; m[g] = m[g] " " $1 }
       END { for (g in wt) printf "%.3f%s\n", wt[g], m[g] }' "$2" "$1" |
    sort -k1,1gr -k2,2 |
    awk -v n="$3" '{ b = 0; for (i = 1; i < n; i++) if (load[i] + 0 < load[b] + 0) b = i
                     load[b] += $1; for (j = 2; j <= NF; j++) print b, $j }'
}
ep_shards=3
shard "$out/endpoint.list" "$ci/test-weights.txt" "$ep_shards" >"$out/endpoint.plan"

t=(-test.count=1 -test.v -test.timeout=4m)
pids=() names=()
# run <name> <pool> <command...>: one shard, its own pool, in the background
run() {
  local name=$1 pool=$2
  shift 2
  mkpool "$pool" || { echo "zpool create $pool failed" >"$out/$name.log"; rc=1; return; }
  (
    s=$(up)
    env ZFS_TEST_POOL="$pool" ZFS_REQUIRE_REAL=1 "$@" >"$out/$name.log" 2>&1
    r=$?
    echo "$name rc=$r start=$s end=$(up)" >>"$out/shards.txt"
    exit $r
  ) &
  pids+=($!) names+=("$name")
}
for i in $(seq 0 $((ep_shards - 1))); do
  re=$(awk -v s="$i" '$1 == s { printf "%s%s", sep, $2; sep = "|" }' "$out/endpoint.plan")
  [ -n "$re" ] || continue
  run "endpoint.$i" "zep$i" ./endpoint.test "${t[@]}" -test.run "^($re)\$"
done
# csi: the non-sanity tests do not depend on the access type and run once
# (with mount, as before); csi-sanity runs for filesystems and for zvols
run csi-other zcsi0 env CSI_SANITY_ACCESS_TYPE=mount ./csi.test "${t[@]}" -test.skip '^TestSanity$'
run csi-mount zcsi1 env CSI_SANITY_ACCESS_TYPE=mount ./csi.test "${t[@]}" -test.run '^TestSanity$'
run csi-block zcsi2 env CSI_SANITY_ACCESS_TYPE=block ./csi.test "${t[@]}" -test.run '^TestSanity$'

for k in "${!pids[@]}"; do
  wait "${pids[$k]}" || { echo "${names[$k]} failed"; rc=1; }
done
# a pool that will not export means a test left something busy
for p in $(zpool list -H -o name); do
  case $p in zep* | zcsi*) zpool destroy -f "$p" || { echo "zpool destroy $p failed"; rc=1; } ;; esac
done
echo "guest-run end at uptime $(up)s, rc $rc"
echo "$rc" >"$out/rc.tmp" && mv "$out/rc.tmp" "$out/rc"
exit "$rc"
