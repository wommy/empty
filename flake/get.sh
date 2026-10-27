#!/usr/bin/env bash
D=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/flake
cd /home/user/empty/.claude/worktrees/agent-a5e3d463acd0910f8
for f in internal/endpoint/model_test.go internal/endpoint/modelrun_test.go internal/endpoint/apply.go internal/endpoint/realfacts_test.go internal/endpoint/reconcile.go internal/endpoint/plan.go internal/endpoint/table.go ci/check-skips.sh; do
  git show 98aef7a:$f > $D/$(basename $f)
done
git diff --stat 91d1f8f 98aef7a -- internal/endpoint internal/zfs > $D/diffstat.txt
git diff 91d1f8f 98aef7a -- internal/endpoint/model_test.go internal/endpoint/modelrun_test.go > $D/modeldiff.txt
git ls-tree --name-only 98aef7a internal/endpoint/ > $D/files.txt
wc -l $D/*
