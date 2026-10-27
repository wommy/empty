#!/bin/bash
# systemd-analyze verify the shipped unit, with ExecStart pointed at a built binary
set -u
W=/home/user/empty/.claude/worktrees/agent-aad3f1ab5f9072c5f
D=$(dirname "$(readlink -f "$0")")/unit
rm -rf "$D"; mkdir -p "$D/sbin"
(cd "$W" && GOTOOLCHAIN=go1.27.3 go build -o "$D/sbin/zfs-endpoint" ./cmd/zfs-endpoint) || exit 1
sed "s#/usr/local/sbin/zfs-endpoint#$D/sbin/zfs-endpoint#" "$W/deploy/systemd/zfs-endpoint@.service" > "$D/zfs-endpoint@.service"
systemctl --version | head -1
echo "--- verify template instance"
cd "$D" && systemd-analyze verify "$D/zfs-endpoint@tank-k8s.service"
echo "rc=$?"
echo "--- negative control: a bad KillMode must be reported"
mkdir -p "$D/neg"
cp "$D/zfs-endpoint@.service" "$D/neg/"
echo "KillMode=bogus" >> "$D/neg/zfs-endpoint@.service"
cd "$D/neg" && systemd-analyze verify "$D/neg/zfs-endpoint@tank-k8s.service"
echo "rc=$?"
