#!/usr/bin/env bash
# the analyze block's guest command: syntax, then the new lines run with systemctl stubbed
src=/home/user/empty/.claude/worktrees/agent-a27f596bb840a667d/ci/real-zfs-vm.sh
cmd=$(awk '/^vm .timeout 60 systemctl/,/^  >"\$work\/systemd-analyze.txt"/' "$src" | sed "1s/^vm '//" | awk '!/systemd-analyze.txt/' | sed "\$s/' \\\\\$//")
printf '%s\n' "$cmd" | tail -n 3
bash -n -c "$cmd" && echo "analyze command: syntax ok"
new=$(printf '%s\n' "$cmd" | awk '/zfs-zed; do|modules-load.d \(/')
d=$(mktemp -d)
printf 'zfs\n# comment\n\n' >"$d/zfs.conf"
new=${new//\/etc\/modules-load.d/$d}
bash -c "systemctl() { case \$1 in is-active) echo inactive;; is-enabled) echo masked-runtime;; esac; }; $new"
rm -rf "${d:?}"
