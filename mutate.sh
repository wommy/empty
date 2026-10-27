#!/bin/bash
# usage: mutate.sh name file line perl-subst
D=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/mut
f=$D/internal/endpoint/$2
cp "$f" "$f.orig"
perl -i -pe "if (\$. == $3) { $4 }" "$f"
if cmp -s "$f" "$f.orig"; then echo "$1: NO CHANGE"; mv "$f.orig" "$f"; exit; fi
out=$(cd $D && go test ./internal/endpoint/ -count=1 2>&1)
if echo "$out" | grep -q "^ok"; then echo "$1: SURVIVED"; else echo "$1: killed by $(echo "$out" | grep -o -- '--- FAIL: [^ ]*' | head -3 | tr '\n' ' ')$(echo "$out" | grep -m1 'build failed\|cannot\|undefined')"; fi
mv "$f.orig" "$f"
