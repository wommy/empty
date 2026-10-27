#!/usr/bin/env bash
S=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave10
W=/home/user/empty/.claude/worktrees/agent-a3777e1ab243b4e78
export GOTOOLCHAIN=go1.27.3 GOCACHE=$S/gocache-cold-race-cold
cd "$W"
for i in 1 2; do
  echo "default:" >> $S/atexit.txt
  go test -race -count=1 ./internal/lock ./internal/unixsock ./cmd/zfs-csi 2>&1 | tee -a $S/atexit.txt
  echo "GORACE=atexit_sleep_ms=0:" >> $S/atexit.txt
  GORACE=atexit_sleep_ms=0 go test -race -count=1 ./internal/lock ./internal/unixsock ./cmd/zfs-csi 2>&1 | tee -a $S/atexit.txt
done
