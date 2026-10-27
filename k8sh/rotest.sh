#!/bin/bash
# Does connect() to a unix socket work through a read-only bind mount?
set -eo pipefail
d=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/k8sh/ro
mkdir -p "$d/src" "$d/ro"
rm -f "$d/src/s.sock"
python3 -I -c '
import socket,sys,os,time
s=socket.socket(socket.AF_UNIX); s.bind(sys.argv[1]); s.listen(1)
if os.fork()==0:
    c,_=s.accept(); c.send(b"ok"); c.close(); os._exit(0)
time.sleep(30)' "$d/src/s.sock" &
srv=$!
sleep 1
unshare -m bash -ec "
mount --bind '$d/src' '$d/ro'
mount -o remount,bind,ro '$d/ro'
grep ' $d/ro ' /proc/self/mountinfo
touch '$d/ro/x' 2>&1 || true
python3 -I -c 'import socket,sys; c=socket.socket(socket.AF_UNIX); c.connect(sys.argv[1]); print(\"connect over ro bind:\", c.recv(2))' '$d/ro/s.sock'
"
kill $srv || true
