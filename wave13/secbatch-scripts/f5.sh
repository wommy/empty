#!/bin/bash
# F5 fail-before (check disabled) / pass-after evidence
set -u
W=/home/user/empty/.claude/worktrees/agent-a1ef6cfe663da83d2
E=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave13/secbatch
cd "$W"
cp cmd/zfs-endpoint/main.go "$E/main.go.keep"
python3 - <<'PY'
p='cmd/zfs-endpoint/main.go'
s=open(p).read()
s=s.replace('\tif err := checkExposure(ctx, z, *parent, etab); err != nil {','\tif err := checkExposure(ctx, z, *parent, etab); false && err != nil {',1)
open(p,'w').write(s)
PY
go test -count=1 -run 'TestStartupRefusesACrossmntAncestor' ./cmd/zfs-endpoint/ > "$E/11-before.txt" 2>&1
cp "$E/main.go.keep" cmd/zfs-endpoint/main.go
cat "$E/11-before.txt"
go test -count=1 -v -run 'TestStartupRefusesACrossmntAncestor|TestExposedByAncestor' ./cmd/zfs-endpoint/ ./internal/endpoint/ > "$E/11-after.txt" 2>&1
tail -3 "$E/11-after.txt"
