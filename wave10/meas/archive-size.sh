#!/usr/bin/env bash
# Size and pack/unpack time of each disk format as actions/cache archives it.
set -euo pipefail
cd "$(dirname "$0")/disk"
[ -e zstdW.qcow2 ] && mv zstdW.qcow2 zstd.qcow2
for f in plain.qcow2 zstd.qcow2; do
  a=$(date +%s.%N)
  tar -cf - "$f" | zstdmt -q -f -o "$f.tzst"
  b=$(date +%s.%N)
  rm -rf x && mkdir x
  zstdmt -dc "$f.tzst" | tar -xf - -C x
  c=$(date +%s.%N)
  awk -v f="$f" -v s="$(stat -c %s "$f.tzst")" -v a="$a" -v b="$b" -v c="$c" \
    'BEGIN { printf "%s: archive %d MB, pack %.1fs, unpack %.1fs\n", f, s / 1048576, b - a, c - b }'
done
rm -rf x
