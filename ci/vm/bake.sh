#!/usr/bin/env bash
# Bake the real-ZFS VM bundle: an Ubuntu cloud image with OpenZFS and
# nfs-kernel-server already installed and cloud-init switched off, its kernel
# and initrd for direct boot, and an extracted (not installed) qemu for the
# host. ci/real-zfs-vm.sh boots it in a few seconds instead of provisioning a
# fresh image with cloud-init on every run.
#
# usage: ci/vm/bake.sh <ubuntu-series> <image-build> <openzfs-minor> <out-dir>
#   ci/vm/bake.sh stonking current 2.4 "$RUNNER_TEMP/zfsvm-bundle"
#
# Run it on a fresh ubuntu-24.04 runner, before qemu is installed: the qemu
# tree in the bundle is exactly the packages apt adds to that runner image.
# Needs /dev/kvm and sudo. The bundle is cached keyed by a hash of this
# script and ci/vm/image/ (see the workflows), and rebaked nightly because
# the series' "current" image moves. Nothing in it is trusted for validity:
# every boot re-asserts the OpenZFS version (ci/vm/guest-run.sh, then
# ci/real-zfs-vm.sh on the host).
#
# Bundle layout:
#   base.qcow2   the provisioned disk, zstd-compressed qcow2, booted read-only
#                (snapshot=on), so one bundle serves any number of runs
#   vmlinuz, initrd.img, cmdline   for -kernel/-initrd/-append
#   provenance   key=value: image serial and sha256, kernel, zfs versions,
#                bake time, bake inputs hash
#   qemu/        dpkg-deb -x of the qemu packages, run via LD_LIBRARY_PATH
set -euo pipefail

series=${1:?series, e.g. stonking}
build=${2:-current}
want=${3:-2.4}
out=${4:?output directory}
here=$(cd "$(dirname "$0")" && pwd)
keyring=/usr/share/keyrings/ubuntu-cloudimage-keyring.gpg
work=$(mktemp -d "${RUNNER_TEMP:-/tmp}/zfsbake.XXXXXX")
rm -rf "$out"
mkdir -p "$out" "$work/debs" "$work/share"
t0=$SECONDS
step() { echo "::notice::bake +$((SECONDS - t0))s: $*"; }

# 1. qemu for the bundle: the packages apt would add on this runner image,
# downloaded and extracted, then installed for the bake boot itself
sudo apt-get update -qq
mapfile -t debs < <(apt-get install -s --no-install-recommends qemu-system-x86 | awk '/^Inst /{ print $2 }')
[ "${#debs[@]}" -gt 0 ] || { echo "qemu is already installed: bake on a fresh runner" >&2; exit 1; }
(cd "$work/debs" && apt-get download "${debs[@]}")
for d in "$work"/debs/*.deb; do dpkg-deb -x "$d" "$out/qemu"; done
# absolute links (firmware into /usr/share/seabios, /usr/lib/ipxe) must
# point inside the tree
find "$out/qemu" -type l -lname '/*' -print0 | while IFS= read -r -d '' l; do
  ln -sfn "$(realpath -m --relative-to="$(dirname "$l")" "$out/qemu$(readlink "$l")")" "$l"
done
q=$out/qemu
if LD_LIBRARY_PATH=$q/usr/lib/x86_64-linux-gnu ldd "$q/usr/bin/qemu-system-x86_64" | grep 'not found'; then
  echo "the extracted qemu misses libraries" >&2; exit 1
fi
sudo apt-get install -y -qq --no-install-recommends "$work"/debs/*.deb qemu-utils cloud-image-utils >/dev/null
step "qemu extracted (${#debs[@]} packages)"

# 2. the base image: verify the signed checksum list, then the image
base="https://cloud-images.ubuntu.com/$series/$build"
file="$series-server-cloudimg-amd64.img"
curl -fsSL --retry 3 -o "$work/SHA256SUMS" "$base/SHA256SUMS"
curl -fsSL --retry 3 -o "$work/SHA256SUMS.gpg" "$base/SHA256SUMS.gpg"
gpgv --keyring "$keyring" "$work/SHA256SUMS.gpg" "$work/SHA256SUMS"
sum=$(awk -v f="$file" '$2 == f || $2 == "*" f { print $1 }' "$work/SHA256SUMS")
[ -n "$sum" ] || { echo "$file is not in $base/SHA256SUMS" >&2; exit 1; }
curl -fsSL --retry 3 -o "$work/disk.qcow2" "$base/$file"
echo "$sum  $work/disk.qcow2" | sha256sum -c -
# room for the packages; the test pools live on a guest tmpfs, not here
qemu-img resize -q "$work/disk.qcow2" 8G
step "image verified"

# 3. provision once with cloud-init: packages, the boot hook, the trims
inputs=$(cat "$here/bake.sh" "$here"/image/* | sha256sum | cut -c1-16)
cat >"$work/share/provenance.host" <<EOF
series=$series
build=$build
image_sha256=$sum
baked_at=$(date -u +%s)
bake_inputs=$inputs
EOF
b64() { base64 -w0 "$here/image/$1"; }
cat >"$work/user-data" <<EOF
#cloud-config
package_update: true
packages: [zfsutils-linux, nfs-kernel-server]
write_files:
  - { path: /usr/local/sbin/zfsci-boot, permissions: '0755', encoding: b64, content: $(b64 zfsci-boot) }
  - { path: /etc/systemd/system/zfsci.service, permissions: '0644', encoding: b64, content: $(b64 zfsci.service) }
  - { path: /usr/local/sbin/zfsci-bake, permissions: '0755', encoding: b64, content: $(b64 bake-guest.sh) }
runcmd:
  - [/usr/local/sbin/zfsci-bake, "$want"]
power_state: { mode: poweroff, condition: true }
EOF
cloud-localds "$work/seed.img" "$work/user-data"
# same machine, NIC and MAC as ci/real-zfs-vm.sh, so the netplan config
# cloud-init writes here matches at test time
timeout 600 qemu-system-x86_64 -machine q35,accel=kvm -cpu host -smp "$(nproc)" -m 4096 \
  -nodefaults -display none -no-reboot \
  -drive file="$work/disk.qcow2",if=virtio,format=qcow2,discard=unmap,cache=unsafe \
  -drive file="$work/seed.img",if=virtio,format=raw \
  -virtfs local,path="$work/share",mount_tag=out,security_model=none,id=out \
  -netdev user,id=n0 -device virtio-net-pci,netdev=n0,mac=52:54:00:12:34:56,romfile= \
  -serial file:"$work/bake-console.log" ||
  { tail -n 100 "$work/bake-console.log"; exit 1; }
[ -e "$work/share/baked" ] || { echo "the guest did not finish baking" >&2; tail -n 150 "$work/bake-console.log"; exit 1; }
step "provisioned"

# 4. the bundle: zstd qcow2 (actions/cache would not compress it further;
# the guest decompresses only the clusters it reads at boot)
qemu-img convert -O qcow2 -c -o compression_type=zstd "$work/disk.qcow2" "$out/base.qcow2"
cp "$work/share/vmlinuz" "$work/share/initrd.img" "$work/share/cmdline" "$work/share/provenance" "$out/"
chmod 0644 "$out/vmlinuz" "$out/initrd.img"
step "bundle $(du -sh "$out" | cut -f1)"
cat "$out/provenance"
rm -rf "$work"

# 5. self-test: the bundle boots on its own qemu and is OpenZFS $want
"$here/../real-zfs-vm.sh" smoke "$want" "$out"
step "smoke test passed"
