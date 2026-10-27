# Rolling back a fresh CreateVolume: design study (wave26)

This study is read-only, against f7aa459. Paths are relative to `internal/endpoint/` unless stated.
It does not touch the error-code mapping after a change (that is retry-fix's work) or snapshot size recording.

## 1. What happens today to an uncommitted volume

**What the failure leaves behind.** `CreateVolume` (endpoint.go:535) applies the plan. The first op, create or clone, makes the row. The commit (`provision_success` + `committed_at`) is always the last op. For NFS, the commit only happens after `verifyExport`.

Any failure in between returns early. Nothing is undone:
- `resumeErr` or `opErr` remaps the failure (Aborted after a change, apply.go:162, errors.go:247).
- The row stays owned and uncommitted.
- For a clone of a volume, the stamped `<src>@volume-source-for-volume-<id>` also stays. It pins the source.

**Who can see the row.** No CSI read path shows it. `ServedVolume()` (table.go:116) gates GetVolume, ListVolumes, expand, export, snapshot sources and clone sources. Only two things act on it:
- the **Resume** branch of `PlanCreateVolume` (plan.go:355), on a retry with the same name;
- `DeleteVolume`.

**DeleteVolume of an uncommitted volume works.** `planDelete` (plan.go:784) needs only `OwnedVolume()`, not `Committed`. It destroys the row and releases the stamped origin (`releasedOrigin`, `releaseCloneSourcesFor`).

But external-provisioner only calls DeleteVolume for a PV. A PV exists only after a CreateVolume succeeded. So DeleteVolume reaches the row in one case: provisioning in the background later succeeds, and the PV of the deleted claim goes Released, then Delete.

**The CO side.** Codes Aborted, Unavailable, ResourceExhausted, DeadlineExceeded and Canceled are non-final (`nonFinal`, fsm_explore_test.go:979; docs/research/adversarial/csi-spec.md S7). For a non-final code, external-provisioner keeps the claim in its in-memory `claimsInProgress` and goes on retrying after the PVC is deleted. If a retry then succeeds, the PV is created and immediately reclaimed through DeleteVolume, so nothing leaks.

This is my reading of sig-storage-lib-external-provisioner's `syncClaimHandler`/`claimsInProgress`. It is not in the repo's research notes; verify it against the pinned sidecar version.

The row leaks in three cases:
- (a) the error is final: Internal, FailedPrecondition, NotFound, AlreadyExists, InvalidArgument;
- (b) the provisioner restarts or changes leader while the claim is in background (the map is in memory);
- (c) a non-Kubernetes caller simply stops.

**Reconcile does not collect it automatically.** Reconcile (reconcile.go) does collect uncommitted rows: `ReasonUncommitted`, no digest needed, not counted in `MaxDeletions`. The limits:
- The age gate is `tooYoung`: `MinAge` from zfs `creation`. The default is 1h (`DefaultReconcileMinAge`), and the floor is 10 min (`MinReconcileMinAge`).
- It is on demand, on the optional admin socket only (ADR 0001). Nothing in the repo schedules it. `cmd/zfs-endpoint reconcile` and `ci/unitprobe` are the only callers.
- A run whose plan also holds an *undesired* volume, sent without a digest, applies nothing. The uncommitted rows wait too (reconcile.go:682).

So "collected by reconcile, if at all" is accurate. With no operator running Reconcile, the row stays forever.

**Cost of a leaked row while it stays:**
- **Clone of a volume:** the pinned snapshot keeps the source's overwritten blocks. DeleteVolume of the *source* is FailedPrecondition "has dependent clone" until the row goes.
- **NFS:** the row is created with `sharenfs` set (plan.go:431), so it stays exported to the requested clients. If `verifyExport` refused for exposure (FailedPrecondition, which is final), it stays served *beyond* its export.

## 2. Is a bounded rollback safe under the repo's invariants?

Yes, if it is built from existing parts and gated as below.

- **Ownership (ADR 0003):** gate on `OwnedVolume()`, `VolumeName == r.Name`, source markers equal to the request, and `!Committed`. These are the same checks Resume uses. A row that fails them (owner changed, vanished) is left alone, as cov24's "changed before commit" cases expect.
- **guid re-check (OWN-15):** do not hand-roll destroys. Plan them with `planDelete(t, id, false)` on a fresh scan. Every destroy then carries the scanned guid and runs through `destroy`/`sameDataset`. The residual window is the same documented one as every other destroy.
- **"Nothing else references it":** most of this holds by construction:
  - An uncommitted row cannot be a snapshot source or a clone source; both need `ServedVolume`.
  - CSI never received its id.
  - `planDelete` already refuses children, snapshots, dependent clones and user holds.
  - The plain (non `-r`) destroy makes zfs refuse anything that appeared since the scan.
- **Lock:** the rollback runs before `release()`, under `vol:<id>` (plus the source keys). A concurrent same-name CreateVolume or DeleteVolume waits. Reconcile's `TryAcquire` skips it as busy. No second actor can see the half-state.
- **"This call created it":** the call's first scan, under the lock, showed the id absent (`t.Get(id)` false in pass 0), and at least one mutation was attempted. Track this across both passes: the NFS/mounted-fs commit happens in pass 1, where the row already exists in `t`.

  A first op whose result is ambiguous also counts: the answer was lost (k0-after) or the op was killed, so it may have run. Today that case is **Internal (final) with the dataset present**, a silent leak (seams_test.go:128 pins "never mutated stays Internal"). The re-scan decides.
- **Idempotent retries:** the retry after a rollback is a first attempt again. The crash fingerprint (crash_test.go:225) has no guid, creation or committed_at, so recreate-after-rollback converges to the same fingerprint.
- **Crash convergence:** the rollback is best-effort and in-process only. A real crash (process death) still leaves an uncommitted row. Resume and Reconcile stay the backstop, unchanged. Nothing is removed.
- **Precedent:** `rollbackExpand` (endpoint.go:937) and `restoreShareNFS` already compensate within the call. They use `context.WithoutCancel` + `rollbackTimeout` and report with `errors.Join(err, rollback err)`, keeping the original code.

## 3. Where a rollback would be wrong, or a trap

| Case | Verdict |
|---|---|
| A retry by a different request with the same name | Safe. Rollback happens only within the call that made the row; resumes never roll back. A request whose kind or source differs is AlreadyExists *before* apply, and a pre-apply error never rolls back. Rolling back fixes a trap: today, after a failed fresh create, the same name with another kind or source is AlreadyExists (final) forever. |
| Two concurrent retries of the same name | Safe. They serialize on `vol:<id>`. The second scans after the rollback and creates fresh; it never sees the half-row. |
| **Caller deadline or cancel (DeadlineExceeded/Canceled)** | **Do not roll back.** On a pool where a clone or mount takes longer than the sidecar's 10s, each attempt times out, rolls back, and the next attempt redoes it: a livelock. Today, Resume finishes it on the next try. Gate this out with `kindDeadline` / `context.Canceled` (errors.go). |
| The clone source changes between attempts | Acceptable. A rollback releases the internal snapshot, so the retry clones a newer point of the source. CSI does not promise which point; a retry is a new request in time. If the source was deleted in between, the retry is NotFound, consistent with the source being gone. Today the leaked clone *blocks* that delete. |
| Restore from a CSI snapshot | Safe. The CSI snapshot is not ours to release; only the clone is destroyed. A deleted-meanwhile snapshot gives NotFound on the retry. |
| NFS export side effects | Rollback helps: `zfs destroy` unshares, and the leaked export (or exposure) goes. Downside: under a persistent failure (exposure through an ancestor's crossmnt, root-owner misconfiguration), each retry exports and unexports the volume briefly: export churn every ≤5 min, plus exportfs and nfsd work. A mounted-and-busy destroy fails (busy) and leaves the row for Resume. That is acceptable. |
| ENOSPC (pool full): does a rollback free space? | Almost nothing now. A fresh fs or clone owns only metadata; a thick `create -V` is atomic, so there is nothing to roll back; and the `set refreservation` that failed reserved nothing. What it frees is *future* space: the internal snapshot stops pinning the source's overwritten blocks. If the headroom floor (`-min-free-bytes`) is set, the next retry becomes a pre-apply ResourceExhausted refusal (headroom.go), which is cheaper than re-creating. Destroy works on a full pool (zfs slop space). Neutral-to-good, not a fix. |
| Zvol destroyed right after create | udev probing gives EBUSY. `destroy` settles once and retries. If still busy, the rollback fails, the row stays, and Resume handles it. |
| Two endpoint processes on one parent | Not covered by the in-process lock. This is the same assumption every destroy and Reconcile already make (ADR 0001: one parent, one CO). |

## 4. Recommendation

Roll back a fresh create. Keep retry and Resume for caller deadlines and as the crash backstop, and Reconcile as the last resort.

Relying on retry + Reconcile leaves real leaks:
- final codes (verifyExport exposure, permission failures after a change, the ambiguous-op-0 Internal);
- provisioner restarts with a claim in background;
- non-Kubernetes callers;
- and Reconcile is opt-in, manual and admin-only.

A rollback makes the error mean what CSI says it means: "no volume exists". It also costs nothing on the success path.

### Minimal design

1. **The planner function.** `Planner.PlanRollbackCreate(t Table, r CreateVolumeRequest) Plan`: pure, golden-testable. It returns nil unless the row is `OwnedVolume`, `VolumeName == r.Name`, `Kind`, `SourceType` and `SourceID` equal the request, and the row is `!Committed`. Otherwise:
   - it returns `planDelete(t, id, false).plan()`, which gives the origin release (deferred, first), the snapshots (none expected) and the volume, each guid-tied;
   - for `r.SourceVolumeID`, if the row is absent (the clone failed after the snapshot was cut), it destroys `-d` the stamped clone-source snapshot for `id` on that source, when it is unused. This is `releaseCloneSourcesFor` narrowed to the one known source: set `t.srcsFor = id` on a table covering the source, as `scanDelete` does.
2. **The endpoint wrapper.** `Endpoint.rollbackCreate(ctx, r, id)` uses `context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)`. It runs `scanFor(id, r.SourceVolumeID)`, then `PlanRollbackCreate`, then `apply`.
3. **In `CreateVolume`:**
   - Before the loop: `fresh := id absent in the pass-0 scan`. Set `mutated = true` once `apply` (or `verifyExport`'s reshare, or the commit) has been called.
   - On any error return after that, if `fresh && mutated && !deadlineOrCanceled(err)`, call `rollbackCreate`.
   - On rollback success, keep `err`'s code and append "; rolled back: nothing was left".
   - On rollback failure, return `errors.Join(err, fmt.Errorf("rolling back the create: %w", rerr))` with err's code. The row stays for Resume and Reconcile.
   - The code choice itself belongs to retry-fix: do not change codes here. Note for them: after a *successful* rollback, a pre-change (final) code would be truthful again. Leave this as a follow-up, not part of this change.
4. **Docs.** Add a **Rollback** entry to CONTEXT.md (`Commit` / `Resume` neighbourhood). Update the Reconcile doc comment's "left by a CreateVolume that crashed or failed" to "crashed, or whose rollback failed".

### Invariants to state (and test)

- **R1.** A rollback destroys only datasets this call's first scan saw absent and its own ops made. It never touches a committed row, a row with another name marker, a CSI snapshot, or anything with children, holds or clones.
- **R2.** Every rollback destroy is guid-tied to a scan taken after the failure, under the same locks.
- **R3.** A rollback never runs on a caller deadline or cancel, and never on a resume (a row that existed at the first scan).
- **R4.** A rollback never changes the returned code; a failed rollback is reported, joined.
- **R5.** After a successful rollback, the parent is byte-for-byte what it was before the call (same fingerprint as no call at all). A retry converges to the uninterrupted fingerprint.

## 5. Tests that prove it

Use the sim hooks (`zfstest.Sim.Fail`), `failRoot`, `afterCreate`, the runner wrappers (`faultRunner`, `swapRunner`) and `wantCode`.

1. **`TestCreateRollbackLeavesNothing`** is a table over create shapes:
   - shapes: block thick/sparse, fs, fs-reserve, fs-props, NFS, NFS-root, clone of snapshot (zvol thick), clone of volume (fs reserve, zvol thick);
   - for each, fail mutating call k (k ≥ 1) via `Sim.Fail`: NoSpace on `set refreservation`, unknown error on `set ns:provision_success`, `failRoot`, `SetNFSDown`, and the exposure case (ancestor `sharenfs=on`);
   - it asserts: the code is unchanged from today, the fingerprint equals the pre-call fingerprint (R5), the source has no `volume-source-for-volume-*` snapshot, DeleteVolume(source) succeeds, and a clean retry equals the uninterrupted fingerprint.
2. **Clone fails after the cut:** `Sim.Fail` on `clone`. Op 0 (the snapshot) applied; assert the stamped snapshot is released.
3. **Ambiguous op 0:** a runner that runs `create` and then returns ExitError -1 (the k0-after shape). Assert the dataset is gone and the code is still Internal.
4. **No rollback on deadline:** a slow-clone runner with ctx timeout. Assert the row stays uncommitted and the next call resumes it (fewer mutations than a fresh create).
5. **No rollback on a resume:** an uncommitted row made by hand (as endpoint_test.go:663 does), plus a failing resume. Assert the row remains.
6. **Guard:** add rollback cases to `swapCases` (`TestDestroyNeverHitsAReplacedDataset`). Swap at the rollback's guid check; assert Aborted joined and the impostor untouched. Golden planner cases for `PlanRollbackCreate`: committed, name mismatch, source mismatch, children, snapshot, hold, absent row with an unstamped reserved snapshot. Each gives nil or a refusal.
7. **Concurrency:** two goroutines CreateVolume the same name; the first fails at k. The second blocks on the lock, then creates fresh and commits; it never returns AlreadyExists or Resume work.
8. **The rollback itself fails:** `Sim.Fail` returns busy on `destroy`, twice (past the settle). Assert a joined error with the original code and an uncommitted row. A retry resumes and commits.
9. **Crash sweep: keep Resume covered.** `faultRunner` today keeps serving calls after the "crash", so with a rollback it would silently test rollback + fresh create instead of Resume.
   - Add a `dead` mode: every call after the crash fails with errCrash. That is a truer process-death model and keeps today's Resume coverage.
   - Run the current mode as "survives" to cover rollback.
   - Add a double fault: crash at k, then crash at rollback mutation j; this must converge.
   - Real-pool cost (ADR 0005): run the "survives" mode on the sim leg only, or rotate it like `crashSample`.
10. **Model/fsm:** rerun `fsm_explore_test`, `model_test` and `modelrun_test`. Uncommitted states become reachable only via crash or a failed rollback; update any expected-state sets that list them as a failure outcome.
11. **Existing tests that must flip** (they assert that the row remains):
    - `TestFileExportCreateServedBeyondByAncestor` (cov24_test.go:79);
    - `TestFileExportCreateRootFailureIsRetryable` (cov24_test.go:300, the `sim.Has` check);
    - `TestFileExportCreateNotServed` (export_create_contract_test.go:91, comment "stays uncommitted");
    - cov24 `TestFileExportCreateChangedBeforeCommit`: "sharenfs changed" is now rolled back; "vanished" and "owner changed" are guarded and left alone.

## 6. Call-count impact

- **`TestRPCCallBudget`: zero.** It pins success and retry main paths only, and the rollback runs only on an error return. Optionally add a separate failure-path budget row, e.g. "CreateVolume clone of volume rollback" with `Sim.Fail`.
- **Extra zfs calls on the failure path:**
  - fs, zvol, NFS, or clone of snapshot: +3 (`get -t … P/x`, `get guid P/x`, `destroy P/x`);
  - clone of volume: +6 (scan of x, scan of src, guid + `destroy -d` of the origin, guid + destroy of x);
  - clone failed after the cut: +4 (two scans, guid, `destroy -d`);
  - a busy zvol destroy adds a settle, + guid + destroy.
- **Per retry under a persistent failure:** Resume costs about 2–4 calls per retry (scans + the failing op). Rollback costs a full fresh path up to the failure plus 3–6. Example: an fs clone of a volume with reserve that hits ENOSPC costs ~3 calls today and ~12 with rollback, at ≤1 retry per 5 min (sidecar backoff). The forks are negligible; the real cost is create/destroy txgs and NFS export churn. With `-min-free-bytes` set, a full pool turns retries into a 2–3-call headroom refusal.

## 7. Coordination

- **retry-fix (error mapping after a change):** this design reads `CodeOf(err)` and the cell's kind only, to exclude the caller's deadline and cancel. It must not change codes. Merge order does not matter, but tests 1 and 3 assert codes, so take them from retry-fix's table at merge time.
- **Snapshot size recording:** no overlap. The internal clone-source snapshot is released by planDelete's existing ops, not by a new path.
