#!/usr/bin/env bash
# runs banner() and wait_ssh() from ci/real-zfs-vm.sh against fake.py, vm stubbed
# usage: harness.sh <real-zfs-vm.sh> <stall|close|refuse> <T seconds> [read timeout] [qemu-exits]
set -euo pipefail
src=$1 mode=$2 T=$3 rto=${4:-0.3} die=${5:-}
here=$(cd "$(dirname "$0")" && pwd)
eval "$(awk '/^banner\(\) \{/' "$src")"
eval "$(awk '/^wait_ssh\(\) \{/,/^}/' "$src" | sed "s/echo 0\.3)/echo $rto)/")"
work=$(mktemp -d)
noinitrd=
sleep 1000 & q=$!
echo "$q" >"$work/qemu.pid"
if [ -n "$die" ]; then noinitrd=1; (sleep 0.5; kill "$q") & fi
vm() { :; }
boot() { echo "boot (fallback) called with $*"; sleep 1000 & echo $! >"$work/qemu.pid"; }
now_ms() { echo $(($(date +%s%N) / 1000000)); }
mark() { echo "$*"; }
python3 "$here/fake.py" "$mode" "$T" & f=$!
t0=$(now_ms)
wait_ssh
t1=$(now_ms)
kill "$f" "$(cat "$work/qemu.pid")" 2>/dev/null || true
echo "mode=$mode T=${T}s rto=$rto: seen at $((t1 - t0))ms (latency $((t1 - t0 - T * 1000))ms)"
rm -rf "${work:?}"
