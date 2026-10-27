#!/usr/bin/env bash
# Mutant proof for the de-vacuumed ownership tests: each mutant removes one
# ownership guard; the HEAD test file must pass (vacuous), the fixed one fail.
set -u
W=/home/user/empty/.claude/worktrees/agent-a5e3d463acd0910f8
S=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave14ms
cd "$W"
cp internal/endpoint/ownership_test.go "$S/own.new"
git show HEAD:internal/endpoint/ownership_test.go >"$S/own.old"
T='^(TestCloneSourcesMustBeOwned|TestVolumeNameMustMatchID|TestReceivedCopyIsNotOwned|TestReconcileSparesReceivedAndRenamed)$'
run() { # file mutant-name
  cp "$S/own.$1" internal/endpoint/ownership_test.go
  go test -count=1 -run "$T" ./internal/endpoint/ 2>&1 | grep -E '^(--- FAIL|ok|FAIL)' | sed "s/^/  $2 $1: /"
}
mut() { # name file old new
  cp "$2" "$S/orig.go"
  python3 - "$2" "$3" "$4" <<'EOF'
import sys
p,a,b=sys.argv[1:]
s=open(p).read(); assert s.count(a)==1,(p,a); open(p,'w').write(s.replace(a,b))
EOF
  run old "$1"; run new "$1"
  cp "$S/orig.go" "$2"
}
mut M1-name-guard internal/endpoint/table.go 'r.Managed && r.VolumeName == r.ID' 'r.Managed'
mut M2-clone-src-committed internal/endpoint/plan.go '		if !ok || !src.ServedVolume() {
			return nil, errorf(NotFound, "source volume %s not found", srcID)' '		if !ok || !src.OwnedVolume() {
			return nil, errorf(NotFound, "source volume %s not found", srcID)'
mut M3-received-is-local internal/endpoint/table.go 'if p := cell(k); p.Local() {' 'if p := cell(k); p.Local() || p.Source == "received" {'
cp "$S/own.new" internal/endpoint/ownership_test.go
git status --short
