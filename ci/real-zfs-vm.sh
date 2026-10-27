#!/usr/bin/env bash
# Run the endpoint contract tests against real ZFS inside an Ubuntu cloud
# image booted under KVM. GitHub has no runner for a non-LTS Ubuntu, and ZFS
# lives in the kernel, so a container would test the host's ZFS instead.
#
# usage: ci/real-zfs-vm.sh <ubuntu-series> [image-build]
#   ci/real-zfs-vm.sh stonking current      # Ubuntu 26.10 RC track, OpenZFS 2.4.x
# Images come from the per-series tree (cloud-images.ubuntu.com/<series>/),
# whose files are named <series>-server-cloudimg-amd64.img; the releases/
# tree names them ubuntu-<version>-... instead.
set -euo pipefail

series=${1:?series, e.g. stonking}
build=${2:-current}
work=${RUNNER_TEMP:-/tmp}/zfsvm
mkdir -p "$work"
cd "$(git rev-parse --show-toplevel)"

# test binaries: static, so the VM needs no Go toolchain
for pkg in endpoint server; do
  CGO_ENABLED=0 go test -c -o "$work/$pkg.test" "./internal/$pkg"
done

img="$work/$series.img"
url="https://cloud-images.ubuntu.com/$series/$build/$series-server-cloudimg-amd64.img"
curl -fsSL --retry 3 -o "$img" "$url"
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
packages: [zfsutils-linux]
runcmd:
  - [modprobe, zfs]
EOF
cloud-localds "$work/seed.img" "$work/user-data"

# -display none, not -nographic: QEMU refuses -nographic with -daemonize
qemu-system-x86_64 -enable-kvm -cpu host -m 4096 -smp 2 -display none \
  -drive file="$img",if=virtio -drive file="$work/seed.img",if=virtio,format=raw \
  -netdev user,id=n0,hostfwd=tcp:127.0.0.1:2222-:22 -device virtio-net-pci,netdev=n0 \
  -serial file:"$work/console.log" -daemonize -pidfile "$work/qemu.pid"

# on failure, show what the guest printed: cloud-init and boot errors land there
trap 'rc=$?; [ $rc -eq 0 ] || { echo "--- guest console (tail) ---"; tail -n 80 "$work/console.log" 2>/dev/null; }; [ -f "$work/qemu.pid" ] && kill "$(cat "$work/qemu.pid")" 2>/dev/null; exit $rc' EXIT

common=(-i "$work/key" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 -o LogLevel=ERROR)
vm() { ssh "${common[@]}" -p 2222 ci@127.0.0.1 "$@"; }

for _ in $(seq 1 90); do vm true 2>/dev/null && break; sleep 5; done
# status exits non-zero on recoverable warnings; zfs version is the real check
vm 'cloud-init status --wait >/dev/null || true; zfs version; uname -r'

scp "${common[@]}" -P 2222 "$work/endpoint.test" "$work/server.test" ci@127.0.0.1:

vm 'set -e
  truncate -s 20G vdev0
  sudo zpool create -f -O compression=lz4 zfsep-test "$PWD/vdev0"
  rc=0
  sudo ZFS_TEST_POOL=zfsep-test ./endpoint.test -test.count=1 -test.v || rc=$?
  sudo ZFS_TEST_POOL=zfsep-test ./server.test -test.count=1 -test.v || rc=$?
  sudo zpool destroy -f zfsep-test
  exit $rc'
