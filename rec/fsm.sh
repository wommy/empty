#!/bin/bash
cd /tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/rec/fsmrun || exit 1
export GOTOOLCHAIN=go1.27.3
for k in fs block; do
  for p in zfs-endpoint democratic-csi; do
    FSM_DEPTH=4 FSM_KIND=$k FSM_PREFIX=$p FSM_OUT=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/rec/fsm-$k-$p.txt \
      go test ./internal/endpoint/ -run TestFSMExplore -v -timeout 20m 2>&1 | grep -E "findings|^ok|FAIL"
  done
done
