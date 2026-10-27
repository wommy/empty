#!/bin/bash
# usage: mktrees.sh <commit> <dir>...   export <commit> of the worktree into each fresh dir
set -eu
repo=/home/user/empty/.claude/worktrees/agent-a0f85a155c91152de
commit=$1; shift
for d in "$@"; do
  rm -rf "$d"
  mkdir -p "$d"
  git -C "$repo" archive "$commit" | tar -x -C "$d"
done
