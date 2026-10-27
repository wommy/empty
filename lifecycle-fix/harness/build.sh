#!/bin/bash
# build the endpoint from the worktree and the client from the wave6 harness source
set -e
H=$(dirname "$(readlink -f "$0")")
W=/home/user/empty/.claude/worktrees/agent-aad3f1ab5f9072c5f
chmod +x "$H/bin/zfs" "$H/run-shutdown.sh"
cd "$W"
GOTOOLCHAIN=go1.27.3 go build -o "$H/zfs-endpoint" ./cmd/zfs-endpoint
mkdir -p "$W/.harness-client-tmp"
cp /tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave6/harness/client/main.go "$W/.harness-client-tmp/main.go"
GOTOOLCHAIN=go1.27.3 go build -o "$H/lifeclient" ./.harness-client-tmp
rm -rf "$W/.harness-client-tmp"
echo built
