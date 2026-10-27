#!/usr/bin/env bash
# the guest command from ci/real-zfs-vm.sh, with sudo stubbed: modprobe exits $1
src=/home/user/empty/.claude/worktrees/agent-a27f596bb840a667d/ci/real-zfs-vm.sh
cmd=$(awk '/^vm .s=\$\(date/,/; exit \$r.$/' "$src" | sed "1s/^vm '//; \$s/'\$//")
printf '%s\n' "$cmd" | cat -A
for rc in 0 1; do
  bash -c "sudo() { echo \"sudo \$*\"; [ \"\$1\" != modprobe ] || { sleep 0.2; return $rc; }; }; $cmd"
  echo "-> exit $?"
done
