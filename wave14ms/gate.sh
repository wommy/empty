#!/usr/bin/env bash
cd /home/user/empty/.claude/worktrees/agent-a5e3d463acd0910f8
echo "gofmt: [$(gofmt -l .)]"
go vet ./... && echo "vet ok"
go vet -tags nightly ./... && echo "vet nightly ok"
go mod tidy -diff && echo "tidy ok"
bash -n ci/check-explore-ran.sh && echo "bash -n ok"
/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/bin/actionlint && echo "actionlint ok"
s=$(date +%s)
go test -race -count=1 ./... 2>&1 | grep -v 'no test files'
echo "go test -race ./...: $(( $(date +%s) - s ))s"
