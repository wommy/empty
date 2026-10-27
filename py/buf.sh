#!/bin/bash
set -e
export PATH=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/gobin:$PATH
cd /home/user/empty/.claude/worktrees/agent-a1abcb64b96d0f34f
buf "$@"
