# Adversarial review: ADR 0010 rollback of a failed fresh CreateVolume (at 620cda8)

Method: read `git diff 08ca83f 620cda8`. Ran the suite and 22 mutants on a `git archive 620cda8` copy at
`/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/mut` (the repo was not touched).
Wrote 3 probe tests in that copy (`internal/endpoint/zz_review_test.go`).
Fetched upstream external-provisioner v5.2.0 `pkg/controller/controller.go` and sig-storage-lib-external-provisioner v11.0.1 `controller/controller.go`.

## Findings

### MEDIUM-1 (R4 broken; confirmed by a failing probe): an ambiguous first op plus a failed rollback returns final Internal and leaves the row
- `endpoint.go:597-603`: `fail` calls `resumeErr` only when `changed`. `changed` is set only after a whole plan applies (`:654`), so it is false when op 1 of pass 0 fails.
- Path: `mayHaveRun` (`:700-709`) sets `made=true` for an unclassified or interrupted first op. Then `rollbackCreate` (`:740-746`) joins the rollback failure to an `err` that is still the pre-change Internal.
- External-provisioner treats Internal as ProvisioningFinished (controller.go `checkError`, v5.2.0 L1879-1915). Once the PVC is deleted, nothing retries the claim, and the row is stranded until a manual Reconcile.
- This is the ADR's own motivating case ("the ambiguous first op, Internal"). It contradicts ADR R4 ("a failed rollback ... whose after-change code (retryable) stands").
- Realistic trigger: a `zfs create -V` answer is lost, then the destroy is EBUSY after the one settle (udev probing the zvol it just made). An auto-snapshot `-r` on the parent, or a failed rollback scan, triggers it too.
- Probe `TestReviewAmbiguousFirstOpFailedRollbackIsRetryable` (`loseCreate` with a zvol, plus `sim.Fail` busy on destroy) gives `code 13 (Internal), row left = true`.
- Minimal fix: in `rollbackCreate`'s `case rerr != nil:`, add `err = resumeErr(err)` before `errors.Join`.
  - Verified: with that line the full endpoint suite passes and the probe passes.
  - Do not put `changed || made` in `fail`. That breaks `TestUnknownFailureAfterAMutationIsRetryable` and `TestCreateResumeFailureStaysAborted`, because the rollback that "found nothing" must keep Internal.
- Killing test: the probe above. Also assert that a retry then resumes (no second `create`).

### LOW-MEDIUM-2 (confirmed by a failing probe): the cut clone-source snapshot is not released when the name is taken by something not ours
- `plan.go:777-789` (`PlanRollbackCreate`) releases the stamped `<src>@volume-source-for-volume-<id>` only when no row exists. A row that fails the guards (not owned, another kind or source) returns an empty plan, so the cut stays.
- Path: a clone of a volume. The snapshot is cut (op 1), then a foreign dataset takes `<parent>/c` before the clone, and the clone fails KindExists.
- The code is AlreadyExists (final), the snapshot stays on the source (space, plus one more Reconcile item), and retries are AlreadyExists forever.
- It does not block the source's DeleteVolume: `planDelete` destroys its own unused cut.
- Old `errors.go` text claimed a rollback "would cover ... AlreadyExists ... on a clone after its snapshot". It does not.
- Probe `TestReviewForeignNameAfterCutReleasesCut` (a runner wrapper that creates `c` on the clone call and returns "dataset already exists") reports "the cut snapshot stays (code 6)".
- Fix: when the row fails the guards and `r.SourceVolumeID != ""`, still return `releaseCloneSource(t, src, id)`. It already refuses a cut that is used (`dependent`), already deferred, or stamped for another clone.
- Killing test: that probe.

### LOW-3: R1 says the rollback touches only "what this call's first scan saw absent"; the code also releases a stamped cut the first scan saw present
- `plan.go:503-507`: the snapshot op is `OkExists`. A cut left by an earlier deadline-ended attempt (row absent) counts toward `n>0` in `applyN`, so `made=true`, and the rollback releases it.
- This is benign (stamped for this id, unused), arguably desirable, but it does not match R1's text and no test covers it.
- Fix the ADR text ("... or a cut stamped for this clone and unused"), and add a plan test with a pre-existing stamped cut whose clone fails.

### LOW-4: rename-away during the rollback reads as "undone"
- `apply.go` destroy: `sameDataset`'s `Get` returns `zfs.ErrNotFound`, and `OkMissing` turns that into success.
- If an admin renames the fresh dataset away between the rollback scan and the destroy, `undone=true` and a final pre-change code is returned. The renamed dataset still carries our markers under another name.
- This needs an operator inside a millisecond window. Accept it, or mention it in the ADR. A guid-tied "missing" is not proof of destruction.

### LOW-5 (test gaps found by mutation): guards that survive their removal
Each of these mutants left the suite green:
- `callerEnded` (`endpoint.go:690-691`): removing any one disjunct. The four overlap in every test.
  - The `ctx.Err() != nil` disjunct matters only for a non-zfs failure (`setRoot`, verifyExport's NFS checks) after the caller's ctx ended. My probe cancelling during a failing `zfs set` passed only because the zfs layer classifies the failure as interrupted.
  - Killing test: `Config.Root` returns a plain error after calling `cancel()`. Assert the row is left (no rollback) and the code is not final.
- `context.WithoutCancel` in `rollbackCreate` (`:726`).
  - Killing test: cancel the caller's ctx from a runner hook on the rollback's guid `get`. Assert the destroy still ran.
- `mayHaveRun`: removing `KindInterrupted`, removing `kindDeadline`, or making it always true. These are mostly equivalent, because the guards make an extra rollback harmless and `kindDeadline` is excluded by `callerEnded` anyway. Add a KindInterrupted test that is not the caller's: an ExitError of "signal: killed" while the ctx is alive. `loseCreate` already does that, so pin its kind.
- `!errors.As(err, new(*Error))` wrapper (`:742`): equivalent today. `err` is always an `*Error` when `made`, because `applyN` and `resumeErr` both return one. Dead code.
- `changed = true` before a commit `fail` (`:660`): equivalent, since `changed` is already true from `:654`.

Killed as expected: removing `fresh`, `pass==0`, `beforeChange`, the committed/owned/kind/source guards, and `mayHaveRun`'s KindUnknown.

## Questions checked and found sound
- **Lock held during the rollback:** `fail` runs before `defer release()` (`:579`). The rollback takes no new locks, so there is no new lock-ordering problem. The source volume lock (`vol:src`, plus `snap:`/`vol:` for restores) stays held throughout. `TestCreateRollbackConcurrentSameName` pins this (synctest: no zfs call by the second request until release).
- **Bounded:** at most 30s (`rollbackTimeout`) past the failure, on `WithoutCancel`. Contenders get Aborted from `lock` (wait capped to their deadline minus 1s), which is in-progress for the sidecar.
- **Destroys tied to the dataset the scan read:** guid-tied via `sameDataset`, never `-r`. A snapshot (user, sanoid, CSI), a hold, children or a clone gives FailedPrecondition, so the rollback fails and the result is joined and Aborted.
  - Plan-level pins: `rollback_plan_test.go:47-53`. R2: `crash_test.go:597` `rollback-create` swap case.
  - A restore's origin (a CSI snapshot) is never released: `releasedOrigin` requires the reserved name and that `src` is covered, and the rollback scans only `id` for restores.
- **A received or renamed-in dataset at the name before our create:** the create fails KindExists, so `n=0`, nothing counts as made, and there is no rollback. Adoption mode (`democratic-csi` prefix): the guards rely on `OwnedVolume` and the per-clone stamp. A bare (legacy) cut is refused before anything is made (`plan.go:497`). Two controllers on one parent are out of scope (ADR 0001).
- **Lost commit answer:** the rescan sees Committed, so the plan is empty and the after-change Aborted is kept. The retry returns the compatible volume.

## Sidecar interplay (verified upstream)
- **v5.2.0 `checkError`:** Canceled, DeadlineExceeded, Unavailable and Aborted are InBackground. ResourceExhausted is Reschedule only when `supportsTopology() && SelectedNode != nil`, otherwise Finished. Everything else is Finished. A non-gRPC error is InBackground.
- **Retries continue while the PVC exists:** v5.2.0 sets `FailedProvisionThreshold(0)`, and lib v11.0.1 `processNextClaimWorkItem` (L964-966) calls `AddRateLimited` on every error (1s up to 5m). A final code only drops the claim from `claimsInProgress` (`syncClaim` L1098-1111), so once the PVC is gone no call follows. The provisioner never calls DeleteVolume for a create with no PV. A later CreateVolume uses the same name (`pvc-<uid>`) and hits a clean pool after a successful rollback.
- **ResourceExhausted with WaitForFirstConsumer:** zfs-csi advertises no topology (no ACCESSIBILITY or Topology in `internal/csi`, `cmd`, `deploy`), so `mayReschedule=false` and the result is Finished plus a rate-limited retry. If topology is ever added, Reschedule drops `selected-node` and the scheduler retries. Harmless after a rollback: nothing remains.
- **The only dangerous combination** is a final code with something left, i.e. MEDIUM-1 (and LOW-2 or LOW-4).
- **The ADR's deadline exclusion** keeps rows from sidecar timeouts (default `--timeout` 10s). A later Resume that fails with a final code (for example FailedPrecondition exposure) still strands the row if the PVC is then deleted. The ADR acknowledges this (Reconcile backstop). Note it as residual risk.

## Verdict
Mostly sound. Every destroy is guid-checked, the guards hold, the rollback runs under the lock and is bounded, and the sidecar reasoning checks out against the upstream code.

One medium defect needs fixing before relying on R4: an ambiguous first op whose rollback fails returns final Internal with the row left. The fix is the one-line `resumeErr` in the rollback-failed branch, with the probe as a regression test.

The cut-snapshot leak on a foreign name (LOW-2), the R1 wording (LOW-3) and the mutation gaps (LOW-5) are follow-ups, not blockers.
