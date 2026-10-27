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
