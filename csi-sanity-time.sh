#!/usr/bin/env bash
# time ci.yml's NFS csi-sanity step, warm, 3 times; run from the worktree root
export GORACE=atexit_sleep_ms=0 CSI_SANITY_NFS=1
for i in 1 2 3; do
  s=$(date +%s.%N)
  go test -race -count=1 -ldflags "-X github.com/wommy/empty/internal/version.Version=test-1" -run '^TestSanity$' -v ./internal/csi >"/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/nfs-step.$i.log" 2>&1
  rc=$?
  e=$(date +%s.%N)
  echo "run $i rc=$rc wall $(echo "$e - $s" | bc)s; $(grep -E 'sanity_test.go|^ok' "/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/nfs-step.$i.log" | tr '\n' ' ')"
done
