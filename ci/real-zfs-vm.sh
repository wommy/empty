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
curl -fsSL --retry 3 -o "$img" "$base/$file"
echo "$sum  $img" | sha256sum -c -
qemu-img resize "$img" 20G

ssh-keygen -q -t ed25519 -N '' -f "$work/key" <<<y >/dev/null
cat >"$work/user-data" <<EOF
#cloud-config
users:
  - name: ci
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    ssh_authorized_keys: [$(cat "$work/key.pub")]
package_update: true
packages: [zfsutils-linux, nfs-kernel-server]
runcmd:
  - [modprobe, zfs]
  - [systemctl, enable, --now, nfs-server]
EOF
cloud-localds "$work/seed.img" "$work/user-data"

# boot first: the guest's apt install overlaps the test compile below
# -display none, not -nographic: QEMU refuses -nographic with -daemonize
qemu-system-x86_64 -enable-kvm -cpu host -m 4096 -smp 2 -display none \
  -drive file="$img",if=virtio -drive file="$work/seed.img",if=virtio,format=raw \
  -netdev user,id=n0,hostfwd=tcp:127.0.0.1:2222-:22 -device virtio-net-pci,netdev=n0 \
  -serial file:"$work/console.log" -daemonize -pidfile "$work/qemu.pid"

common=(-i "$work/key" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 -o LogLevel=ERROR)
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
trap 'rc=$?; [ $rc -eq 0 ] || diag; [ -f "$work/qemu.pid" ] && kill "$(cat "$work/qemu.pid")" 2>/dev/null; exit $rc' EXIT

# test binaries: static, so the VM needs no Go toolchain; built as the
# runner user, only the binaries run as root
for pkg in "${pkgs[@]}"; do
  CGO_ENABLED=0 go test -c -o "$work/$pkg.test" "./internal/$pkg"
done

up=
for _ in $(seq 1 90); do vm true 2>/dev/null && { up=1; break; }; sleep 5; done
[ -n "$up" ] || { echo "VM never became reachable over ssh" >&2; exit 1; }
# status exits non-zero on recoverable warnings; the version, module and
# nfsd checks below are the real gates
# wait for cloud-init by its marker file: `status --wait` dies with ssh 255
# when sshd restarts during the package install
done_=
for _ in $(seq 1 120); do vm 'test -e /var/lib/cloud/instance/boot-finished' 2>/dev/null && { done_=1; break; }; sleep 5; done
[ -n "$done_" ] || { echo "cloud-init never finished" >&2; exit 1; }
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
# what the file-export tests need besides running nfsd threads
vm 'command -v exportfs && test -d /etc/exports.d && sudo test -r /var/lib/nfs/etab' >/dev/null \
  || { echo "NFS prerequisites missing in the VM (exportfs, /etc/exports.d, etab)" >&2; exit 1; }
vm 'n=$(sudo cat /proc/fs/nfsd/threads 2>/dev/null || echo 0); echo "nfsd threads: $n"; [ "$n" -gt 0 ]' \
  || { echo "nfs-server is not running in the VM" >&2; exit 1; }

bins=()
for pkg in "${pkgs[@]}"; do bins+=("$work/$pkg.test"); done
scp "${common[@]}" -P 2222 "${bins[@]}" ci@127.0.0.1:
rc=0
vm 'set -eo pipefail
  # 10G on a 20G guest disk that also holds the OS: the sparse vdev must
  # not outgrow the filesystem under it, or the pool suspends on ENOSPC
  truncate -s 10G vdev0
  sudo zpool create -f -O compression=lz4 zfsep-test "$PWD/vdev0"
  env=(ZFS_TEST_POOL=zfsep-test ZFS_REQUIRE_REAL=1)
  rc=0
  t=(-test.count=1 -test.v -test.timeout=8m)
  ./endpoint.test -test.list ".*" >endpoint.list
  ./csi.test -test.list ".*" >csi.list
  sudo "${env[@]}" ./endpoint.test "${t[@]}" 2>&1 | tee endpoint.log || rc=$?
  # csi-sanity, Identity and Controller groups, for filesystems and zvols;
  # the non-sanity csi tests do not depend on the access type, run them once
  sudo "${env[@]}" CSI_SANITY_ACCESS_TYPE=mount ./csi.test "${t[@]}" 2>&1 | tee csi-mount.log || rc=$?
  sudo "${env[@]}" CSI_SANITY_ACCESS_TYPE=block ./csi.test "${t[@]}" -test.run "^TestSanity\$" 2>&1 | tee csi-block.log || rc=$?
  sudo zpool destroy -f zfsep-test
  exit $rc' || rc=$?

scp "${common[@]}" -P 2222 'ci@127.0.0.1:*.log' 'ci@127.0.0.1:*.list' "$work/" || rc=1
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
