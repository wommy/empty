#!/bin/bash
# Shutdown harness against the real binary and a fake zfs.
# usage: run-shutdown.sh MODE FAKE_SLEEP DRAIN
#   MODE: main  SIGTERM to the daemon only (systemd KillMode=mixed)
#         group SIGTERM to the whole process group (a terminal's Ctrl-C,
#               or what KillMode=control-group does to the whole cgroup)
#         kill9 SIGKILL to the daemon (crash): its zfs must die with it
#   FAKE_SLEEP: seconds the in-flight `zfs get available` takes
#   DRAIN: the daemon's -drain
H=$(dirname "$(readlink -f "$0")"); mode=$1; export FAKE_SLEEP=$2; drain=$3
R=$(mktemp -d); export FAKE_LOG=$R/zfs.log; : > "$FAKE_LOG"
PATH=$H/bin:$PATH setsid "$H/zfs-endpoint" -parent tank/k8s -listen "unix://$R/s.sock" -drain "$drain" -stop-wait 5s > "$R/d.log" 2>&1 &
for _ in $(seq 50); do [ -S "$R/s.sock" ] && break; sleep 0.1; done
D=$(pgrep -f "listen unix://$R/s.sock" | head -1)
"$H/lifeclient" "$R/s.sock" > "$R/c.log" 2>&1 &
sleep 0.7
t0=$(date +%s.%N)
case $mode in
  group) kill -TERM -- "-$D";;
  kill9) kill -KILL "$D";;
  *) kill -TERM "$D";;
esac
while kill -0 "$D" 2>/dev/null; do sleep 0.05; done
t1=$(date +%s.%N)
echo "mode=$mode zfs_takes=${FAKE_SLEEP}s drain=$drain: daemon exited after $(echo "$t1-$t0" | bc)s"
sleep 0.3
echo "--- zfs children still alive after daemon exit:"; pgrep -af "$H/bin/zfs" || echo none
wait
cat "$R/c.log"; echo "--- daemon log"; cat "$R/d.log"; echo "--- fake zfs log"; cat "$FAKE_LOG"
pkill -f "$H/bin/zfs"; rm -rf "$R"
