#!/usr/bin/env bash
# stub-analyze.sh <repo>: runs the remote command of real-zfs-vm.sh's
# background boot report through sh, as ssh would, with stub
# systemctl/systemd-analyze on PATH (a 2000-line blame: awk must read it
# all, so the producer never sees a closed pipe), and a stub ssh as vm()
set -euo pipefail
repo=$1
t=$(mktemp -d)
trap 'rm -rf "$t"' EXIT
mkdir "$t/bin"
cat >"$t/bin/systemctl" <<'EOF'
#!/bin/sh
exit 1
EOF
cat >"$t/bin/systemd-analyze" <<'EOF'
#!/bin/sh
case $1 in
  blame) seq 1 2000 | sed 's/$/ms x.service/' ;;
  critical-chain) echo "ssh.service +12ms"; echo " └─network.target" ;;
  *) echo "Startup finished in 1s (kernel) + 2s (initrd) + 3s (userspace) = 6s" ;;
esac
EOF
cat >"$t/bin/findmnt" <<'EOF'
#!/bin/sh
printf '/\n/boot\n/run\n'
EOF
chmod +x "$t/bin"/*
# the vm '...' argument, from "vm 'timeout" to the closing "' \"
awk '/^vm .timeout 60 systemctl is-system-running/ { p = 1 } p { print } p && /\x27 \\$/ { exit }' \
  "$repo/ci/real-zfs-vm.sh" >"$t/cmd.sh"
# turn the vm call into: PATH=stubs sh -c '<arg>'
sed -i "1s/^vm /PATH=$(printf '%s' "$t/bin" | sed 's/[\/&]/\\&/g'):\$PATH sh -c /; \$s/ \\\\\$//" "$t/cmd.sh"
set +e
out=$(bash "$t/cmd.sh" 2>&1)
rc=$?
set -e
echo "$out"
[ "$(grep -c 'x.service' <<<"$out")" = 15 ] || { echo "FAIL: blame not cut to 15 lines"; exit 1; }
grep -q '^ssh.service' <<<"$out" || { echo "FAIL: no critical chain"; exit 1; }
grep -q '^mounted under /boot: /boot $' <<<"$out" || { echo "FAIL: findmnt line"; exit 1; }
echo "PASS: stub-analyze (rc $rc)"
