# Final codes after a change: audit (HEAD f7aa459, read-only)

## The rule and the sidecars

- **Rule** (errors.go:96-105): "An unclassified failure after a change is Aborted, not Internal ... a final code would strand the half-applied plan." `resumeErr` (endpoint.go:685) is `remapMutated`, and it only re-keys errors that carry an errTable cell (made by `zfsFail`).
- **Gap in the table itself.** The after-change Aborted row is `{ops: mutatingOps, KindUnknown, after: true}` (errors.go:138). `opRead` and `opReadParent` are not in `mutatingOps`, so `remapMutated(zfsErr(...))` is a no-op. A failed scan or get after a change stays Internal even when it is passed through `resumeErr`.
- **The CSI front end passes codes through unchanged** (internal/csi/controller.go:19-20; every RPC does `return nil, err`). It never rewrites Aborted. `server.grpcErr` is 1:1 (server.go:33-37). A transport failure reaches the sidecar as Unavailable or DeadlineExceeded, both of which are retried.

### What the pinned sidecars treat as final

- **external-provisioner v5.2.0** (`pkg/controller/controller.go:1879-1914`, `checkError`):
  - Canceled, DeadlineExceeded, Unavailable, Aborted and non-gRPC errors are InBackground.
  - ResourceExhausted is Reschedule under WaitForFirstConsumer, and Finished otherwise.
  - Everything else is Finished.
- **sig-storage-lib-external-provisioner v11.0.1** (`controller/controller.go:1098-1111, 703-708, 1056-1060`):
  - Finished takes the claim out of `claimsInProgress`.
  - The claim is still requeued while the PVC exists, because csi-provisioner.go:400-401 sets the thresholds to 0.
  - Once the PVC is deleted, a claim that is not in progress is dropped ("claim no longer exists"), and DeleteVolume is never called.
  - So a final code after a change leaks the volume when the PVC is deleted (or WaitForFirstConsumer reschedules) before a retry succeeds.
  - Deletes are retried forever whatever the code (FailedDeleteThreshold(0)), so a DeleteVolume code never strands anything.
- **external-snapshotter v8.2.0** (`pkg/sidecar-controller/snapshot_controller.go:727-749, 362-370`):
  - Canceled, DeadlineExceeded, Unavailable, ResourceExhausted, Aborted and non-gRPC errors are not final.
  - A final error removes `snapshot.storage.kubernetes.io/volumesnapshot-being-created`. Without that annotation, deleting the content does not wait for a snapshot whose handle it never got.
- **external-resizer v1.13.1** (`pkg/util/util.go:237-259`): same set as the snapshotter.
  - A final error only marks a condition, or "infeasible" for InvalidArgument, OutOfRange or NotFound (`pkg/controller/expand_and_recover.go:222-233`).
  - Every error is requeued (`controller.go:317`).
- **Doc discrepancy:** docs/research/edges2/sidecar-conformance.md says non-gRPC errors are final in the provisioner. In v5.2.0 they are InBackground.

## Findings (ranked by likelihood × damage)

### 1. rootErr passes LocalRoot's own Internal through: the 65c03cb fix misses the production path

- **Where:** apply.go:138-139 keeps a classified `*Error`'s code at any `i` and with no cell. LocalRoot always returns `*Error`, and it returns Internal for:
  - open errors other than ELOOP, ENOTDIR, ENOENT and ENOSYS (root_linux.go:47);
  - fstat errors;
  - mountinfo read errors (:61);
  - chown and chmod errors other than EPERM and EROFS (rootSysErr :89), for example EINVAL for an unmappable uid.

  The non-`*Error` branch fixed in 65c03cb is reached only by test setters.
- **Sequence:** NFS create with nfs.uid, gid or mode.
  1. Pass 0 runs create-filesystem (sharenfs set).
  2. Pass 1 runs mount (if needed) and set-root, which fails with LocalRoot's Internal.
  3. `resumeErr` cannot remap it (no cell).
- **Code returned:** Internal (final).
- **Consequence:** an uncommitted volume, already exported, is stranded once the PVC is deleted. It stays until an operator runs `zfs-endpoint reconcile` (manual only).
- **Fix:** in rootErr, treat `e.Code == Internal` as unclassified. That is, only keep `e.Code` when it is not Internal, and fall through to the switch, so `i > 0` gives Aborted and `i == 0` gives `zfsFail(OpSetRoot, ...)`.
- **Test** (cov24_test.go style): reuse `failRoot` and `simNFS`, and set
  `rootErr = &endpoint.Error{Code: endpoint.Internal, Msg: "open", Err: syscall.EIO}`.
  Want Aborted; today the result is 13. Then clear the error, retry, and check that the volume commits with the owner set.
  Also add a unit test in TestRootErrMapping: `resumeErr(rootErr(&Error{Code: Internal}, 0, root))` should give Aborted.

### 2. CreateVolume: reads after a change are Internal, and resumeErr cannot help (table gap)

- **Where:**
  - pass-1 `scanForUnproven` (endpoint.go:560);
  - `headroom` → `childSpace` get (586; headroom.go:237-243; only when MinFreeBytes > 0 and the resume reserves space);
  - verifyExport's scan (639);
  - `readVolume` after the commit (615 → 737);
  - `decodeColumns` Internal (table.go) on any of those scans.

  All of them are `zfsErr` (opRead, not mutated) and are returned raw.
- **Sequence:** every FileExport filesystem create takes two passes (RequireMounted = FileExport, endpoint.go:282).
  1. Pass 0 applies create-filesystem (or snapshot then clone).
  2. Pass 1's scan fails with an unclassified error (any stderr the classifier does not know, a JSON/schema decode error, EIO).
- **Code returned:** Internal (final).
- **Consequence:**
  - Before the commit: an uncommitted volume (exported, for NFS) is stranded as in item 1.
  - After the commit (`readVolume`): the volume is committed but the PV is never made. The PVC retry converges while it exists. If the PVC is deleted first, the volume leaks, and Reconcile collects it only when given a desired set.
- **Fix, two parts:**
  - (a) errors.go:138: drop `ops: mutatingOps`, so KindUnknown after a change is Aborted for every op, reads included.
  - (b) In CreateVolume, set `changed` once a pass applied any op or the scanned row was uncommitted, and send every later error through `resumeErr`. That covers the pass-1 scan, headroom, verifyExport (scan and PlanCreateVolume) and `readVolume`.
- **Test:**
  1. Start from `simNFS(t, nil)`, or a plain sim with `FileExport: true`.
  2. Set `sim.Fail` to return `&zfs.ExitError{Code: 1, Stderr: "boom\n"}` for the first `get ... -d` after a `create` argv was seen (the afterCreate wrapper's counting, cov24_test.go:36-70).
  3. Call CreateVolume of a plain filesystem. Want Aborted; today the result is 13.
  4. Clear the failure and retry. The volume commits.

  For the post-commit variant, fail the scan that follows `set ...provision_success=true`.

### 3. CreateSnapshot: Internal after the snapshot is cut

- **Where:** endpoint.go:1046-1048, the re-scan after apply (`zfsErr`, Internal on an unknown kind), and :1052, `errorf(Internal, "vanished after creation")`.
- **Sequence:**
  1. `zfs snapshot` succeeds.
  2. `scan(sourceVolumeID)` fails, or does not see the row.
- **Code returned:** Internal, which is final for the snapshotter, so it removes the being-created annotation.
- **Consequence:** if the VolumeSnapshot is deleted before a retry, the content goes without DeleteSnapshot (no handle).
  - The owned CSI snapshot leaks.
  - DeleteVolume of its source is then refused for good with FailedPrecondition "has snapshot" (plan.go planDelete).
  - Reconcile does not collect CSI snapshots.
  - Likelihood is low, damage is high: the PV can never be deleted.
- **Fix:** when `len(plan) > 0`, return the re-scan's error through `remapMutated` (after 2a), and make "vanished" Aborted.
- **Test:**
  1. Set `sim.Fail` to fail (stderr "boom") the first `get -d` after a `snapshot` argv.
  2. Call CreateSnapshot. Want Aborted; today the result is 13.
  3. Clear the failure and retry. The same id comes back, and `ListSnapshots` shows one snapshot.

### 4. ResourceExhausted (and other classified codes) mid-plan on a fresh clone

- **Where:** errors.go:136. The NoSpace row has no after-change variant. `opErr` (apply.go:162-172) remaps only cells whose code changes.
- **Sequence:** clone from a volume or snapshot with a thick size (zvol, or a filesystem with reserve=true):
  1. snapshot (from a volume only), then clone, have run;
  2. `sizeOps` `set refreservation=auto|N` fails with ENOSPC ("size is greater than available space").
- **Code returned:** ResourceExhausted. That is Finished under Immediate binding and Reschedule under WaitForFirstConsumer.
- **Consequence:**
  - An uncommitted clone is stranded.
  - For clone-from-volume, `<src>@volume-source-for-volume-<id>` now has a dependent clone, so DeleteVolume(src) is FailedPrecondition "has dependent clone" until an operator reconciles.
  - Likely on a near-full pool, because the headroom guard is off by default.
  - The same shape with deterministic codes is rarer: AlreadyExists or NotFound on OpClone after OpSnapshot, or InvalidArgument from `sizeOps`.
- **Fix, minimal:** add `{ops: mutatingOps, kinds: KindNoSpace, after: true, code: Aborted}` before errors.go:136. The snapshotter and resizer already treat ResourceExhausted as non-final, so only the provisioner sees a difference.
- **Fix, better:** for a fresh create (row absent at the scan) that fails at `i > 0`, destroy the target by the guid just read before returning the classified code, and return Aborted if that rollback fails. This also covers the deterministic codes.
- **Test:**
  1. Make a sim source filesystem volume (capacity set, committed).
  2. Set `sim.Fail` to return stderr "cannot set property for 'x': size is greater than available space" on a `set` argv containing `refreservation=` after a `clone` argv.
  3. Call CreateVolume with SourceVolumeID and Reserve. Want Aborted; today the result is 8.
  4. Also assert that `DeleteVolume(src)` succeeds after the retry converges and the clone is deleted.

### 5. NFS create under a parent mounted nowhere: the volume is created, then refused

- **Where:**
  - plan.go:426-427: a parent mountpoint of none or legacy leaves the new filesystem's mountpoint inherited.
  - The fresh branch never refuses this.
  - Pass 1 then hits plan.go:378-380.
- **Sequence:** pass 0 runs create-filesystem with sharenfs, then pass 1 plans.
- **Code returned:**
  - with nfs.uid, gid or mode: FailedPrecondition "is not mounted" (final);
  - without them: verifyExport returns Unavailable on every retry and never converges.
- **Consequence:** one uncommitted, unmountable dataset per PVC. This needs an operator misconfiguration to trigger, but every create then hits it.
- **Fix:** in PlanCreateVolume's fresh branch, refuse with FailedPrecondition before any op when `nfs != nil && r.Kind == Filesystem && !path.IsAbs(p.parentMount)` (and RequireMounted for `ownsRoot`).
- **Test:**
  1. `simNFS`, then `z.Set(ctx, "tank/k8s/vols", {"mountpoint": "none"})`.
  2. Call CreateVolume with `nfsCreate` plus UID.
  3. Want FailedPrecondition and `!sim.Has("tank/k8s/vols/pvc")`. Today the dataset exists.

### 6. LocalRoot's classified FailedPrecondition after create and mount

- **Where:** root_linux.go:33 (path is not clean), the symlink case (ELOOP/ENOTDIR), the ENOSYS case (openat2 < 5.6), :68-70 (not its mount), and rootSysErr EPERM/EROFS.
- **Code returned:** final, on an exported, uncommitted volume.
- **Assessment:** these are deliberate refusals of host states. ENOSYS is known at startup.
- **Fix:** probe openat2 in `New` when FileExport is set, so the refusal comes before any create. The others are acceptable as they are, but leak on PVC delete, as in item 1.
- **Test:** a `failRoot` returning `errorf(FailedPrecondition, ...)` documents the current behaviour: it asserts that the volume exists uncommitted. There is no failing-first test unless the policy changes.

### 7. verifyExport "served beyond its export" (endpoint.go:663)

- **Code returned:** FailedPrecondition on an uncommitted, exported volume.
- **Assessment:** deliberate, and pinned by TestFileExportCreateServedBeyondByAncestor. Only an administrator can clear it, so FailedPrecondition is honest. The leak on PVC delete is the cost.
- **Action:** document only.

### 8. ExpandVolume: Internal when the rollback also fails (endpoint.go:911)

- **Where:** `errors.Join(err, rollback)` keeps the first code, Internal for an unknown kind at op 0.
- **Sequence:** a thick filesystem `set refquota,refreservation` fails partway, leaving refquota raised, and the rollback fails too.
- **Consequence:** the resizer requeues every error anyway, and the next Expand repairs the under-reserved row (plan.go, the under-reserved branch of PlanExpandVolume). `readVolume` after a successful set has the item-2 shape, with the same mitigation.
- **Damage:** low.
- **Optional fix:** return Aborted when the rollback fails.
- **Test:** sim.Fail on the rollback `set refquota=` gives code 13 today.

### 9. PublishFileExport: a failed restore is reported as a clean FailedPrecondition (endpoint.go:1235)

- **Sequence:**
  1. `set sharenfs` adds the client.
  2. The exposure check refuses.
  3. `restoreShareNFS` fails.
- **Code returned:** `errors.Join(refused, ...)` reads as FailedPrecondition, with the grant left in place.
- **Consequence:** a retry plans nothing (the client is already listed), refuses again, and skips the restore (`len(plan) == 0`). The grant persists.
- **Scope:** no CSI path calls this (no attacher, deploy/k8s/controller.yaml:40). Low likelihood, but it is an access leak.
- **Fix:** on a restore failure, return Unavailable (joined error).
- **Test:** sim.Fail on the restore `inherit`/`set` gives FailedPrecondition today.

### 10. Handler panic gives Internal whatever ops applied (server/daemon.go:213)

- **Assessment:** low. It is a bug path, and a retry may panic again.

## Checked, no finding

- **DeleteVolume, DeleteSnapshot:** FailedPrecondition or NotFound mid-plan is harmless. The provisioner retries deletes forever (FailedDeleteThreshold(0)), and every destroy is OkMissing, so a retry converges.
- **Reconcile:** a failed group becomes a Skipped entry and the call returns nil (reconcile.go:446-470, 750-754). Its errors all come before apply.
- **UnpublishFileExport:** FailedPrecondition after the set (endpoint.go, the `covers` check) is re-derived the same way on retry, by a reshare.
- **CreateVolume resume plans with unclassified zfs op failures:** these are Aborted (resumeErr, opErr). Commit failure is Aborted.
- **CSI controller:** returns no codes of its own after an endpoint call, apart from InvalidArgument pre-checks before the call.
