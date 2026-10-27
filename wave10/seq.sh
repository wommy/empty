#!/usr/bin/env bash
S=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave10
GOCACHE=$S/gocache-cold-plain-cold bash $S/run1.sh plain-warm
COLD=1 bash $S/run1.sh race-cold -race
GOCACHE=$S/gocache-cold-race-cold bash $S/run1.sh race-warm -race
echo done
