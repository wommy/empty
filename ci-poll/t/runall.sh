#!/usr/bin/env bash
# every scenario, several times; then the old 5s banner bound for comparison
h=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/ci-poll/t/harness.sh
s=/home/user/empty/.claude/worktrees/agent-a27f596bb840a667d/ci/real-zfs-vm.sh
for m in stall close refuse; do
  for T in 3 6; do
    for i in 1 2 3; do bash "$h" "$s" "$m" "$T"; done
  done
done
echo "--- 5s banner bound (the old ConnectTimeout=5 shape)"
for T in 3 6; do bash "$h" "$s" stall "$T" 5; done
echo "--- qemu exits under a no-initrd boot: fallback"
bash "$h" "$s" stall 3 0.3 die
