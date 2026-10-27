#!/bin/bash
# S12 before/after. The ported test constructs Identity{Health: ...}, which
# c1205e9 does not have; BEFORE runs the same test body with the c1205e9
# construction Identity{EP: ...} against c1205e9's identity.go.
set -u
W=/home/user/empty/.claude/worktrees/agent-a131fcf70b9f3c4be
EV=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave13/fixes-csi-ev
cd "$W" || exit 1
save=$(mktemp -d "$EV/s12.XXXX")
cp internal/csi/identity.go internal/csi/sanity_test.go internal/csi/identity_test.go "$save/"
sed -e 's/id := &zcsi.Identity{Health: healthpb.NewHealthClient(conn)}/id := \&zcsi.Identity{EP: pb.NewEndpointServiceClient(conn)}/' \
    -e 's#healthpb "google.golang.org/grpc/health/grpc_health_v1"#pb "github.com/wommy/empty/gen/zfsendpoint/v1"#' \
    "$save/identity_test.go" > internal/csi/identity_test.go
git show c1205e9:internal/csi/identity.go > internal/csi/identity.go
git show c1205e9:internal/csi/sanity_test.go > internal/csi/sanity_test.go
{
echo "== S12 BEFORE (c1205e9 identity.go; test body with Identity{EP: ...})"
grep -n 'Identity{' internal/csi/identity_test.go
go test ./internal/csi -run TestProbeFollowsEndpointHealth -count=1 2>&1 | grep -v '^\s*$' | head -20
cp "$save"/identity.go "$save"/sanity_test.go "$save"/identity_test.go internal/csi/
echo "== S12 AFTER"
go test ./internal/csi -run TestProbeFollowsEndpointHealth -count=1 -v 2>&1 | grep -E '^(--- |ok|FAIL|PASS)'
} | tee "$EV/S12.log"
rm -rf "$save"
