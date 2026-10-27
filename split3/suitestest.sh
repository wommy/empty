#!/usr/bin/env bash
# check-suites.sh against broken part matrices
set -uo pipefail
R=/home/user/empty/.claude/worktrees/agent-acf7494638986ab8a
C=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/split3/suites
try() { # try <label> <sed expr on ci.yml>
  rm -rf "${C:?}" && mkdir -p "$C"
  cp -r "$R/ci" "$R/.github" "$C/"
  mkdir -p "$C/internal" && cp -r "$R/internal/endpoint" "$R/internal/csi" "$C/internal/"
  sed -i "$2" "$C/.github/workflows/ci.yml"
  if out=$(bash "$C/ci/check-suites.sh" 2>&1); then echo "NOT REJECTED: $1"; else echo "rejected: $1"; fi
  printf '%s\n' "$out" | awk '/::error::/ { print "    " $0 }'
}
try "endpoint part 1 dropped" '/{ suite: endpoint, part: 1, parts: 2 }/d'
try "endpoint part 0 twice" 's/{ suite: endpoint, part: 1, parts: 2 }/{ suite: endpoint, part: 0, parts: 2 }/'
try "endpoint parts disagree" 's/{ suite: endpoint, part: 1, parts: 2 }/{ suite: endpoint, part: 1, parts: 3 }/'
try "csi split in two" 's/{ suite: csi, part: 0, parts: 1 }/{ suite: csi, part: 0, parts: 2 }\n          - { suite: csi, part: 1, parts: 2 }/'
try "csi dropped" '/{ suite: csi, part: 0, parts: 1 }/d'
try "part out of range" 's/{ suite: endpoint, part: 1, parts: 2 }/{ suite: endpoint, part: 2, parts: 2 }/'
