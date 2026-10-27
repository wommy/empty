# Wave 24: coverage tests, mutation evidence

Branch `cov-tests` from claude/gallant-johnson-1qaxp5 (26975dd). Each new or fixed test was run against a
temporarily mutated production file (the exact replacement below, applied by a script that restores the
file afterwards). Every mutation makes the named test(s) fail. Items follow wave22/coverage.md's ranking.

## Item 1: reconcile max-deletions gate (anomaly)

Before: TestReconcileMaxDeletionsAndDigest (adv4_test.go:316) sent no PlanDigest, so the digest gate
refused first and the "3 > 2" assertion held with the max-deletions check deleted. Fixed: the test now
asserts the digest-gate message for the undigested call, then sends a dry-run digest with MaxDeletions 2
and asserts "more than max deletions 2" and that a, b, c survive. New TestReconcileDefaultMaxDeletionsWithDigest
covers MaxDeletions 0 = DefaultMaxDeletions (11 refused, exactly 10 pass).

### 1: max-deletions check disabled

Mutation in `internal/endpoint/reconcile.go`:

```diff
- if limit := cmp.Or(r.MaxDeletions, DefaultMaxDeletions); !r.UnlimitedDeletions && plan.undesired() > limit {
+ if limit := cmp.Or(r.MaxDeletions, DefaultMaxDeletions); false && !r.UnlimitedDeletions && plan.undesired() > limit {
```

`go test -count=1 -run 'TestReconcileMaxDeletionsAndDigest|TestReconcileDefaultMaxDeletionsWithDigest'  ./internal/endpoint` exit 1:

```
--- FAIL: TestReconcileMaxDeletionsAndDigest (0.00s)
    adv4_test.go:324: 3 > 2: <nil>
--- FAIL: TestReconcileDefaultMaxDeletionsWithDigest (0.01s)
    adv4_test.go:366: 11 > default: <nil>
FAIL
FAIL	github.com/wommy/empty/internal/endpoint	0.033s
FAIL
```

## Item 2: admin-socket uid refusal, no peer, stream interceptor (security)

internal/server/roles_internal_test.go: TestAuthorizeRefusesAdminPeerOfAnotherUID calls `authorize` with a
PeerInfo{Role: RoleAdmin, HasCred: true, UID: euid+1} on Reconcile (PermissionDenied), uid 0 and euid
(allowed), the CSI role (PermissionDenied), no peer (Unauthenticated, on an admin and a CSI method), and
health with no peer (allowed). TestStreamInterceptorServesHealthOnly calls `stream` directly.

### 2a: authorize no longer calls adminPeer

Mutation in `internal/server/roles.go`:

```diff
- if pi.Role == RoleAdmin && !adminPeer(pi, os.Geteuid()) {
+ if false && pi.Role == RoleAdmin && !adminPeer(pi, os.Geteuid()) {
```

`go test -count=1 -run 'TestAuthorizeRefusesAdminPeerOfAnotherUID'  ./internal/server` exit 1:

```
--- FAIL: TestAuthorizeRefusesAdminPeerOfAnotherUID (0.00s)
    roles_internal_test.go:55: admin socket, uid 1 (daemon 0): <nil>
FAIL
FAIL	github.com/wommy/empty/internal/server	0.022s
FAIL
```

### 2b: no peer is allowed

Mutation in `internal/server/roles.go`:

```diff
- 		return status.Error(codes.Unauthenticated, "no peer")
+ 		return nil
```

`go test -count=1 -run 'TestAuthorizeRefusesAdminPeerOfAnotherUID'  ./internal/server` exit 1:

```
--- FAIL: TestAuthorizeRefusesAdminPeerOfAnotherUID (0.00s)
    roles_internal_test.go:69: no peer, /zfsendpoint.v1.EndpointService/Reconcile: <nil>
FAIL
FAIL	github.com/wommy/empty/internal/server	0.013s
FAIL
```

### 2c: stream interceptor serves every stream

Mutation in `internal/server/roles.go`:

```diff
- 	if !isHealth(info.FullMethod) {
- 		return status.Errorf(codes.PermissionDenied, "%s: streams have no role", info.FullMethod)
+ 	if false && !isHealth(info.FullMethod) {
+ 		return status.Errorf(codes.PermissionDenied, "%s: streams have no role", info.FullMethod)
```

`go test -count=1 -run 'TestStreamInterceptorServesHealthOnly'  ./internal/server` exit 1:

```
--- FAIL: TestStreamInterceptorServesHealthOnly (0.00s)
    roles_internal_test.go:97: /zfsendpoint.v1.EndpointService/Watch: called true, <nil>, want PermissionDenied and no call
    roles_internal_test.go:97: /zfsendpoint.v1.EndpointService/Reconcile: called true, <nil>, want PermissionDenied and no call
FAIL
FAIL	github.com/wommy/empty/internal/server	0.010s
FAIL
```

### 2d: stream interceptor denies health too (inverted)

Mutation in `internal/server/roles.go`:

```diff
- 	if !isHealth(info.FullMethod) {
- 		return status.Errorf(codes.PermissionDenied, "%s: streams have no role", info.FullMethod)
+ 	if true {
+ 		return status.Errorf(codes.PermissionDenied, "%s: streams have no role", info.FullMethod)
```

`go test -count=1 -run 'TestStreamInterceptorServesHealthOnly'  ./internal/server` exit 1:

```
--- FAIL: TestStreamInterceptorServesHealthOnly (0.00s)
    roles_internal_test.go:94: /grpc.health.v1.Health/Watch: called false, rpc error: code = PermissionDenied desc = /grpc.health.v1.Health/Watch: streams have no role
FAIL
FAIL	github.com/wommy/empty/internal/server	0.008s
FAIL
```

## Item 3: OWN-15 guard in the busy-then-settle window

internal/endpoint/settle_test.go TestDeleteVolumeSettleWindow (own simulator, runs on the VM binary too): the
first destroy is busy (Sim.Fail); Config.Settle renames the volume away and creates an impostor zvol with
the same markers. DeleteVolume is Aborted, the impostor and the moved original survive, one destroy argv.
Second case: Settle errors, the result is the busy (FailedPrecondition, errors.Is ErrBusy), one destroy.

### 3a: no guid re-check after the settle

Mutation in `internal/endpoint/apply.go`:

```diff
- 	if e.cfg.Settle(ctx) != nil {
- 		return err
- 	}
- 	if err := e.sameDataset(ctx, op); err != nil {
- 		return err
- 	}
+ 	if e.cfg.Settle(ctx) != nil {
+ 		return err
+ 	}
```

`go test -count=1 -run 'TestDeleteVolumeSettleWindow'  ./internal/endpoint` exit 1:

```
--- FAIL: TestDeleteVolumeSettleWindow (0.00s)
    --- FAIL: TestDeleteVolumeSettleWindow/replaced_while_settling (0.00s)
        settle_test.go:174: code = 0 (<nil>), want 10
FAIL
FAIL	github.com/wommy/empty/internal/endpoint	0.007s
FAIL
```

### 3b: a failed settle is ignored (retries anyway)

Mutation in `internal/endpoint/apply.go`:

```diff
- 	if e.cfg.Settle(ctx) != nil {
- 		return err
- 	}
+ 	_ = e.cfg.Settle(ctx)
```

`go test -count=1 -run 'TestDeleteVolumeSettleWindow'  ./internal/endpoint` exit 1:

```
--- FAIL: TestDeleteVolumeSettleWindow (0.00s)
    --- FAIL: TestDeleteVolumeSettleWindow/settle_fails (0.00s)
        settle_test.go:189: code = 0 (<nil>), want 9
FAIL
FAIL	github.com/wommy/empty/internal/endpoint	0.008s
FAIL
```

## Item 4: CreateVolume(NFS) refuses to commit a volume served beyond its export

internal/endpoint/cov24_test.go (own simulator, no skip): TestFileExportCreateServedBeyondByAncestor sets
sharenfs=on (crossmnt) on tank/k8s, then CreateVolume(NFS) is FailedPrecondition "served beyond its export",
the volume exists uncommitted, ListVolumes is empty; with the ancestor off, a retry commits.
TestFileExportCreateChangedBeforeCommit wraps the runner (afterCreate) to destroy the volume, inherit its
managed marker, or set sharenfs=off right before verifyExport's scan: all Aborted, never committed.

### 4a: verifyExport ignores exposure beyond the export

Mutation in `internal/endpoint/endpoint.go`:

```diff
- 		if x := live.exposure(realPath(row.Mount), want); len(x) > 0 && len(missing) == 0 {
+ 		if x := live.exposure(realPath(row.Mount), want); false && len(x) > 0 && len(missing) == 0 {
```

`go test -count=1 -run 'TestFileExportCreateServedBeyondByAncestor'  ./internal/endpoint` exit 1:

```
--- FAIL: TestFileExportCreateServedBeyondByAncestor (0.00s)
    cov24_test.go:85: code = 0 (<nil>), want 9
FAIL
FAIL	github.com/wommy/empty/internal/endpoint	0.006s
FAIL
```

### 4b: verifyExport does not check the row is still ours

Mutation in `internal/endpoint/endpoint.go`:

```diff
- 		if !ok || !row.OwnedVolume() {
- 			return errorf(Aborted, "volume %s vanished or changed owner during provisioning", id)
+ 		if false && (!ok || !row.OwnedVolume()) {
+ 			return errorf(Aborted, "volume %s vanished or changed owner during provisioning", id)
```

`go test -count=1 -run 'TestFileExportCreateChangedBeforeCommit'  ./internal/endpoint` exit 1:

```
--- FAIL: TestFileExportCreateChangedBeforeCommit (0.00s)
    --- FAIL: TestFileExportCreateChangedBeforeCommit/vanished (0.00s)
        cov24_test.go:142: volume pvc: sharenfs changed during provisioning (now ""); retry, want "vanished or changed owner"
    --- FAIL: TestFileExportCreateChangedBeforeCommit/owner_changed (0.00s)
        cov24_test.go:145: committed
FAIL
FAIL	github.com/wommy/empty/internal/endpoint	0.008s
FAIL
```

### 4c: verifyExport commits a volume whose sharenfs changed

Mutation in `internal/endpoint/endpoint.go`:

```diff
- 		if len(want) == 0 {
- 			// the resume set the requested export just before this scan
+ 		if false && len(want) == 0 {
+ 			// the resume set the requested export just before this scan
```

`go test -count=1 -run 'TestFileExportCreateChangedBeforeCommit'  ./internal/endpoint` exit 1:

```
--- FAIL: TestFileExportCreateChangedBeforeCommit (0.00s)
    --- FAIL: TestFileExportCreateChangedBeforeCommit/sharenfs_changed (0.00s)
        cov24_test.go:140: code = 0 (<nil>), want 10
FAIL
FAIL	github.com/wommy/empty/internal/endpoint	0.007s
FAIL
```

## Item 5: a refused Publish restores the previous local sharenfs

internal/endpoint/cov24_test.go TestFileExportRefusedPublishRestoresLocalList (own simulator): published to
10.0.0.1, then an extraEtab adds a foreign line for the volume from the second etab read of the next Publish
(after its change). Publish 10.0.0.2 is FailedPrecondition, sharenfs is back to rw=10.0.0.1/32 set locally,
and 10.0.0.2 is not exported. Second case: Sim.Fail fails the restoring set: the joined error names both,
wraps the injected error, code FailedPrecondition.

### 5a: restoreShareNFS always inherits (the Set branch deleted)

Mutation in `internal/endpoint/endpoint.go`:

```diff
- 	if before.ShareNFSLocal {
- 		return e.zfs.Set(ctx, before.Name, map[string]string{"sharenfs": before.ShareNFS})
+ 	if false && before.ShareNFSLocal {
+ 		return e.zfs.Set(ctx, before.Name, map[string]string{"sharenfs": before.ShareNFS})
```

`go test -count=1 -run 'TestFileExportRefusedPublishRestoresLocalList'  ./internal/endpoint` exit 1:

```
--- FAIL: TestFileExportRefusedPublishRestoresLocalList (0.00s)
    --- FAIL: TestFileExportRefusedPublishRestoresLocalList/restored (0.00s)
        cov24_test.go:197: sharenfs "" after a refused publish, was "rw=10.0.0.1/32"
    --- FAIL: TestFileExportRefusedPublishRestoresLocalList/restore_fails (0.00s)
        cov24_test.go:223: want the refusal and the failed restore, both wrapped: volume pvc is served beyond its client list by the NFS export table: /tank/k8s/vols/pvc *(rw); remove these exports (an ancestor dataset's sharenfs with crossmnt, e.g. "on", or /etc/exports) first
FAIL
FAIL	github.com/wommy/empty/internal/endpoint	0.015s
FAIL
```

### 5b: a failed restore is dropped

Mutation in `internal/endpoint/endpoint.go`:

```diff
- 			if rerr := e.restoreShareNFS(ctx, row); rerr != nil {
+ 			if rerr := e.restoreShareNFS(ctx, row); false && rerr != nil {
```

`go test -count=1 -run 'TestFileExportRefusedPublishRestoresLocalList'  ./internal/endpoint` exit 1:

```
--- FAIL: TestFileExportRefusedPublishRestoresLocalList (0.00s)
    --- FAIL: TestFileExportRefusedPublishRestoresLocalList/restore_fails (0.00s)
        cov24_test.go:223: want the refusal and the failed restore, both wrapped: volume pvc is served beyond its client list by the NFS export table: /tank/k8s/vols/pvc *(rw); remove these exports (an ancestor dataset's sharenfs with crossmnt, e.g. "on", or /etc/exports) first
FAIL
FAIL	github.com/wommy/empty/internal/endpoint	0.010s
FAIL
```

### 5c: no restore after a refusal

Mutation in `internal/endpoint/endpoint.go`:

```diff
- 		if len(plan) > 0 {
- 			if rerr := e.restoreShareNFS(ctx, row); rerr != nil {
+ 		if false && len(plan) > 0 {
+ 			if rerr := e.restoreShareNFS(ctx, row); rerr != nil {
```

`go test -count=1 -run 'TestFileExportRefusedPublishRestoresLocalList'  ./internal/endpoint` exit 1:

```
--- FAIL: TestFileExportRefusedPublishRestoresLocalList (0.00s)
    --- FAIL: TestFileExportRefusedPublishRestoresLocalList/restored (0.00s)
        cov24_test.go:197: sharenfs "rw=10.0.0.1/32:10.0.0.2/32" after a refused publish, was "rw=10.0.0.1/32"
    --- FAIL: TestFileExportRefusedPublishRestoresLocalList/restore_fails (0.00s)
        cov24_test.go:223: want the refusal and the failed restore, both wrapped: volume pvc is served beyond its client list by the NFS export table: /tank/k8s/vols/pvc *(rw); remove these exports (an ancestor dataset's sharenfs with crossmnt, e.g. "on", or /etc/exports) first
FAIL
FAIL	github.com/wommy/empty/internal/endpoint	0.010s
FAIL
```

## Item 6: ExpandVolume when the rollback fails too

internal/endpoint/cov24_test.go TestExpandRollbackFailureIsReported (own simulator): a thick 1 GiB filesystem
grows to 2 TiB on a 1 TiB pool; the sim applies refquota, then ENOSPC on refreservation. Sim.Fail then fails
the rollback (its set, or its scan): ResourceExhausted, message has "rolling refquota back", errors.Is the
injected error, the volume is left half-applied. With the pool grown, the retry converges to
refquota = refreservation = 2 TiB. The `before.ID == ""` branch is unreachable from ExpandVolume
(PlanExpandVolume returns the row with every plan) and is not tested.

### 6a: a failed rollback is dropped (only the grow's error returned)

Mutation in `internal/endpoint/endpoint.go`:

```diff
- 		if rerr := e.rollbackExpand(ctx, before); rerr != nil {
+ 		if rerr := e.rollbackExpand(ctx, before); false && rerr != nil {
```

`go test -count=1 -run 'TestExpandRollbackFailureIsReported'  ./internal/endpoint` exit 1:

```
--- FAIL: TestExpandRollbackFailureIsReported (0.00s)
    --- FAIL: TestExpandRollbackFailureIsReported/set (0.00s)
        cov24_test.go:265: the failed rollback is not reported: op 1/1 set tank/k8s/vols/thick: zfs set tank/k8s/vols/thick: no space: cannot set property for 'tank/k8s/vols/thick': size is greater than available space
    --- FAIL: TestExpandRollbackFailureIsReported/scan (0.00s)
        cov24_test.go:265: the failed rollback is not reported: op 1/1 set tank/k8s/vols/thick: zfs set tank/k8s/vols/thick: no space: cannot set property for 'tank/k8s/vols/thick': size is greater than available space
FAIL
FAIL	github.com/wommy/empty/internal/endpoint	0.005s
FAIL
```

### 6b: rollbackExpand ignores its scan error

Mutation in `internal/endpoint/endpoint.go`:

```diff
- 	t, err := e.scan(ctx, before.ID)
- 	if err != nil {
- 		return err
- 	}
- 	return e.apply(ctx, e.plan.PlanExpandRollback(t, before))
+ 	t, err := e.scan(ctx, before.ID)
+ 	if false && err != nil {
+ 		return err
+ 	}
+ 	return e.apply(ctx, e.plan.PlanExpandRollback(t, before))
```

`go test -count=1 -run 'TestExpandRollbackFailureIsReported'  ./internal/endpoint` exit 1:

```
--- FAIL: TestExpandRollbackFailureIsReported (0.00s)
    --- FAIL: TestExpandRollbackFailureIsReported/scan (0.00s)
        cov24_test.go:265: the failed rollback is not reported: op 1/1 set tank/k8s/vols/thick: zfs set tank/k8s/vols/thick: no space: cannot set property for 'tank/k8s/vols/thick': size is greater than available space
FAIL
FAIL	github.com/wommy/empty/internal/endpoint	0.010s
FAIL
```
