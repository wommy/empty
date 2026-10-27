#!/usr/bin/env bash
# Durable checkpoint (owner decision 2026-10-27: state branch + WIP refs).
# 1. Orchestration state from the scratchpad (text at depth <= 3 outside repo
#    copies, module caches and tool dumps, plus checkpoint.include) ->
#    branch claude/gallant-johnson-1qaxp5-state, one commit per checkpoint.
# 2. Every local builder branch with commits not on the PR branch ->
#    claude/gallant-johnson-1qaxp5-wip/<branch> (forced: WIP refs move).
# usage: checkpoint.sh [-n] "<message>"     (-n: list what would be saved)
# resume: git fetch origin claude/gallant-johnson-1qaxp5-state &&
#           mkdir -p <scratchpad> && git archive FETCH_HEAD | tar -x -C <scratchpad>
#         git fetch origin 'refs/heads/claude/gallant-johnson-1qaxp5-wip/*:refs/remotes/origin/wip/*'
#         then read RESUME at the top of CHECKPOINT.md
set -euo pipefail
dry=; [ "${1:-}" = -n ] && { dry=1; shift; }
msg=${1:-checkpoint}
repo=/home/user/empty
pr=claude/gallant-johnson-1qaxp5
state=$pr-state
scratch=$(cd "$(dirname "$0")" && pwd)

files() {
  cd "$scratch"
  find . -maxdepth 3 \
    -type d \( -name 'gocache*' -o -name 'gomod*' -o -name tc -o -name venv -o -name deb -o -name .git \
      -o -name dl -o -name bin -o -name vmbin -o -name 'wt-*' -o -name repos -o -name '*copy*' \) -prune -o \
    -type d -exec test -e '{}/go.mod' \; -prune -o \
    -type f -size -2M \( -name '*.md' -o -name '*.json' -o -name '*.jsonl' -o -name '*.txt' -o -name '*.py' \
      -o -name '*.sh' -o -name '*.diff' -o -name '*.patch' -o -name '*.tsv' -o -name '*.csv' -o -name '*.yml' \
      -o -name '*.yaml' -o -name '*.golden' -o -name '*.include' \) -print
  shopt -s globstar nullglob
  local g f
  while read -r g; do
    case $g in '' | '#'*) continue ;; esac
    for f in $g; do [ -f "$f" ] && [ "$(stat -c %s "$f")" -lt 2097152 ] && echo "./$f"; done
  done <checkpoint.include
}
list=$(files | sed 's|^\./||' | sort -u)
if [ -n "$dry" ]; then
  echo "$list"; echo "$(echo "$list" | wc -l) files, $(cd "$scratch" && echo "$list" | tr '\n' '\0' | du -cb --files0-from=- | tail -1 | cut -f1) bytes"
  exit 0
fi

cd "$repo"
idx=$(mktemp -u); trap 'rm -f "$idx"' EXIT
echo "$list" | GIT_INDEX_FILE=$idx git --work-tree="$scratch" add -f --pathspec-from-file=-
tree=$(GIT_INDEX_FILE=$idx git write-tree)
parent=$(git ls-remote origin "refs/heads/$state" | cut -f1)
[ -z "$parent" ] || git fetch -q origin "$state"
if [ -n "$parent" ] && [ "$(git rev-parse "$parent^{tree}")" = "$tree" ]; then
  echo "state unchanged ($parent)"
else
  c=$(git commit-tree "$tree" ${parent:+-p "$parent"} -m "checkpoint: $msg (PR head $(git rev-parse --short "$pr"))")
  git push -q origin "$c:refs/heads/$state"
  echo "state -> $state $(git rev-parse --short "$c") ($(echo "$list" | wc -l) files)"
fi

for b in $(git for-each-ref --format='%(refname:short)' refs/heads/ | grep -vE '^(claude/|worktree-agent-)'); do
  n=$(git rev-list --count "$pr..$b")
  [ "$n" -gt 0 ] || continue
  git push -q -f origin "$b:refs/heads/$pr-wip/$b" && echo "wip $b (+$n) -> $pr-wip/$b"
done
