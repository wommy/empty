#!/usr/bin/env bash
# PR-tier cost of the model suite under -race (the unit job's command).
cd /home/user/empty/.claude/worktrees/agent-a5e3d463acd0910f8
uptime
s=$(date +%s.%N)
go test -race -count=1 ./internal/endpoint/ 2>&1 | tail -1
echo "endpoint package with model, -race: $(echo "$(date +%s.%N) - $s" | bc)s wall"
s=$(date +%s.%N)
go test -race -count=1 -skip 'TestModel$|TestRealFacts|FuzzModel' ./internal/endpoint/ 2>&1 | tail -1
echo "endpoint package without model, -race: $(echo "$(date +%s.%N) - $s" | bc)s wall"
for n in 100 200 400; do
  MODEL_SEQS=$n go test -race -count=1 -v -run 'TestModel$' ./internal/endpoint/ 2>&1 | grep -E 'sequences x|^(ok|FAIL)'
done
