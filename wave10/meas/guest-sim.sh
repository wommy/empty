#!/usr/bin/env bash
# Run ci/real-zfs-guest.sh locally with no ZFS: zpool/mount stubs, and test
# binaries wrapped to drop ZFS_TEST_POOL/ZFS_REQUIRE_REAL (so they run on the
# simulator). Then merge the logs as ci/real-zfs-vm.sh does and run
# check-skips. Expected: coverage and spec floor pass; the endpoint leg fails
# only with "allowed to skip ... but it ran" (the simulator ran Sim tests),
# which is check-skips proving the binary was not on a real pool.
set -uo pipefail
repo=$1
S=$(cd "$(dirname "$0")" && pwd)
g=$S/guest-sim
rm -rf "$g"
mkdir -p "$g/stub" "$g/home"
for b in zpool mountpoint mount truncate; do printf '#!/bin/sh\nexit 0\n' >"$g/stub/$b"; chmod +x "$g/stub/$b"; done
for p in endpoint csi; do
  cp "$S/out/$p.test" "$g/home/$p.real"
  printf '#!/bin/sh\nunset ZFS_TEST_POOL ZFS_REQUIRE_REAL\nexec "$(dirname "$0")/%s.real" "$@"\n' "$p" >"$g/home/$p.test"
  chmod +x "$g/home/$p.test"
done
cp "$repo/ci/real-zfs-guest.sh" "$repo/ci/real-zfs-weights.txt" "$g/home/"
cd "$g/home"
a=$(date +%s.%N)
PATH=$g/stub:$PATH bash real-zfs-guest.sh
echo "guest script exit $? in $(awk -v a="$a" -v b="$(date +%s.%N)" 'BEGIN{printf "%.1f", b-a}')s"
o=$g/home/out
cat "$o"/endpoint.[0-9].log >"$g/endpoint.log"
cat "$o/csi-other.log" "$o/csi-mount.log" >"$g/csi-mount.log"
cp "$o/csi-block.log" "$o/endpoint.list" "$o/csi.list" "$g/"
cd "$repo"
ci/check-skips.sh vm endpoint "$g/endpoint.log" "$g/endpoint.list" 2>&1 | grep -vE 'allowed to skip only' | head; echo "endpoint check rc ${PIPESTATUS[0]}"
ci/check-skips.sh vm csi "$g/csi-mount.log" "$g/csi.list" 44; echo "csi-mount check rc $?"
ci/check-skips.sh vm csi "$g/csi-block.log" "" 44; echo "csi-block check rc $?"
grep -h 'Ran [0-9]* of' "$o"/csi-*.log
