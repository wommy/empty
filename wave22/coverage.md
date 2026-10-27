# Wave 22: Pareto triage of untested code (read-only)

Repo HEAD d232d63. Nothing in the repo was modified.

## Method

- `go test -coverprofile=wave22/cover.out ./...` gives a 62.0% total, pulled down by `gen/` and the `nfsref` port. Per package: endpoint 92.7, csi 90.1, zfs 90.2, server 83.6, unixsock 81.4, lock 84.6, cmd/zfs-endpoint 86.8, cmd/zfs-csi 30.9.
- That profile counts only each package's own tests. A second run, `-coverpkg=./internal/...,./cmd/...` (`wave22/coverx.out`, 86.7%), credits code reached from other packages. A block counts as uncovered below only when **both** runs leave it at 0 (`wave22/both.txt`, 263 blocks in endpoint/server/csi/lock/unixsock/cmd). Two things turned out to be covered from other packages and are excluded: the lock wait-cancel path, and server.Reconcile's action mapping.
- The VM suites (`ci/real-zfs-suites.txt`: `endpoint`, `csi`) run the **same** endpoint and csi tests against a real pool (the harness switches on `ZFS_TEST_POOL`). Real-only additions:
  - TestRealFacts, TestRealGetJSONMatchesSim, TestPropertyRoundTrip
  - TestNFSReplay (real nfsd)
  - TestZFSDiffReplay, TestModel on a pool
  - the crash and OWN-15 sweeps (first destroy only on PRs; the full sweep runs nightly)
  - csi TestSanity

  So the VM adds real-zfs/nfsd fidelity, plus the LocalRoot, FileEtab and FileNFSD happy paths. It adds **no new error branches**: tests that need fault injection call `requireSim`, so they skip on the VM. Every gap below is therefore also a gap on the VM unless noted.
- Not covered here: sim fidelity (internal/zfs/zfstest), fork counts, and k8s manifests. Other agents are on those.

## Top 10 (risk x reachability)

### 1. Reconcile's max-deletions gate never runs. HIGH, sim
`internal/endpoint/reconcile.go:690-695`. TestReconcileMaxDeletionsAndDigest (`adv4_test.go:316`) calls `Reconcile{MaxDeletions:2}` **without a PlanDigest**. It gets FailedPrecondition from the *digest* gate (`reconcile.go:681-685`), so the "3 > 2" assertion would still pass if the max-deletions check were deleted. Once a digest is supplied, the check is the only brake on a mass collection from a wrong desired set.
- **Test:** 4 committed volumes, desired={keep}.
  - Dry-run, then `Reconcile{PlanDigest: dry.Digest, MaxDeletions: 2}`: expect FailedPrecondition, the message contains "more than max deletions", and all 3 volumes still exist.
  - Same again with MaxDeletions 0 and 11 undesired volumes (DefaultMaxDeletions 10).
  - Also assert the error text in the existing test, so it cannot pass through the wrong gate again.

### 2. Admin-role uid refusal, no-peer and stream denial are not wired into tests. HIGH, pure unit
`internal/server/roles.go:117-119` (admin socket, peer uid neither 0 nor euid: PermissionDenied), `:107-109` (no peer: Unauthenticated) and `:133-137` (`stream` interceptor, 0%). `adminPeer` has its own unit test, but `authorize` never takes the refusal branch: tests run as root or over bufconn (`HasCred=false`). Removing the `adminPeer` call from `authorize` would break no test.
- **Test:** call `authorize` with `peer.NewContext(ctx, &peer.Peer{AuthInfo: PeerInfo{Role: RoleAdmin, HasCred: true, UID: euid+1}})` on the Reconcile method and expect PermissionDenied. With no peer, expect Unauthenticated. Call `stream` with a non-health FullMethod: PermissionDenied, handler not called. With `/grpc.health.v1.Health/Watch`: handler called.
- No ZFS needed.

### 3. OWN-15 guard in the busy-then-settle retry window. HIGH, sim
`internal/endpoint/apply.go:93-98`.
- **Rename race:** after a busy destroy and `Config.Settle`, the second `sameDataset` check (97-98) is never exercised. `swapRunner` (`crash_test.go:408`) swaps *before* the first guid check, never between the busy destroy and the retry. That window lasts as long as `udevadm settle`, the longest unguarded gap in any destroy.
- **Settle failure:** returning the original busy error (94-95) is also untested.
- **Test:** sim with `Fail` returning busy on the first `destroy` of the target. Settle hook renames the target away and creates an impostor carrying the same markers (`KeepGUID` is not needed: a new guid is the point). Expect DeleteVolume to be Aborted, the impostor present, and the original present under its new name. A second case: Settle returns an error, and the result is the busy error (FailedPrecondition) with exactly one destroy argv in the audit.

### 4. CreateVolume(NFS) does not commit a volume served beyond its export. HIGH, sim
`internal/endpoint/endpoint.go:650-653` (verifyExport: served beyond, so FailedPrecondition, uncommitted), plus `:632-634` (vanished or owner changed, Aborted) and `:636-639` (sharenfs changed mid-provision, Aborted). The startup exposure check only *warns* at runtime (`cmd/zfs-endpoint/main.go:healthCheck`). An ancestor crossmnt export that appears after startup is caught by this branch alone, and it has never run.
- **Test:** `setupNFS`, then `zfs set sharenfs=on tank/k8s` (the sim models crossmnt for "on"), then `CreateVolume(nfsCreate("pvc","", "10.20.0.0/24"))`. Expect FailedPrecondition with "served beyond", ListVolumes empty, `committed` not set. After `sharenfs=off` on the ancestor, a retry converges.

### 5. Publish refusal does not restore the previous *local* sharenfs. HIGH, sim
`internal/endpoint/endpoint.go:1194-1196` (restoreShareNFS's `Set` branch, the volume already had a local list) and `:1172-1175` (restore fails: joined error). Only the inherit branch is tested. A wrong restore leaves the new client exported after a refused Publish, which widens exposure silently.
- **Test:** a volume already published to client A. A wrapping `EtabReader` adds a crossmnt ancestor line only after the first apply, so the planner passes and the post-apply exposure check fails. Publish client B: expect FailedPrecondition, sharenfs equal to `rw=A` (source local), and B not served. A second case uses `Fail` on the restoring `zfs set`: the error is joined, wraps both, and has code FailedPrecondition.

### 6. ExpandVolume when the rollback itself fails. MED-HIGH, sim
`internal/endpoint/endpoint.go:860-863` (grow failed and `rollbackExpand` failed: joined error) and `:884-892` (`before.ID==""`, scan error). The rollback success path is covered. When the rollback fails, refquota stays raised without its reservation (the half-applied state ADR'd as PlanExpandRollback). The caller must see both errors, and a retry must converge.
- **Test:** sim `Fail` the refreservation set *and* the rollback refquota set. Expect the error to contain "rolling refquota back", its code to be the grow's, and a retry with `Fail` cleared to converge to refquota = refreservation = new size.

### 7. rootErr mapping for a failed OpSetRoot. MED-HIGH, sim
`internal/endpoint/apply.go:135-150`: a classified `*Error` keeps its code; DeadlineExceeded; Canceled becomes Aborted; and `i>0` becomes Aborted ("earlier ops applied"). The last case is the half-created NFS volume (`create -o sharenfs` ran, chown failed). If it were reported Internal, the provisioner stops retrying and the volume is stranded.
- **Test:** a table over the pure `rootErr(err, i, plan)`, and one end-to-end case: wrap `Sim` as `Config.Root` returning `errors.New("x")`. `CreateVolume` with NFS UID set returns Aborted. With the root setter fixed, a retry converges with the owner set.
- `LocalRoot`'s own failure branches (`root_linux.go:46-62,75-93`: ENOSYS, missing mountinfo, chown EPERM/EROFS mapped to FailedPrecondition) are VM/root territory. The EPERM mapping can run locally as non-root against a tmpdir plus a fake Mountinfo file with matching major:minor.

### 8. A cancelled Reconcile run (shutdown mid-collection). MED, sim
`internal/endpoint/reconcile.go:453-456`: once ctx ends, every remaining group must be reported `SkipNotAttempted` and nothing more destroyed. This is the drain path: `Daemon.Shutdown` cancels handler contexts. `reconcile.go:722-727` (re-scan or re-plan fails under the locks) is also uncovered.
- **Test (internal):** 3 undesired groups, then `applyReconcile(ctx, plan, done)` where `done(0)` calls `cancel()`. Expect Actions to be group 0's alone, groups 1-2 in Skipped with the `SkipNotAttempted` prefix, and their datasets still present. Alternatively, cancel from a `Fail` hook on the first destroy, through public `Reconcile`.

### 9. Health probe transitions, and drain cut short by a second signal. MED, pure unit
- `internal/server/daemon.go:99-111`: the `Probe` loop body never ticks, because main's interval is 30s. Untested: NOT_SERVING on a failing check (suspended pool), back to SERVING, and *no* flip when ctx ends during the check (100-102).
- `cmd/zfs-endpoint/main.go:420-425`: `secondSignal` is 0%; the drain-interrupt path is only reached through an injected `d.interrupt`.
- `main.go:238-241`: when the admin listen fails, the CSI listener's close is untested.
- **Test:** `Probe(ctx, time.Millisecond, check)` with a check scripted err, then nil, then cancel-inside. Poll `Health.Check` for NOT_SERVING then SERVING; after cancel, the status is unchanged. For `secondSignal`: `signal.Notify` is process-wide, so in a subprocess test, `syscall.Kill(os.Getpid(), SIGTERM)` and the channel closes. For 239: point `-admin-listen` at an existing non-socket file; `run` errors and the `-listen` path is gone (the socket was unlinked).

### 10. Lock and scan backpressure exhaustion. MED, sim (internal)
- `internal/endpoint/endpoint.go:744`: after 3 passes, DeleteVolume returns Aborted because the clone source's `vol:` lock stays busy. `:753` is the lock error.
- `internal/endpoint/table.go:452-457`: waiting for a whole-parent scan slot ends with DeadlineExceeded vs Aborted (`maxWholeScans=2`, the namespace-deletion memory bound).

These are the only guards against livelock and an unbounded scan pile-up.
- **Test (internal):** a clone volume whose source is held through `e.locks.Acquire("vol:src")` for the whole call. DeleteVolume(clone) is Aborted with "kept changing" and the clone still exists. For the scan: fill `e.scans` with 2 tokens, then `ListVolumes` with a 10ms deadline gives DeadlineExceeded, and with a cancelled ctx gives Aborted.

## Honourable mentions (lower risk or cheaper to defer)

- **CSI request validation, pure unit with a fake EP** (`internal/csi/controller.go`):
  - `capacityRange` negative or required>limit (364-369)
  - ExpandVolume nil or zero capacity_range (582-590)
  - `noParams` rejecting non-`csi.storage.k8s.io/` keys on CreateSnapshot (346-348, 627)
  - block and mount in one request (212)
  - fs_type≠zfs and the non-NFS-volume message in ValidateVolumeCapabilities (245, 254)

  The endpoint re-validates capacity (`plan.go:156-181`), so for capacity the risk is the wrong gRPC code, not bad state.
- **Publish re-share retry errors** (`endpoint.go:1142-1147`) and the nfsd thread read error mapped to Unavailable (`:1204`). Sim, via `Fail` and a stub `NFSD`.
- **scanDelete when the parent vanishes between the two reads** (`endpoint.go:802-805`) gives Unavailable, not success. Sim, via `Fail` on the snapshot `list`.
- **Startup:**
  - SIGTERM while waiting for the parent (`startup.go:134-135`) is a clean stop
  - "cannot run zfs" is mapped to exit 78 (`:136-137`, `:177-180`)
  - lockParent's too-long name and the ancestor-conflict message (`lockparent.go:51-54,76-77`)

  Cheap unit tests through the existing `deps` seams.
- **unixsock** `bindPrivate` and `ShareWithGroup` syscall-failure branches (`unixsock.go:98-126,180-194`): only reachable with a read-only or full directory. Low value.
- **`FileEtab` and `FileNFSD`** (`export.go:602-633`, 0% locally): the VM covers them through real nfsd. A missing etab reads as "no exports" (fail-open for exposure, by design). A 3-line tmpfile test would pin that.

## Simulator vs VM summary

Items 1, 3, 4, 5, 6, 7, 8 and 10 need only the sim plus existing seams (`Sim.Fail`, runner wrappers, `Config.Settle`, `Config.Etab`, `Config.Root`, `Config.NFSD`). Items 2 and 9 need no ZFS at all. Only LocalRoot's syscall branches and real-exportfs behaviour need the VM, and the VM already covers their happy paths.
