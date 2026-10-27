#!/usr/bin/env bash
# usage: run2.sh <label> <gocache> <extra go test flags...>  -- warm-cache run recording wall, user+sys CPU and load average
S=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave10
W=/home/user/empty/.claude/worktrees/agent-a3777e1ab243b4e78
export GOTOOLCHAIN=go1.27.3
label=$1; export GOCACHE=$2; shift 2
cd "$W"
la0=$(cut -d' ' -f1 /proc/loadavg)
TIMEFORMAT="wall=%R user=%U sys=%S"
tm=$( { time go test -json -count=1 "$@" ./... > "$S/$label.json" 2> "$S/$label.err"; } 2>&1 )
la1=$(cut -d' ' -f1 /proc/loadavg)
echo "$label $tm load_before=$la0 load_after=$la1 nproc=$(nproc)" | tee -a "$S/walls2.txt"
