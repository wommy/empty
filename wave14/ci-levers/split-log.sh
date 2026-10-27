#!/usr/bin/env bash
# split-log.sh <job log> <out dir> <repo>: split a GitHub job log's
# ::group:: sections (shard logs, systemd-analyze) into files, and run
# ci/test-times.awk over the shard logs into <out dir>/tt.tsv
set -euo pipefail
f=$1 d=$2 repo=$3
mkdir -p "$d"
awk -v d="$d" '{ sub(/^\xef\xbb\xbf/, ""); line = substr($0, 30) }
  line ~ /^##\[group\]/ { g = substr(line, 10); out = ""; if (g ~ /\.log$/ || g == "systemd-analyze") out = d "/" g; next }
  line ~ /^##\[endgroup\]/ { out = ""; next }
  out != "" { print line > out }' "$f"
{
  echo "# nproc=4 mode=pr"
  for s in endpoint.0 endpoint.1 endpoint.2 csi-other csi-mount csi-block; do
    [ -f "$d/$s.log" ] && awk -v shard="$s" -f "$repo/ci/test-times.awk" "$d/$s.log"
  done
} >"$d/tt.tsv"
