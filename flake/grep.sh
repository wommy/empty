#!/usr/bin/env bash
cd /home/user/empty/.claude/worktrees/agent-a5e3d463acd0910f8
echo "== Settle"; git grep -n "Settle" 98aef7a -- . ':!*.md' | head -40
echo "== creation/createtxg"; git grep -n -i "creation\|createtxg" 98aef7a -- internal/endpoint/*.go internal/zfs/*.go | grep -v _test | head -30
echo "== sharenfs/mount"; git grep -n -i "sharenfs\|func (c \*Client) Mount\|canmount\|mountpoint" 98aef7a -- internal/endpoint internal/zfs | grep -v zfstest | head -30
echo "== ErrBusy"; git grep -n "ErrBusy\|busy" 98aef7a -- internal/zfs/*.go | head -20
echo "== real-pool tests in endpoint"; git grep -n "ZFS_TEST_POOL" 98aef7a -- internal/endpoint | head -20
echo "== check-skips"; git grep -n "TestModel\|MODEL_" 98aef7a -- ci .github | head -20
