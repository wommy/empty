#!/usr/bin/env bash
# Build a relocatable qemu tree for the real-ZFS job: the packages apt would
# add to this runner image for qemu-system-x86 and qemu-utils, downloaded and
# unpacked with dpkg-deb (no dpkg database, no triggers, no apt-get update on
# a warm run), plus wrappers in <dir>/bin that point the binaries at the
# tree's libraries and firmware. The workflow caches <dir> per runner image
# version; on a cache hit this script only re-verifies it.
#
# usage: ci/qemu-tree.sh <dir>
#   then: export PATH=<dir>/bin:$PATH
# Exits non-zero if the tree does not run here; the caller then falls back
# to apt-get install.
set -euo pipefail

dir=${1:?tree directory}

verify() {
  "$dir/bin/qemu-system-x86_64" --version | head -n 1 &&
    "$dir/bin/qemu-img" --version | head -n 1 &&
    # the firmware a pc boot with virtio-net loads, through the wrapper's -L
    for f in bios-256k.bin kvmvapic.bin linuxboot_dma.bin vgabios-stdvga.bin efi-virtio.rom; do
      [ -e "$dir/usr/share/qemu/$f" ] || [ -e "$dir/usr/share/seabios/$f" ] || [ -e "$dir/usr/lib/ipxe/qemu/$f" ] ||
        { echo "missing firmware $f" >&2; return 1; }
    done
}

if [ -x "$dir/bin/qemu-system-x86_64" ]; then
  verify && exit 0
  echo "cached qemu tree does not run on this runner image; rebuilding" >&2
fi

rm -rf "$dir"
mkdir -p "$dir/debs" "$dir/bin"
sudo apt-get update -qq
# exactly what apt would install on this image: no more (no recommends),
# no less (a library the image lacks)
mapfile -t pkgs < <(apt-get install -s --no-install-recommends qemu-system-x86 qemu-utils | awk '/^Inst /{ print $2 }')
if [ "${#pkgs[@]}" -eq 0 ]; then
  # already installed on this image: take the packages themselves
  pkgs=(qemu-system-x86 qemu-system-common qemu-system-data qemu-utils seabios ipxe-qemu)
fi
chmod 0777 "$dir/debs" # apt downloads as _apt
(cd "$dir/debs" && apt-get download "${pkgs[@]}" >/dev/null)
for d in "$dir"/debs/*.deb; do dpkg-deb -x "$d" "$dir"; done
rm -rf "$dir/debs"
# absolute links (firmware into /usr/share/seabios, /usr/lib/ipxe) must
# resolve inside the tree
find "$dir" -type l -lname '/*' -print0 | while IFS= read -r -d '' l; do
  ln -sfn "$(realpath -m --relative-to="$(dirname "$l")" "$dir$(readlink "$l")")" "$l"
done
# wrappers find the tree from their own path, so a cache restored
# elsewhere still works
cat >"$dir/bin/qemu-system-x86_64" <<'EOF'
#!/bin/sh
t=$(dirname "$(readlink -f "$0")")/..
LD_LIBRARY_PATH=$t/usr/lib/x86_64-linux-gnu:$t/lib/x86_64-linux-gnu${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH} \
  QEMU_MODULE_DIR=$t/usr/lib/x86_64-linux-gnu/qemu \
  exec "$t/usr/bin/qemu-system-x86_64" -L "$t/usr/share/qemu" -L "$t/usr/share/seabios" -L "$t/usr/lib/ipxe/qemu" "$@"
EOF
cat >"$dir/bin/qemu-img" <<'EOF'
#!/bin/sh
t=$(dirname "$(readlink -f "$0")")/..
LD_LIBRARY_PATH=$t/usr/lib/x86_64-linux-gnu:$t/lib/x86_64-linux-gnu${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH} \
  exec "$t/usr/bin/qemu-img" "$@"
EOF
chmod +x "$dir/bin/qemu-system-x86_64" "$dir/bin/qemu-img"
printf '%s\n' "${pkgs[@]}" >"$dir/packages"
verify
