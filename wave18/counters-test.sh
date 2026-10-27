#!/usr/bin/env bash
# exercise counters()/counters_table() from ci/real-zfs-guest.sh against this
# container's /proc and a fake cgroup tree, with some cgroups/files absent
set -eo pipefail
G=${1:?guest script}
S=${2:?scratch dir}
mkdir -p "$S"
eval "$(awk '/^cg=\/sys\/fs\/cgroup/ { on = 1 } /^snap >"\$out\/load.before"/ { on = 0 } on' "$G")"
echo "real cgself=[$cgself]"
fake() { # fake cgroup tree: root, tests, zfs-zed (removed before 'after'), journald; no udisks2/rsyslog/udevd
  mkdir -p "$S/cg/system.slice/zfs-zed.service" "$S/cg/system.slice/systemd-journald.service" "$S/cg/user.slice/session-3.scope" "$S/cg/init.scope"
  printf 'usage_usec %s\nuser_usec 1\nsystem_usec %s\n' "$1" "$2" >"$S/cg/cpu.stat"
  printf 'usage_usec %s\nuser_usec 1\nsystem_usec %s\n' "$3" "$4" >"$S/cg/user.slice/session-3.scope/cpu.stat"
  printf 'usage_usec %s\nsystem_usec %s\n' "$5" "$6" >"$S/cg/system.slice/zfs-zed.service/cpu.stat"
  printf 'usage_usec %s\nsystem_usec %s\n' "$7" "$8" >"$S/cg/system.slice/systemd-journald.service/cpu.stat"
  printf 'usage_usec 5\n' >"$S/cg/init.scope/cpu.stat"
}
cg=$S/cg cgself=/user.slice/session-3.scope
fake 1000000 500000 200000 100000 3000000 2500000 10 5
t0=$EPOCHREALTIME
counters >"$S/before"
t1=$EPOCHREALTIME
# some work: forks and reads
for _ in $(seq 1 200); do /bin/true; done
cat /usr/bin/* >/dev/null 2>&1 || true
fake 84000000 57000000 30000000 20000000 9000000 7000000 2000000 1500000
rm -f "$S/cg/init.scope/cpu.stat" # absent after: delta must print -
counters >"$S/after"
echo "one snapshot: $(awk -v a="$t0" -v b="$t1" 'BEGIN { printf "%.1fms", (b - a) * 1000 }')"
echo "--- before"; cat "$S/before"
echo "--- table"; counters_table "$S/before" "$S/after"
echo "--- only one snapshot"; counters_table "$S/before" "$S/missing" 2>/dev/null || echo "(awk rc $?, guest ignores it)"
echo "--- no cgroups, no /proc files at all"
cg=$S/nonexistent cgself=
counters >"$S/nocg"; echo "lines without cgroups: $(wc -l <"$S/nocg")"
