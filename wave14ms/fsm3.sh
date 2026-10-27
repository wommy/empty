#!/usr/bin/env bash
cd /home/user/empty/.claude/worktrees/agent-a5e3d463acd0910f8
O=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave14ms
D=${1:-3}
for k in fs block; do
  for p in zfs-endpoint democratic-csi; do
    echo "== $k $p depth $D"
    FSM_KIND=$k FSM_DEPTH=$D FSM_PREFIX=$p FSM_OUT=$O/fsm-d$D-$k-$p.txt go test -tags nightly -count=1 -run TestFSMExplore -v -timeout 30m ./internal/endpoint/ 2>&1 | grep -E 'depth|findings|PASS|FAIL|panic'
  done
done
