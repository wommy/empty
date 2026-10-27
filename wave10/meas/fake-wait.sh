#!/usr/bin/env bash
# Dry-run `ci/real-zfs-vm.sh wait` without KVM: replay run 40456494050's real
# guest output (split into the new shard files) into a fake share, with a
# sleep standing in for qemu. Proves the host-side assert, the log merge and
# the check-skips wiring; says nothing about boot or test speed.
# usage: fake-wait.sh <repo> <case>   case: good | kmod-2.3 | missing-test | stale-kernel
set -uo pipefail
repo=$1 case=${2:-good}
S=$(cd "$(dirname "$0")" && pwd)
export RUNNER_TEMP=$S/fake-$case GITHUB_STEP_SUMMARY=$S/fake-$case.summary.md
rm -rf "$RUNNER_TEMP" "$GITHUB_STEP_SUMMARY"
work=$RUNNER_TEMP/zfsvm
o=$work/share/out
mkdir -p "$o" "$RUNNER_TEMP/bundle"
python3 -I - "$S/../vmjob.log" "$o" <<'EOF'
import re, sys
lines = open(sys.argv[1], encoding="utf-8-sig").read().splitlines()
out = sys.argv[2]
strip = lambda l: re.sub(r'^\S+Z ', '', l, count=1)
body = [strip(l) for l in lines]
start = next(i for i, l in enumerate(body) if l.startswith('nfsd threads')) + 1
segs, cur = [], []
for l in body[start:]:
    if l.startswith('skip budget'):
        break
    cur.append(l)
    if l == 'PASS':
        segs.append(cur); cur = []
ep, csim, csib = segs[:3]
# split the endpoint log into 3 pseudo-shards at test boundaries
n = len(ep); cuts = [0, n // 3, 2 * n // 3, n]
def boundary(k):
    while k < n and not ep[k].startswith('=== RUN'):
        k += 1
    return k
cuts = [0, boundary(cuts[1]), boundary(cuts[2]), n]
for i in range(3):
    open(f"{out}/endpoint.{i}.log", "w").write("\n".join(ep[cuts[i]:cuts[i+1]]) + "\n")
k = next(i for i, l in enumerate(csim) if l.startswith('=== RUN   TestSanity'))
open(f"{out}/csi-other.log", "w").write("\n".join(csim[:k]) + "\nPASS\n")
open(f"{out}/csi-mount.log", "w").write("\n".join(csim[k:]) + "\n")
open(f"{out}/csi-block.log", "w").write("\n".join(csib) + "\n")
EOF
"$S/out/endpoint.test" -test.list '.*' >"$o/endpoint.list"
"$S/out/csi.test" -test.list '.*' >"$o/csi.list"
kmod=2.4.4-1ubuntu4
[ "$case" = kmod-2.3 ] && kmod=2.3.4-1ubuntu1
cat >"$o/version.txt" <<EOF
zfs-2.4.4-1ubuntu4
zfs-kmod-$kmod
sysfs: $kmod
uname: 7.3.0-9-generic
os: 26.10 stonking
exportfs: /usr/sbin/exportfs
nfsd: 16
EOF
kernel=7.3.0-9-generic
[ "$case" = stale-kernel ] && kernel=7.3.0-8-generic
cat >"$RUNNER_TEMP/bundle/provenance" <<EOF
series=stonking
build=current
image_sha256=feedface
baked_at=$(date -u +%s)
bake_inputs=0123456789abcdef
serial=20261025
kernel=$kernel
EOF
cp "$RUNNER_TEMP/bundle/provenance" "$o/provenance"
if [ "$case" = missing-test ]; then
  grep -v -- '--- PASS: TestReconcileAgainstDesiredSet ' "$o/endpoint.1.log" >"$o/x" && mv "$o/x" "$o/endpoint.1.log"
  grep -v -- '--- PASS: TestReconcileAgainstDesiredSet ' "$o/endpoint.2.log" >"$o/x" && mv "$o/x" "$o/endpoint.2.log"
  grep -v -- '--- PASS: TestReconcileAgainstDesiredSet ' "$o/endpoint.0.log" >"$o/x" && mv "$o/x" "$o/endpoint.0.log"
fi
echo 0 >"$o/rc"
echo "$RUNNER_TEMP/bundle" >"$work/bundle"
date +%s.%N >"$work/boot.t0"
sleep 30 &
echo $! >"$work/qemu.pid"
: >"$work/console.log"
cd "$repo" && bash ${TRACE:+-x} ci/real-zfs-vm.sh wait 2.4
echo "exit: $?"
