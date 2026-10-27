#!/usr/bin/env bash
S=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave10
W=/home/user/empty/.claude/worktrees/agent-a3777e1ab243b4e78
export GOTOOLCHAIN=go1.27.3
export GOCACHE=$S/gocache-buf GOMODCACHE=$S/gomod-buf GOBIN=$S/bufbin
rm -rf "$GOCACHE" "$GOMODCACHE" "$GOBIN"; mkdir -p "$GOBIN"
cd "$W"
TIMEFORMAT="wall=%R user=%U sys=%S"
echo "load $(cut -d' ' -f1 /proc/loadavg)" >> $S/buf.txt
echo -n "1. cold: go run buf lint (modules fetched + buf compiled + linked + lint): " >> $S/buf.txt
{ time go run github.com/bufbuild/buf/cmd/buf@v1.47.2 lint ; } 2>> $S/buf.txt
echo -n "2. warm: go run buf lint again (all cached): " >> $S/buf.txt
{ time go run github.com/bufbuild/buf/cmd/buf@v1.47.2 lint ; } 2>> $S/buf.txt
echo -n "3. go install buf (cache warm; link-only): " >> $S/buf.txt
{ time go install github.com/bufbuild/buf/cmd/buf@v1.47.2 ; } 2>> $S/buf.txt
echo -n "4. prebuilt binary: buf lint: " >> $S/buf.txt
{ time $GOBIN/buf lint ; } 2>> $S/buf.txt
echo -n "5. modules-only download time (cold modcache, go mod download style via go list -m): " >> $S/buf.txt
export GOMODCACHE=$S/gomod-buf2
rm -rf "$GOMODCACHE"
{ time go list -deps github.com/bufbuild/buf/cmd/buf@v1.47.2 >/dev/null 2>&1 ; } 2>> $S/buf.txt
echo done >> $S/buf.txt
