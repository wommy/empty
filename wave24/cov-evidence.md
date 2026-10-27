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

## Item 7: rootErr mapping for a failed OpSetRoot

internal/endpoint/cov24_internal_test.go TestRootErrMapping: a table over the pure rootErr(err, i, plan).

### 7a: a classified root error loses its code

Mutation in `internal/endpoint/apply.go`:

```diff
- 	if errors.As(err, &e) {
- 		return &Error{Code: e.Code, Msg: what + ": " + e.Msg, Err: e.Err}
+ 	if false && errors.As(err, &e) {
+ 		return &Error{Code: e.Code, Msg: what + ": " + e.Msg, Err: e.Err}
```

`go test -count=1 -run 'TestRootErrMapping'  ./internal/endpoint` exit 1:

```
--- FAIL: TestRootErrMapping (0.00s)
    cov24_internal_test.go:38: classified, first op: op 1/1 set-root tank/v/a: read-only (code 13), want code 9 with "op 1/1 set-root tank/v/a: read-only"
    cov24_internal_test.go:38: classified, after a change: op 2/2 set-root tank/v/a (earlier ops applied; a retry converges): read-only (code 10), want code 9 with "op 2/2"
FAIL
FAIL	github.com/wommy/empty/internal/endpoint	0.006s
FAIL
```

### 7b: the caller's deadline is not DeadlineExceeded

Mutation in `internal/endpoint/apply.go`:

```diff
- 	case errors.Is(err, context.DeadlineExceeded):
- 		code = DeadlineExceeded
+ 	case false && errors.Is(err, context.DeadlineExceeded):
+ 		code = DeadlineExceeded
```

`go test -count=1 -run 'TestRootErrMapping'  ./internal/endpoint` exit 1:

```
--- FAIL: TestRootErrMapping (0.00s)
    cov24_internal_test.go:38: deadline: op 1/1 set-root tank/v/a: context deadline exceeded (code 13), want code 4 with "op 1/1"
    cov24_internal_test.go:38: deadline, after a change: op 2/2 set-root tank/v/a (earlier ops applied; a retry converges): context deadline exceeded (code 10), want code 4 with "op 2/2"
FAIL
FAIL	github.com/wommy/empty/internal/endpoint	0.006s
FAIL
```

### 7c: cancellation is not Aborted

Mutation in `internal/endpoint/apply.go`:

```diff
- 	case errors.Is(err, context.Canceled):
- 		code = Aborted
+ 	case false && errors.Is(err, context.Canceled):
+ 		code = Aborted
```

`go test -count=1 -run 'TestRootErrMapping'  ./internal/endpoint` exit 1:

```
--- FAIL: TestRootErrMapping (0.00s)
    cov24_internal_test.go:38: canceled: op 1/1 set-root tank/v/a: context canceled (code 13), want code 10 with "op 1/1"
FAIL
FAIL	github.com/wommy/empty/internal/endpoint	0.009s
FAIL
```

### 7d: after a change is not Aborted

Mutation in `internal/endpoint/apply.go`:

```diff
- 	case i > 0:
- 		return &Error{Code: Aborted, Msg: what + " (earlier ops applied; a retry converges)", Err: err}
+ 	case false && i > 0:
+ 		return &Error{Code: Aborted, Msg: what + " (earlier ops applied; a retry converges)", Err: err}
```

`go test -count=1 -run 'TestRootErrMapping'  ./internal/endpoint` exit 1:

```
--- FAIL: TestRootErrMapping (0.00s)
    cov24_internal_test.go:38: unclassified, after a change: op 2/2 set-root tank/v/a: x (code 13), want code 10 with "earlier ops applied"
FAIL
FAIL	github.com/wommy/empty/internal/endpoint	0.005s
FAIL
```

### 7: bug found end to end

cov24_test.go TestFileExportCreateRootFailureIsRetryable: an NFS CreateVolume with UID set, Config.Root
wrapping the sim and returning errors.New("x"). Expected Aborted (a half-made volume must stay retryable);
got Internal: `code = 13 (op 1/1 set-root tank/k8s/vols/pvc: x), want 10`. A new filesystem commits only
after a re-scan, so the set-root runs as op 1/1 of the second pass's resume plan: rootErr maps an
unclassified first-op failure to Internal with no error-table cell, and resumeErr (remapMutated) only
remaps errors that have a cell, so it stayed Internal: a final code for external-provisioner, which then
never deletes the half-made volume. Fix (separate commit): rootErr returns that case through
zfsFail(OpSetRoot, err, false, what), Internal as before but with a cell, so resumeErr remaps it to Aborted.
TestRootErrMapping also asserts resumeErr(rootErr(x, 0, ...)) is Aborted. Reverting the fix:

### 7e (BUG, fixed): the pre-fix rootErr, an unclassified first-op failure without a cell

Mutation in `internal/endpoint/apply.go`:

```diff
- 	default:
- 		// unclassified on the plan's first op: Internal, keyed as a zfs
- 		// failure would be, so that a resumed provision (resumeErr; the
- 		// set-root of a new NFS volume is its resume's first op) remaps it
- 		// to Aborted instead of stranding the half-made volume
- 		return zfsFail(OpSetRoot, err, false, what)
```

`go test -count=1 -run 'TestFileExportCreateRootFailureIsRetryable|TestRootErrMapping'  ./internal/endpoint` exit 1:

```
--- FAIL: TestFileExportCreateRootFailureIsRetryable (0.00s)
    cov24_test.go:314: code = 13 (op 1/1 set-root tank/k8s/vols/pvc: x), want 10
--- FAIL: TestRootErrMapping (0.00s)
    cov24_internal_test.go:48: unclassified, resumed: op 1/1 set-root tank/v/a: x (code 13), want Aborted
FAIL
FAIL	github.com/wommy/empty/internal/endpoint	0.015s
FAIL
```

## Item 8: a cancelled Reconcile run, and a failed re-scan under the locks

internal/endpoint/cov24_internal_test.go (a4 harness, own simulator): TestReconcileCancelledMidRunStops cancels
the context from Sim.Fail on the first destroy of a digested run over 3 undesired volumes: one action, the
other two skipped with the SkipNotAttempted prefix and still present. TestReconcileRescanFailureDestroysNothing
fails the second scan of the run (the re-scan under the locks): the error is returned and nothing is
destroyed. The re-plan failure branch (PlanReconcile erroring on a scan that just succeeded) has no seam
and is not tested.

### 8a: applyReconcile does not stop when the context ends

Mutation in `internal/endpoint/reconcile.go`:

```diff
- 		case ctx.Err() != nil:
- 			failed[g] = true
+ 		case false && ctx.Err() != nil:
+ 			failed[g] = true
```

`go test -count=1 -run 'TestReconcileCancelledMidRunStops'  ./internal/endpoint` exit 1:

```
--- FAIL: TestReconcileCancelledMidRunStops (0.00s)
    cov24_internal_test.go:78: 3 destroys, actions [{Op:{Kind:destroy Target:tank/v/a Source: Size:0 Sparse:false Recursive:false Defer:false Props:map[] OkExists:false OkMissing:true GUID:1002 Path:} Target:a Reason:not in the desired set Volumes:[a]} {Op:{Kind:destroy Target:tank/v/b Source: Size:0 Sparse:false Recursive:false Defer:false Props:map[] OkExists:false OkMissing:true GUID:1003 Path:} Target:b Reason:not in the desired set Volumes:[b]} {Op:{Kind:destroy Target:tank/v/c Source: Size:0 Sparse:false Recursive:false Defer:false Props:map[] OkExists:false OkMissing:true GUID:1004 Path:} Target:c Reason:not in the desired set Volumes:[c]}]: want the first group only
FAIL
FAIL	github.com/wommy/empty/internal/endpoint	0.008s
FAIL
```

### 8b: a failed re-scan under the locks is ignored

Mutation in `internal/endpoint/reconcile.go`:

```diff
- 	if t, err = e.scan(ctx, ""); err != nil {
+ 	if t, err = e.scan(ctx, ""); false && err != nil {
```

`go test -count=1 -run 'TestReconcileRescanFailureDestroysNothing'  ./internal/endpoint` exit 1:

```
--- FAIL: TestReconcileRescanFailureDestroysNothing (0.00s)
    cov24_internal_test.go:118: re-scan failure not reported: reconcile needs a scan of the whole parent
FAIL
FAIL	github.com/wommy/empty/internal/endpoint	0.005s
FAIL
```

## Item 9: health probe transitions, drain cut short by a second signal, admin listen failure

internal/server/probe_test.go TestProbeTransitions: Probe in a synctest bubble with an injected 30s interval
(the fake clock advances one tick per time.Sleep; no wall-clock wait). The check fails, passes, then cancels
the context and fails: NOT_SERVING, SERVING, then unchanged, and Probe returns.
cmd/zfs-endpoint/signal_test.go: TestAdminListenFailureClosesTheCSISocket (a regular file at -admin-listen:
run fails and the -listen socket path is gone); TestSecondSignal re-runs the test binary as a child, which
calls secondSignal and sends itself SIGTERM: the channel closes and the child passes (the parent never
receives a signal; the minute-long select is a failure bound only).

### 9a: Probe reports a check the shutdown interrupted

Mutation in `internal/server/daemon.go`:

```diff
- 		err := check(ctx)
- 		if ctx.Err() != nil {
- 			return
- 		}
+ 		err := check(ctx)
+ 		if false && ctx.Err() != nil {
+ 			return
+ 		}
```

`go test -count=1 -run 'TestProbeTransitions'  ./internal/server` exit 1:

```
--- FAIL: TestProbeTransitions (0.00s)
    probe_test.go:65: tick 3: 3 checks, NOT_SERVING, want SERVING
FAIL
FAIL	github.com/wommy/empty/internal/server	0.011s
FAIL
```

### 9b: Probe never turns NOT_SERVING

Mutation in `internal/server/daemon.go`:

```diff
- 		d.SetServing(err == nil)
- 	}
- }
+ 		d.SetServing(true)
+ 	}
+ }
```

`go test -count=1 -run 'TestProbeTransitions'  ./internal/server` exit 1:

```
--- FAIL: TestProbeTransitions (0.00s)
    probe_test.go:65: tick 1: 1 checks, SERVING, want NOT_SERVING
FAIL
FAIL	github.com/wommy/empty/internal/server	0.014s
FAIL
```

### 9c: the -listen socket is not closed when -admin-listen fails

Mutation in `cmd/zfs-endpoint/main.go`:

```diff
- 		if adminLis, err = unixsock.Listen(adminSock); err != nil {
- 			lis.Close()
+ 		if adminLis, err = unixsock.Listen(adminSock); err != nil {
```

`go test -count=1 -run 'TestAdminListenFailureClosesTheCSISocket'  ./cmd/zfs-endpoint` exit 1:

```
--- FAIL: TestAdminListenFailureClosesTheCSISocket (0.00s)
    signal_test.go:37: -listen socket left behind: <nil>
FAIL
FAIL	github.com/wommy/empty/cmd/zfs-endpoint	0.016s
FAIL
```

### 9d: secondSignal does not catch the signal

Mutation in `cmd/zfs-endpoint/main.go`:

```diff
- 	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
- 	again := make(chan struct{})
+ 	_ = signal.Notify
+ 	again := make(chan struct{})
```

`go test -count=1 -run 'TestSecondSignal'  ./cmd/zfs-endpoint` exit 1:

```
--- FAIL: TestSecondSignal (0.01s)
    signal_test.go:63: child: signal: terminated
        === RUN   TestSecondSignal
FAIL
FAIL	github.com/wommy/empty/cmd/zfs-endpoint	0.017s
FAIL
```

## Item 10: lock and scan backpressure exhaustion

internal/endpoint/cov24_internal_test.go. TestDeleteVolumeLockPassesAreBounded (cloneFixture, synctest bubble,
LockWait 1 min on the fake clock): (1) vol:src held through the call: the second pass's lock wait ends
Aborted "operation pending", clone kept. Note: wave22 expected "kept changing" here; holding one source
reaches the lock error, not the pass bound. (2) While each pass waits, the test adds a stamped
<sN>@volume-source-for-volume-clone on another volume it holds and releases the previous one: three passes
each find a new busy volume, and DeleteVolume is Aborted "kept changing", clone kept.
TestWholeScanSlotWaitEndsWithTheContext fills e.scans with maxWholeScans tokens: ListVolumes with an expired
deadline is DeadlineExceeded, with a cancelled context Aborted; with a slot free it succeeds.

### 10a: deleteLocked treats a lock failure as another pass

Mutation in `internal/endpoint/endpoint.go`:

```diff
- 	release, err := e.lock(ctx, volKeys(*vols)...)
- 	if err != nil {
- 		return false, err
- 	}
+ 	release, err := e.lock(ctx, volKeys(*vols)...)
+ 	if err != nil {
+ 		return true, nil
+ 	}
```

`go test -count=1 -run 'TestDeleteVolumeLockPassesAreBounded'  ./internal/endpoint` exit 1:

```
--- FAIL: TestDeleteVolumeLockPassesAreBounded (0.00s)
    --- FAIL: TestDeleteVolumeLockPassesAreBounded/source_held_throughout (0.00s)
        cov24_internal_test.go:149: DeleteVolume(clone) with vol:src held: volume clone: the volumes its delete touches kept changing; retry, want the lock wait's Aborted
FAIL
FAIL	github.com/wommy/empty/internal/endpoint	0.008s
FAIL
```

### 10b: DeleteVolume's pass bound raised from 3 to 100

Mutation in `internal/endpoint/endpoint.go`:

```diff
- 	for range 3 {
- 		again, err := e.deleteLocked(ctx, volID, &vols)
+ 	for range 100 {
+ 		again, err := e.deleteLocked(ctx, volID, &vols)
```

`go test -count=1 -run 'TestDeleteVolumeLockPassesAreBounded'  ./internal/endpoint` exit 1:

```
--- FAIL: TestDeleteVolumeLockPassesAreBounded (0.00s)
    --- FAIL: TestDeleteVolumeLockPassesAreBounded/a_new_busy_volume_each_pass (0.00s)
        cov24_internal_test.go:195: DeleteVolume(clone): operation pending on [vol:clone vol:src vol:s2 vol:s3], want Aborted after three passes
FAIL
FAIL	github.com/wommy/empty/internal/endpoint	0.014s
FAIL
```

### 10c: a scan-slot wait past its deadline is Aborted

Mutation in `internal/endpoint/table.go`:

```diff
- 			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
- 				code = DeadlineExceeded
+ 			if false && errors.Is(ctx.Err(), context.DeadlineExceeded) {
+ 				code = DeadlineExceeded
```

`go test -count=1 -run 'TestWholeScanSlotWaitEndsWithTheContext'  ./internal/endpoint` exit 1:

```
--- FAIL: TestWholeScanSlotWaitEndsWithTheContext (0.00s)
    cov24_internal_test.go:217: expired: waiting for a whole-parent scan slot: context deadline exceeded, want DeadlineExceeded waiting for a slot
FAIL
FAIL	github.com/wommy/empty/internal/endpoint	0.005s
FAIL
```

### 10d: a cancelled scan-slot wait is DeadlineExceeded

Mutation in `internal/endpoint/table.go`:

```diff
- 			code := Aborted
- 			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
+ 			code := DeadlineExceeded
+ 			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
```

`go test -count=1 -run 'TestWholeScanSlotWaitEndsWithTheContext'  ./internal/endpoint` exit 1:

```
--- FAIL: TestWholeScanSlotWaitEndsWithTheContext (0.00s)
    cov24_internal_test.go:222: cancelled: waiting for a whole-parent scan slot: context canceled, want Aborted waiting for a slot
FAIL
FAIL	github.com/wommy/empty/internal/endpoint	0.051s
FAIL
```

## Commits (branch cov-tests, not pushed; each SSH-signed)

- afdd1a8 item 1
- 8624a63 item 2
- a702044 item 3
- c77c1b4 item 4
- 82a1e44 item 5
- f4d0c0e item 6
- e267457 item 7 (rootErr table)
- 65c03cb item 7 fix: rootErr production change + TestFileExportCreateRootFailureIsRetryable
- a871314 item 8
- 9af1842 item 9
- 9ebfaea item 10
- c9f0cef ci/real-zfs-weights.txt: the 12 new endpoint tests at 0.00

## Gate (at c9f0cef)

gofmt clean on the 184 tracked and untracked .go files; go vet ./... and -tags nightly; go mod tidy -diff;
go test -race ./... (all ok); MODEL_SEQS=1000 TestModel ok (32.5s); nightly TestExploreModelMasksStillBite|
TestExploreLifecycle ok (180.7s); bash ci/check-suites.sh ok; ci/real-zfs-plan.sh 2x3 runs on the new list.
