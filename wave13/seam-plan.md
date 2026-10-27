# seam-plan: S2, S3, S6, S4, S8 + H-01, H-02

Branch `seam-plan` is in worktree `/home/user/empty/.claude/worktrees/agent-a682e26295247bdb6`. It is based on `088e69b` (the HEAD of claude/gallant-johnson-1qaxp5). It is not pushed.

## Commits (oldest first)

| sha | subject |
|---|---|
| 85cc01c | endpoint: take a free lock when no time is left to wait (H-01) |
| 71ac3b6 | endpoint: CreateVolume's resume uses OwnedVolume (S3, H-02) |
| 503ddc0 | endpoint: page tokens survive churn; a never-issued desired id is skipped (S4, S8) |
| 0d3c1e6 | endpoint: zvol clones round to the source's block size; commit only mounted filesystems (S2, S6) |

Each intermediate commit was extracted with `git archive`. Each one passes gofmt, `go vet ./internal/...` and `go test ./internal/endpoint ./internal/server`.

Final checks on HEAD:
- `gofmt -l` prints nothing.
- `go vet ./...` passes.
- `go mod tidy -diff` shows no diff.
- `go test -race -count=1 ./...` passes in every package.

## Anomalies and decisions (most important first)

1. **S6: the chosen design does not heal itself after a kill-before-mount.**
   - I chose UNAVAILABLE over OpMount:
     - An OpMount needs `zfs.Client.Mount` and a `mount` verb in the simulator. Both are in internal/zfs, which is outside my scope.
     - It would also add a new op kind to every op table.
   - The UNAVAILABLE path is idempotent:
     - A retry rescans and writes nothing until the volume is mounted.
     - After that it commits. The mount can come from an operator's `zfs mount` or from a sharenfs change, because libzfs mounts on a share.
   - The cost: when a deadline SIGTERM kills `zfs create` after the ioctl and before the mount, the row stays uncommitted. CreateVolume then returns UNAVAILABLE until someone mounts it, or until Reconcile collects it as uncommitted (default MinAge is 1h) and the next retry creates it again.
   - **Follow-up for the internal/zfs owner:** add `zfs mount` (treating "already mounted" as success) and plan it before the commit. That makes this case heal itself.
2. **S6 reads root mode from `Config.FileExport`.**
   - cmd sets `FileExport: root`, and I may not touch cmd/.
   - `Planner.RequireMounted = cfg.FileExport` is set in `New`, and the coupling is documented on `Config.FileExport`.
   - If cmd ever enables FileExport without root, or root without FileExport, this needs its own Config field.
   - Contract tests use `setup()` with `FileExport: true`. On the real-ZFS VM leg every filesystem CreateVolume now runs a second scan and plan pass. This is expected to pass because root mounts, but I have **not verified it on the VM**.
3. **S4: the token format changed.**
   - Keeping ABORTED "only for a token that cannot be an id" would break csi-sanity's `invalid-token` case, which expects ABORTED, and `internal/server` TestStandalone's `"nope"`. Both strings are valid ids.
   - Tokens are now `after/<last id>`. `/` cannot appear in any id, so a string the endpoint never issued is still ABORTED.
   - A token issued by an older build (a bare id) is ABORTED once during an upgrade, and the CO restarts its walk.
4. **S8: I edited a test outside internal/endpoint.**
   - `internal/server/server_test.go` TestReconcile pinned InvalidArgument for `"a/b"`.
   - It now asserts that `"a/b"` is skipped (DryRun OK) and that an empty id is InvalidArgument. The change is test-only.
   - The proto comment for `desired_volume_ids` does not mention the new behaviour. Changing it means regenerating gen/, so I left it.
5. **H-01 incidentally changes S5's endpoint-side symptom.**
   - With `LockWait < 0`, `wait <= 0` now takes the TryAcquire path. A free lock is taken and a held one is ABORTED immediately, instead of everything being ABORTED.
   - The S5 probe `TestSeamNegativeLockWaitAbortsEverything` now fails, meaning that symptom is gone. Refusing the flag (exit 78) is still for the cmd owner.
6. **S2: I did not change the error classification.** The seams doc suggested classifying "must be a multiple of" as `KindInvalidProperty`, which is in internal/zfs and outside my scope. The ABORTED→INTERNAL flip is fixed generally instead: a resumed provision (an uncommitted row of ours) maps an unclassified zfs failure to ABORTED even on its first op (`resumeErr`).
7. **The S6 probe still passes, and that is correct.** `rEndpoint` has no FileExport (non-root), and non-root keeps committing unmounted volumes. The other probes (S2, S3, S4, S8) now fail ("fixed?").

## Fail-before / pass-after evidence

Before, I measured at 088e69b. Temporary API stubs with no behaviour change (`RequireMounted` field, `commits`, `tokenPrefix`, `volumeToken`, page `valid` param) were added only so the tests compiled; they were later removed. Full log: `/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/seam-before.txt`. After, all of them pass: `/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/seam-after.txt`.

| item | test (file) | before at HEAD | after |
|---|---|---|---|
| S2 | TestPlanCloneRoundsToSourceBlockSize (seams_test.go, planner table) | volsize "2000011264" (want "2000027648") for the volume and snapshot sources; resume plans 2000011264; the limit row was OK (want OutOfRange) | PASS |
| S2 | TestCloneOfLargeBlockZvolProvisions (sim) | `op 3/4 set … 'volsize' must be a multiple of volume block size (131072)` | PASS |
| S2 | TestCreateResumeFailureStaysAborted (sim + Fail) | `attempt 1: code 13 (op 1/2 set …)`, want Aborted | PASS |
| S3 | TestCreateNeverStampsRowWithoutName (raw runner) | code 10 and mutation `set …committed_at …provision_success=true tank/v/m` | PASS (AlreadyExists, 0 mutations) |
| H-02 | TestPlanCreateDoesNotAdoptUnnamedManaged (planner) | plan `[set refquota=67108864, set provision_success]` | PASS |
| S6 | TestPlanCreateCommitsOnlyMounted (planner table) | resume plan `[set provision_success]` on an unmounted canmount=on row; the fresh plan carries the commit | PASS |
| S6 | TestCreateUnmountedFilesystemIsUnavailable (sim whose create/clone leaves the dataset unmounted) | `attempt 0: <nil>` (committed while unmounted) | PASS (Unavailable; the retry is read-only; commits after mount; zvol unaffected) |
| S4 | TestPageResumesAfterDeletedToken (pure `page`) | `invalid starting token "b"` | PASS |
| S4 | TestListTokenSurvivesDeletedVolume (sim; volumes and snapshots) | `invalid starting token "v2"` | PASS |
| S4 | TestPagination (contract, runs on the real leg too) | extended | PASS |
| S8 | TestReconcileSkipsNonIssuableDesiredID (raw runner) | `invalid volume name "ns/pv"` | PASS (skip reported; `b` still collected; "" is InvalidArgument) |
| S8 | TestReconcileAgainstDesiredSet (contract), server TestReconcile | updated to the new semantics | PASS |
| H-01 | TestShortDeadlineStillTakesAFreeLock (lockwait_test.go) | `free lock with 500ms left: operation pending on [vol:a]` | PASS |

All new tests use t.Parallel and no sleeps. They sit at the planner level where the planner proves the row; sim or raw runner tests are used only for the apply loop, the error mapping and the scan.

## What changed (internal/endpoint)

- **plan.go**
  - `RequireMounted` on Planner.
  - `sourceBlockSize`: a Block clone rounds to its source volume's BlockSz, and the limit is checked after rounding. validateCreate skips the default-rounded limit check for Block clones.
  - The resume branch uses `!row.OwnedVolume()` and returns AlreadyExists.
  - With RequireMounted: the resume returns Unavailable for `mountable(row) && !row.Mounted`, and a fresh Filesystem plan omits the commit.
  - New `commits(plan)` helper.
- **endpoint.go**
  - `lock` uses TryAcquire when `wait <= 0` and the context is still live.
  - CreateVolume runs at most two scan/plan/apply passes. The second runs only when the first plan created something without committing.
  - `resumeErr` maps Internal to Aborted for a resumed row.
  - `page` uses `after/<id>` tokens, a binary search to the first id after the token, and per-list token validators.
  - `New` wires RequireMounted. Config.FileExport doc updated.
- **reconcile.go:** only an empty desired id is InvalidArgument. Non-issuable ids reach the planner, which reports them as SkipDesiredMissing. Documented on ReconcileRequest.
- **Tests:** seams_test.go and lockwait_test.go (new); the `simEndpoint` helper in reach_helpers_test.go; TestPagination, TestReconcileAgainstDesiredSet and internal/server TestReconcile updated.
