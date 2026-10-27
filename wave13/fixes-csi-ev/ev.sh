#!/bin/bash
# usage: ev.sh <label> <pkg> <test-regex> <non-test file>...
# Runs the test with the listed files as of c1205e9 (BEFORE), then as in
# the working tree (AFTER). Restores the files afterwards.
set -u
W=/home/user/empty/.claude/worktrees/agent-a131fcf70b9f3c4be
EV=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave13/fixes-csi-ev
label=$1 pkg=$2 re=$3; shift 3
cd "$W" || exit 1
tmp=$(mktemp -d "$EV/save.XXXX")
for f in "$@"; do mkdir -p "$tmp/$(dirname "$f")"; cp "$f" "$tmp/$f"; git show "c1205e9:$f" > "$f"; done
{
echo "== $label BEFORE (c1205e9 sources: $*)"
go test "$pkg" -run "$re" -count=1 2>&1 | grep -v '^\s*$' | grep -v ' INFO rpc ' | head -40
for f in "$@"; do cp "$tmp/$f" "$f"; done
echo "== $label AFTER"
go test "$pkg" -run "$re" -count=1 -v 2>&1 | grep -E '^(=== RUN|--- |ok|FAIL|PASS)' | grep -v '=== RUN' | head -40
} | tee "$EV/$label.log"
rm -rf "$tmp"
