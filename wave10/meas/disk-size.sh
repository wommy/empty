#!/usr/bin/env bash
# Measure what the guest cache costs in each disk format, on the unprovisioned
# stonking cloud image (the provisioned one adds ~100-200MB of packages and
# apt lists): plain qcow2 (c73e40d) vs zstd-compressed qcow2 (speed-vm), and
# the size of each as actions/cache would archive it (tar | zstdmt).
set -euo pipefail
S=$(cd "$(dirname "$0")" && pwd)
d=$S/disk
mkdir -p "$d"
q=$S/../../qemu-tree/bin/qemu-img
cd "$d"
[ -s base.img ] || curl -fsSL -o base.img https://cloud-images.ubuntu.com/stonking/current/stonking-server-cloudimg-amd64.img
"$q" info base.img | grep -E 'file format|virtual size|disk size|compression'
t() { local a; a=$(date +%s.%N); "$@"; awk -v a="$a" -v b="$(date +%s.%N)" 'BEGIN { printf "  %.1fs\n", b - a }'; }
echo "convert to plain qcow2 (as c73e40d stores it):"; t "$q" convert -O qcow2 base.img plain.qcow2
echo "convert to zstd qcow2:"; t "$q" convert -c -O qcow2 -o compression_type=zstd base.img zstd.qcow2
ls -l plain.qcow2 zstd.qcow2 | awk '{ printf "  %s %d MB\n", $9, $5 / 1048576 }'
for f in plain.qcow2 zstd.qcow2; do
  echo "tar|zstdmt of $f (what actions/cache uploads/downloads):"
  t sh -c "tar -cf - $f | zstdmt -q -o $f.tzst -f"
  ls -l "$f.tzst" | awk '{ printf "  %d MB\n", $5 / 1048576 }'
  echo "  extract:"
  rm -rf x && mkdir x
  t sh -c "zstdmt -dc $f.tzst | tar -xf - -C x"
done
