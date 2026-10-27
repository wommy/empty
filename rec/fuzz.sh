#!/bin/bash
cd /home/user/empty/.claude/worktrees/agent-ad45c3ab7d0b822f7 || exit 1
export GOTOOLCHAIN=go1.27.3
for f in FuzzParseColumns FuzzClassify; do
  echo "== $f"; go test ./internal/zfs/ -run '^$' -fuzz "^$f\$" -fuzztime 30s 2>&1 | tail -3
done
for f in FuzzCreateConverges FuzzReconcileSafe; do
  echo "== $f"; go test ./internal/endpoint/ -run '^$' -fuzz "^$f\$" -fuzztime 30s 2>&1 | tail -3
done
go run github.com/bufbuild/buf/cmd/buf@v1.47.2 generate && git diff --exit-code gen/ && echo GEN CLEAN
git status --short
