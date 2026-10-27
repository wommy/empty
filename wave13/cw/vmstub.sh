#!/usr/bin/env bash
# Exercise ci/real-zfs-vm.sh's boot/run split, direct kernel boot and checks
# with stubbed qemu, ssh and gpgv (no KVM here). ZFSVM_PROVISION_ONLY=1, so
# it stops after the checks. Arg: "direct" (cache has a kernel) or "bios",
# optional "badkernel" (guest reports another release).
set -uo pipefail
mode=${1:-direct} bad=${2:-}
cw=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave13/cw
repo=/home/user/empty/.claude/worktrees/agent-a53361ecab5fd8058
t=$cw/vmstub
rm -rf "$t"; mkdir -p "$t/bin" "$t/runner" "$t/cache" "$t/sums"
sum=46d1d848cd8bfc05a2e729f70b02e0b4b97502bf7839b8928186d3c643c13b33
printf '%s *stonking-server-cloudimg-amd64.img\n' "$sum" >"$t/sums/SHA256SUMS"
echo sig >"$t/sums/SHA256SUMS.gpg"
c=$t/cache
echo "$sum" >"$c/sum"; touch "$c/ok" "$c/disk.qcow2" "$c/seed.img"; echo k >"$c/key"; echo pub >"$c/key.pub"
if [ "$mode" = direct ]; then
  echo vmlinuz >"$c/vmlinuz"; echo initrd >"$c/initrd"
  echo "root=LABEL=cloudimg-rootfs ro console=tty1 console=ttyS0" >"$c/cmdline"
  echo 7.3.0-9-generic >"$c/kernel.release"
fi
cat >"$t/bin/gpgv" <<'EOF'
#!/bin/sh
echo "gpgv stub: $*"
EOF
cat >"$t/bin/qemu-img" <<'EOF'
#!/bin/sh
echo "qemu-img stub: $*"
EOF
cat >"$t/bin/qemu-system-x86_64" <<EOF
#!/bin/bash
printf '%q ' "\$@" >"$t/qemu.args"; echo >>"$t/qemu.args"
for ((i=1;i<=\$#;i++)); do [ "\${!i}" = -pidfile ] && { j=\$((i+1)); setsid sleep 120 >/dev/null 2>&1 </dev/null & echo \$! >"\${!j}"; }; done
exit 0
EOF
rel=7.3.0-9-generic; [ -z "$bad" ] || rel=7.3.0-10-generic
cat >"$t/bin/ssh" <<EOF
#!/bin/bash
cmd=\${@: -1}
case \$cmd in
  *"-O"*|exit) exit 0 ;;
esac
for a in "\$@"; do [ "\$a" = -O ] && exit 0; done
case \$cmd in
  true|*boot-finished*|*nfs-server*|*modprobe*) exit 0 ;;
  *"cloud-init status"*) echo "status: disabled"; echo "serial: 20261025"; echo $rel ;;
  *"zfs version"*) printf 'zfs-2.4.4-1ubuntu4\nzfs-kmod-2.4.4-1ubuntu4\n' ;;
  *build.info*) echo 20261025 ;;
  "uname -r") echo $rel ;;
  *exportfs*) echo /usr/sbin/exportfs ;;
  *"ls -ld"*) echo ok ;;
  *threads*) echo "nfsd threads: 16" ;;
  *) echo "ssh stub: unknown command: \$cmd" >&2; exit 1 ;;
esac
EOF
chmod +x "$t/bin/"*
export PATH=$t/bin:$PATH RUNNER_TEMP=$t/runner GITHUB_STEP_SUMMARY=$t/summary.md ZFSVM_CACHE=$c ZFSVM_SUMS=$t/sums ZFSVM_PROVISION_ONLY=1
cd "$repo"
echo "== boot phase"; ZFSVM_PHASE=boot ci/real-zfs-vm.sh stonking current 2.4; echo "boot rc=$?"
echo "qemu args: $(cat "$t/qemu.args")"
echo "state:"; cat "$t/runner/zfsvm/state"
echo "== run phase"; ZFSVM_PHASE=run ci/real-zfs-vm.sh stonking current 2.4; echo "run rc=$?"
echo "summary:"; cat "$t/summary.md"
pkill -f "sleep 120" 2>/dev/null || true
