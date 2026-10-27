#!/usr/bin/env bash
# The nightly explore step and its guard, locally: with the tag the guard
# passes, without it the same command runs nothing and the guard fails.
set -o pipefail
cd /home/user/empty/.claude/worktrees/agent-a5e3d463acd0910f8
O=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave14ms
start=$(date +%s)
go test -tags nightly -count=1 -timeout 25m -run '^Test(Scale|Explore)' -v ./internal/endpoint >"$O/explore-tag.log"
echo "with tag: go test exit $? in $(( $(date +%s) - start ))s"
ci/check-explore-ran.sh "$O/explore-tag.log"; echo "guard exit $?"
go test -count=1 -run '^Test(Scale|Explore)' -v ./internal/endpoint >"$O/explore-notag.log"
echo "without tag: go test exit $?"; tail -2 "$O/explore-notag.log"
ci/check-explore-ran.sh "$O/explore-notag.log"; echo "guard exit $?"
