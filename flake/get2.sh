#!/usr/bin/env bash
cd /home/user/empty/.claude/worktrees/agent-a5e3d463acd0910f8
git show 98aef7a:internal/endpoint/endpoint.go | sed -n 145,170p
echo ==== endpoint_test
git show 98aef7a:internal/endpoint/endpoint_test.go | sed -n 85,145p
echo ==== settle.go
git show 98aef7a:internal/endpoint/settle.go
echo ==== reconcile tooYoung
git show 98aef7a:internal/endpoint/reconcile.go | sed -n 375,410p
echo ==== mount in plan
git show 98aef7a:internal/endpoint/plan.go | grep -n "OpMount\|Mount" | head
echo ==== zfs Mount
git grep -n -A12 "func (c \*Client) Mount" 98aef7a -- internal/zfs
echo ==== check-skips stream
sed -n 1,83p /tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/flake/check-skips.sh | grep -n "stream\|-v\|json\|Output" | head
echo ==== genAdmin
sed -n 210,370p /tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/flake/modelrun_test.go
