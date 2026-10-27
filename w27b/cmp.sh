#!/usr/bin/env bash
set -eo pipefail
W=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad
sort "$W/w27b/endpoint.list" >"$W/w27b/a"
sort "$W/wave27/endpoint.list" >"$W/w27b/b"
diff "$W/w27b/a" "$W/w27b/b" && echo same-set
wc -l "$W/w27b/endpoint.list"
