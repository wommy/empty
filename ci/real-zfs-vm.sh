#!/usr/bin/env bash
# Run the endpoint contract tests against real ZFS inside an Ubuntu cloud
# image booted under KVM. GitHub has no runner for a non-LTS Ubuntu, and ZFS
# lives in the kernel, so a container would test the host's ZFS instead.
#
# usage: ci/real-zfs-vm.sh <ubuntu-series> [image-build] [openzfs-minor]
#   ci/real-zfs-vm.sh stonking current 2.4  # Ubuntu 26.10 RC track, OpenZFS 2.4.x
# Images come from the per-series tree (cloud-images.ubuntu.com/<series>/),
# whose files are named <series>-server-cloudimg-amd64.img; the releases/
# tree names them ubuntu-<version>-... instead. The image is verified
# against SHA256SUMS, itself verified with the Ubuntu cloud image key
# (/usr/share/keyrings/ubuntu-cloudimage-keyring.gpg, from ubuntu-keyring).
# Both `zfs version` lines (userland and kmod) must be <openzfs-minor>.x.
#
# ZFSVM_CACHE=<dir> keeps the provisioned guest (packages installed) between
# runs: guest apt took 47-73s per run, varying with the mirror. A cold run
# provisions, powers off and stores the disk; a warm run boots a copy-on-
# write overlay of it and skips the image download and apt. The workflow
# keys the directory by the image's sha256 and the hash of
# ci/real-zfs-cloud-config.yaml (what provisioning installs). Every
# boot still runs the version, module and nfsd checks below, so a stale
# disk fails them rather than testing another ZFS.
set -euo pipefail

series=${1:?series, e.g. stonking}
build=${2:-current}
want=${3:-2.4}
work=${RUNNER_TEMP:-/tmp}/zfsvm
summary=${GITHUB_STEP_SUMMARY:-/dev/null}
keyring=/usr/share/keyrings/ubuntu-cloudimage-keyring.gpg
# only packages whose tests reach a real pool through ZFS_TEST_POOL;
# internal/server and internal/zfs run on the simulator in the unit job
pkgs=(endpoint csi)
cache=${ZFSVM_CACHE:-}
mkdir -p "$work"
cd "$(git rev-parse --show-toplevel)"

# image: verify the signed checksum list, then the image against it
base="https://cloud-images.ubuntu.com/$series/$build"
file="$series-server-cloudimg-amd64.img"
img="$work/$series.img"
curl -fsSL --retry 3 -o "$work/SHA256SUMS" "$base/SHA256SUMS"
curl -fsSL --retry 3 -o "$work/SHA256SUMS.gpg" "$base/SHA256SUMS.gpg"
gpgv --keyring "$keyring" "$work/SHA256SUMS.gpg" "$work/SHA256SUMS"
sum=$(awk -v f="$file" '$2 == f || $2 == "*" f { print $1 }' "$work/SHA256SUMS")
[ -n "$sum" ] || { echo "$file is not in $base/SHA256SUMS" >&2; exit 1; }
warm=
if [ -n "$cache" ] && [ -f "$cache/ok" ] && [ "$(cat "$cache/sum")" = "$sum" ]; then
  warm=1
  echo "warm guest: $cache (provisioned from image $sum)"
  cp "$cache/key" "$cache/key.pub" "$cache/seed.img" "$work/"
else
  curl -fsSL --retry 3 -o "$img" "$base/$file"
  echo "$sum  $img" | sha256sum -c -
  qemu-img resize "$img" 20G
  ssh-keygen -q -t ed25519 -N '' -f "$work/key" <<<y >/dev/null
fi
sed "s|@SSH_KEY@|$(cat "$work/key.pub")|" ci/real-zfs-cloud-config.yaml >"$work/user-data"
[ -n "$warm" ] || cloud-localds "$work/seed.img" "$work/user-data"

# -display none, not -nographic: QEMU refuses -nographic with -daemonize.
# A warm boot reuses the cold boot's seed, though the provisioned disk has
# cloud-init disabled. All the runner's vCPUs and 6G: the tests run as six
# concurrent shards with their pools on a guest tmpfs.
boot() {
  qemu-system-x86_64 -enable-kvm -cpu host -m 6144 -smp "$(nproc)" -display none \
    -drive file="$1",if=virtio,discard=unmap,cache=unsafe -drive file="$work/seed.img",if=virtio,format=raw \
    -netdev user,id=n0,hostfwd=tcp:127.0.0.1:2222-:22 -device virtio-net-pci,netdev=n0 \
    -serial file:"$work/console.log" -daemonize -pidfile "$work/qemu.pid"
}
overlay() {
  qemu-img create -q -f qcow2 -F qcow2 -b "$cache/disk.qcow2" "$work/run.qcow2" 20G
  boot "$work/run.qcow2"
}
# boot first: provisioning (or a warm boot) overlaps the test compile below
if [ -n "$warm" ]; then overlay; else boot "$img"; fi

# one TCP+SSH handshake for the whole run, not one per command
common=(-i "$work/key" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 -o LogLevel=ERROR
  -o ControlMaster=auto -o ControlPath="$work/ssh-%C" -o ControlPersist=300)
vm() { ssh "${common[@]}" -p 2222 ci@127.0.0.1 "$@"; }

# on failure, show what the guest printed (console: boot errors) and, if it
# answers, cloud-init's own account (apt failures land there); the workflow
# uploads console.log as an artifact
diag() {
  echo "--- guest console (tail) ---"
  tail -n 80 "$work/console.log" 2>/dev/null || true
  echo "--- cloud-init (best effort) ---"
  timeout 60 ssh "${common[@]}" -p 2222 ci@127.0.0.1 \
    'cloud-init status --long; sudo tail -n 80 /var/log/cloud-init-output.log; sudo dmesg | grep -iE "zfs|spl|nfsd" | tail -n 20' || true
}
trap 'rc=$?; [ $rc -eq 0 ] || diag; ssh "${common[@]}" -O exit -p 2222 ci@127.0.0.1 2>/dev/null || true; kill "$(cat "$work/qemu.pid" 2>/dev/null)" 2>/dev/null || true; exit $rc' EXIT

# test binaries: static, so the VM needs no Go toolchain; built as the
# runner user, only the binaries run as root
for pkg in "${pkgs[@]}"; do
  CGO_ENABLED=0 go test -c -o "$work/$pkg.test" "./internal/$pkg"
done

wait_ssh() {
  local up=
  # sshd refuses at once until it is up, so a short poll costs nothing
  for _ in $(seq 1 900); do vm true 2>/dev/null && { up=1; break; }; sleep 0.2; done
  [ -n "$up" ] || { echo "VM never became reachable over ssh" >&2; exit 1; }
}
wait_ssh
# status exits non-zero on recoverable warnings; the version, module and
# nfsd checks below are the real gates
# wait for cloud-init by its marker file: `status --wait` dies with ssh 255
# when sshd restarts during the package install
done_=
for _ in $(seq 1 600); do vm 'test -e /var/lib/cloud/instance/boot-finished' 2>/dev/null && { done_=1; break; }; sleep 1; done
[ -n "$done_" ] || { echo "cloud-init never finished" >&2; exit 1; }
if [ -z "$warm" ] && [ -n "$cache" ]; then
  # store the provisioned disk, then carry on from an overlay of it, as a
  # warm run would; failing to store only costs the next run its speed
  t0=$SECONDS
  # trimmed (discard=unmap) and zstd-compressed: the cached disk shrinks
  # from 2.3G, which every warm run restores and writes out
  vm 'sudo apt-get clean; sudo fstrim -av; sudo sync'
  vm 'sudo systemctl poweroff' 2>/dev/null || true
  ssh "${common[@]}" -O exit -p 2222 ci@127.0.0.1 2>/dev/null || true
  for _ in $(seq 1 300); do kill -0 "$(cat "$work/qemu.pid")" 2>/dev/null || break; sleep 0.2; done
  if ! kill -0 "$(cat "$work/qemu.pid")" 2>/dev/null; then
    rm -rf "$cache" && mkdir -p "$cache"
    qemu-img convert -m 16 -W -c -O qcow2 -o compression_type=zstd "$img" "$cache/disk.qcow2"
    cp "$work/key" "$work/key.pub" "$work/seed.img" "$cache/"
    echo "$sum" >"$cache/sum"
    # cacheable only once a boot from it passes the checks below
    stored=1
    echo "stored provisioned guest in $cache ($(du -sh "$cache" | cut -f1), $((SECONDS - t0))s)"
    overlay
  else
    echo "guest did not power off; not caching it" >&2
    kill "$(cat "$work/qemu.pid")" 2>/dev/null || true
    sleep 2
    boot "$img"
  fi
  wait_ssh
fi
# services (zfs module, nfsd) come up after sshd on a reboot; wait for the
# two the tests need, not for the whole boot to settle
vm 'for _ in $(seq 1 300); do systemctl is-active -q nfs-server && break; sleep 0.1; done'
# cloud-init's runcmd ran only on the provisioning boot
vm 'sudo modprobe zfs'
vm 'cloud-init status --long || true; cat /etc/cloud/build.info; uname -r'

ver=$(vm 'zfs version 2>&1' || true) # a missing module fails below, with the output
echo "$ver"
serial=$(vm 'sed -n "s/^serial: *//p" /etc/cloud/build.info')
{
  echo "### real ZFS VM: $series/$build"
  echo "- image serial: \`$serial\`, sha256 \`$sum\` (gpg-verified SHA256SUMS)"
  echo "- kernel: \`$(vm 'uname -r')\`"
  echo "- zfs version: \`$(echo "$ver" | tr '\n' ' ')\`"
} >>"$summary"
grep -Eq "^zfs-${want//./\\.}\." <<<"$ver" && grep -Eq "^zfs-kmod-${want//./\\.}\." <<<"$ver" \
  || { echo "expected OpenZFS $want.x userland and kmod, got:" >&2; echo "$ver" >&2; exit 1; }
[ "$(sed -n 's/^zfs-kmod-//p' <<<"$ver")" = "$(sed -n 's/^zfs-\([0-9]\)/\1/p' <<<"$ver")" ] \
  || { echo "userland and kmod versions differ:" >&2; echo "$ver" >&2; exit 1; }
# what the file-export tests need besides running nfsd threads. Only
# exportfs: libzfs creates /etc/exports.d on the first share, and
# exportfs writes /var/lib/nfs/etab when it first exports
vm 'sudo sh -c "command -v exportfs"' >/dev/null \
  || { echo "exportfs missing in the VM (nfs-kernel-server not installed?)" >&2; exit 1; }
vm 'ls -ld /etc/exports.d /var/lib/nfs/etab 2>&1 || true' # context for an export test failure
vm 'n=$(sudo cat /proc/fs/nfsd/threads 2>/dev/null || echo 0); echo "nfsd threads: $n"; [ "$n" -gt 0 ]' \
  || { echo "nfs-server is not running in the VM" >&2; exit 1; }
if [ -n "${stored:-}" ]; then
  touch "$cache/ok"
  echo "the stored guest booted and passed the version, module and nfsd checks: cacheable"
fi

bins=()
for pkg in "${pkgs[@]}"; do bins+=("$work/$pkg.test"); done
scp "${common[@]}" -P 2222 "${bins[@]}" ci/real-zfs-guest.sh ci/real-zfs-weights.txt ci@127.0.0.1:
rc=0
# six concurrent shards on six tmpfs-backed pools (ci/real-zfs-guest.sh);
# the logs come back per shard and are merged per check below.
# ZFSDIFF_FULL=1 (nightly.yml) replays the whole zfsdiff corpus
vm "sudo env ZFSDIFF_FULL=${ZFSDIFF_FULL:-} bash real-zfs-guest.sh" || rc=$?
mkdir -p "$work/out"
scp "${common[@]}" -P 2222 'ci@127.0.0.1:out/*' "$work/out/" || rc=1
o=$work/out
for f in "$o"/*.log; do
  echo "::group::$(basename "$f")"
  cat "$f"
  echo "::endgroup::"
done
# what check-skips reads: endpoint = the union of its shards (it must
# cover endpoint.list); csi-mount = the non-sanity tests plus csi-sanity on
# filesystems (it must cover csi.list); csi-block = csi-sanity on zvols
cat "$o"/endpoint.[0-9].log >"$work/endpoint.log" || rc=1
cat "$o/csi-other.log" "$o/csi-mount.log" >"$work/csi-mount.log" || rc=1
cp "$o/csi-block.log" "$o/endpoint.list" "$o/csi.list" "$work/" || rc=1
# TestNFSReplay (internal/endpoint) replays a sharenfs corpus through the
# real libshare, exportfs and etab and diffs it against the reference
# models (internal/nfsref) and the simulator; the guest points its report
# at out/nfs-replay.log. Its last line is the tally.
if [ -f "$o/nfs-replay.log" ]; then
  { echo "<details><summary>NFS replay: $(tail -n 1 "$o/nfs-replay.log")</summary>"
    echo; echo '```'; cat "$o/nfs-replay.log"; echo '```'; echo "</details>"; echo; } >>"$summary"
else
  echo "::error::vm/endpoint: TestNFSReplay wrote no nfs-replay.log"; rc=1
fi
# TestZFSDiffReplay replays zfs(8) sequences (internal/zfs/zfsdiff) on the
# pool, the simulator and the reference model. Divergences are reported,
# not failed (ZFSDIFF_STRICT=1 would); the summary gets the verdict per
# sequence, the workflow uploads the whole report. Its last line is the
# tally with the replay's wall time.
if [ -f "$o/zfsdiff.log" ]; then
  { echo "<details><summary>zfsdiff replay: $(tail -n 1 "$o/zfsdiff.log")</summary>"
    echo; echo '```'; sed -n '3,/^$/p' "$o/zfsdiff.log"; echo '```'; echo "</details>"; echo; } >>"$summary"
else
  echo "::error::vm/endpoint: TestZFSDiffReplay wrote no zfsdiff.log"; rc=1
fi
{
  echo "| leg | package | skips | skipped tests |"
  echo "|---|---|---|---|"
} >>"$summary"
# csi-sanity spec floor: Identity + Controller specs run on the simulator
# (44 of 103 with csi-test v5.6); a regex or skip regression drops below it
ci/check-skips.sh vm endpoint "$work/endpoint.log" "$work/endpoint.list" || rc=1
ci/check-skips.sh vm csi "$work/csi-mount.log" "$work/csi.list" 44 || rc=1
ci/check-skips.sh vm csi "$work/csi-block.log" "" 44 || rc=1
exit $rc
