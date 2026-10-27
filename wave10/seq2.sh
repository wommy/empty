#!/usr/bin/env bash
S=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave10
for i in 1 2 3; do bash $S/run2.sh plain-w$i $S/gocache-cold-plain-cold; done
for i in 1 2; do bash $S/run2.sh race-w$i $S/gocache-cold-race-cold -race; done
echo done >> $S/walls2.txt
