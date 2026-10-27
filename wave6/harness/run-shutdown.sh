#!/bin/bash
# $1 = mode: main (SIGTERM to daemon only) | group (SIGTERM to the whole process group, like systemd KillMode=control-group)
H=$(dirname "$(readlink -f "$0")"); mode=$1
R=$(mktemp -d); export FAKE_LOG=$R/zfs.log; : > $FAKE_LOG
PATH=$H/bin:$PATH setsid $H/zfs-endpoint -parent tank/k8s -listen unix://$R/s.sock -lock-dir $R -drain 2s > $R/d.log 2>&1 &
sleep 0.5
D=$(pgrep -f "lock-dir $R" | head -1)
$H/lifeclient $R/s.sock > $R/c.log 2>&1 &
sleep 0.7
t0=$(date +%s.%N)
if [ "$mode" = group ]; then kill -TERM -- -$D; else kill -TERM $D; fi
while kill -0 $D 2>/dev/null; do sleep 0.05; done
t1=$(date +%s.%N)
echo "daemon exited after $(echo "$t1-$t0"|bc)s"
sleep 0.3
echo "--- zfs children still alive after daemon exit:"; pgrep -af "$H/bin/zfs" || echo none
sleep 0.5; cat $R/c.log; echo "--- daemon log"; cat $R/d.log; echo "--- fake zfs log"; cat $FAKE_LOG
pkill -f "$H/bin/zfs"; rm -rf $R
