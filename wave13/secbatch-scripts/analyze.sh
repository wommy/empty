#!/bin/bash
D=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave13/secbatch-unit
E=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave13/secbatch
W=/home/user/empty/.claude/worktrees/agent-a1ef6cfe663da83d2
mkdir -p "$D/base" "$D/hard"
cp "$W/deploy/systemd/zfs-endpoint@.service" "$D/base/zfs-endpoint@tank-k8s.service"
cat "$W/deploy/systemd/zfs-endpoint@.service" "$W/deploy/systemd/zfs-endpoint@.service.d/hardening.conf" > "$D/hard/zfs-endpoint@tank-k8s.service"
cd "$D"
{
  echo "## unit"
  systemd-analyze security --offline=true base/zfs-endpoint@tank-k8s.service
  echo "## unit + hardening.conf"
  systemd-analyze security --offline=true hard/zfs-endpoint@tank-k8s.service
  echo "## verify (unit + drop-in)"
  systemd-analyze verify --man=no hard/zfs-endpoint@tank-k8s.service
} > "$E/15-systemd-analyze.txt" 2>&1
grep -E "Overall|not executable" "$E/15-systemd-analyze.txt"
