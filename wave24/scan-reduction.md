# Cutting endpoint zfs calls per RPC (design study, wave 24)

Repo at 286cf47. The repo was not changed. Every proposal below except P4 and P7 was prototyped in a scratch copy
(`scratchpad/wave24/copy`). Diff: `scratchpad/wave24/scan-reduction-prototype.patch`.
`go test ./...` passes on the prototype. The one failure is `TestGoldenPlans`, which P9 is expected to change.

## Method

- **Per-RPC sequences.** A probe test (`copy/internal/endpoint/zz_probe_test.go`) wraps the sim runner, logs each call's argv per RPC, and runs every RPC once and then as a retry. Config: FileExport=true, MinFreeBytes=0 (the defaults the tests use). Output: `probe.txt` (before) and `probe-after.txt` (after).
- **Suite totals.** `ZFS_TEST_CALLS=calls.tsv go test ./internal/endpoint` on the simulator. The copy's `zfscalls_test.go` gained a 5th column naming the outermost exported RPC.
  - Baseline: 12274 endpoint calls. 8349 come from CreateVolume (68%); 5597 of those are `zfs get` scans.
  - The simulator suite stands in for the real-ZFS PR leg. Its test mix differs, but the per-RPC savings are the same on a real pool.

### Per-RPC calls (simulator; `<cols>` = the ~40-column scan)

| RPC | before | after | sequence today |
|---|---|---|---|
| CreateVolume block, new | 5 | 4 | scan(id) · Exists(parent) · create -V · set commit · scan(id) |
| CreateVolume block, retry | 2 | **1** | scan(id) · scan(id) |
| CreateVolume fs, new | 7 | 6 | scan(id) · Exists(parent) · get mountpoint(parent) · create · scan(id) [pass 2] · set commit · scan(id) |
| CreateVolume fs, retry | 2 | **1** | scan(id) · scan(id) |
| CreateVolume fs+NFS, new | 8 | 6 | as fs, plus a second scan(id) in verifyExport right after the pass-2 scan |
| CreateVolume from snapshot, new | 7 | 6 | scan(id) · Exists · scan(src) · clone · set refres · set commit · scan(id) |
| CreateVolume from snapshot, retry | 3 | 2 | scan(id) · scan(src) · scan(id) |
| CreateVolume fs from volume, new | 11 | 9 | scan(id) · Exists · scan(src) · mountpoint · snapshot · clone · set · scan(id) · **scan(src)** · set commit · scan(id) |
| ExpandVolume, new / retry | 3 / 2 | 3 / **1** | scan · set · scan / scan · scan |
| DeleteVolume, present | 4 | 4 | scan(id) · list parent snapshots (names) · get guid · destroy |
| DeleteVolume, absent or retry | 3 | 2 | scan(id) · Exists(parent) · list parent snapshots |
| CreateSnapshot new / retry | 3 / 1 | 3 / 1 | whole-parent scan · snapshot · scan(vol) |
| DeleteSnapshot | 3 | 3 | scan(vol) · get guid · destroy -d |
| Publish/Unpublish, Get, List | 1–2 | same | — |

Suite total after all prototyped proposals: **12274 → 10303 endpoint calls (−16%)**. CreateVolume alone: 8349 → ~6500 (−22%).

## Proposals, ranked by calls saved × safety

### P1. Skip the post-op re-read when nothing was applied (no-op retry). Saved: −388 in the suite.
- **Call sites:**
  - `internal/endpoint/endpoint.go:603`: `return e.readVolume(ctx, id)` after a pass-0 plan that is empty with no commit.
  - `endpoint.go:866`: `e.readVolume(ctx, volID)` when `len(plan)==0`.
  - The fix returns `volumeView(row)` from the table `t` already read.
- **Saves:** 1 per idempotent CreateVolume (2→1; 3→2 for a clone) and 1 per no-op ExpandVolume (2→1).
- **Invariant:**
  - Only a served volume is returned.
  - The planner's committed branch (`plan.go:344-401`) has already required `OwnedVolume && Committed` on that row. The prototype re-checks `row.ServedVolume()` anyway.
  - The second scan happened under the same lock with no op in between. It read the same data twice: the textbook case of a redundant scan.
- **Guarded by:** `TestCreateVolumeIdempotent`, `TestCreateVolumeProperties`, `TestExpand`, and the csi idempotency tests.
- **Risk:** very low. The window it gives up runs from the scan to the reply, and it is the same window the RPC always has between the re-read and the reply.
- CreateSnapshot already does this (`endpoint.go:991`: it re-reads only `if len(plan) > 0`).

### P2+P3. Prove the parent once per RPC, not once per missing volume. Saved: −992 in the suite.
- **Call sites:**
  - `table.go:476`: `scan` runs `zfs.Exists(parent)` whenever a single-volume scan says "does not exist".
  - `endpoint.go:560-571`: CreateVolume then reads the parent's mountpoint (FS) or scans the source (clone). Either one already proves the parent exists.
- **Change:**
  - Add `scanParent(ctx, only, confirm bool)`. With `confirm=false`, a not-found returns an absent table and an "unconfirmed" flag.
  - `scanFor` clears the flag when any other scanned volume was found: a child cannot exist without its parent.
  - CreateVolume clears it when `parentMountpoint` succeeds. That function already maps not-found to `parentUnavailable`.
  - Only otherwise (a new plain block volume) does CreateVolume run `Exists`.
- **Saves:** 1 per new FS create and 1 per new clone (block or FS). New plain block creates are unchanged.
- **Invariant:** "only a classified not-found counts as absent" (ADR 0003). A missing or exported parent is Unavailable, never NotFound and never "absent → create".
- **Guarded by:** `TestMissingParentIsUnavailableNotAbsent`, but it covers only DeleteSnapshot and GetVolume. **Add a test (prototype: `scratchpad/wave24/zz_parent_test.go`) where CreateVolume (block, FS, clone) and DeleteVolume on a destroyed parent each return Unavailable.**
  - The prototype's first cut called `parentUnavailable(parent, nil)`. `zfsFail(nil)` returns nil, so a block create got NotFound from `zfs create`. The full suite stayed green; only the new test caught it.
  - When Exists says false, build the error explicitly (or pass the scan's not-found error).
- **Risk:** low, with that test. The order of checks changes: the parent proof now comes after the source scan and not before, but all of it stays under the same locks.

### P6. DeleteVolume of an absent volume: the parent snapshot listing is the parent proof. Saved: −103 in the suite.
- **Call site:** `endpoint.go:794`: `scanDelete` → `e.scan(ctx, id)` → `Exists(parent)`. The very next call, `ListColumns(parent, -t snapshot -d 2)` at `endpoint.go:798`, already maps not-found to `parentUnavailable`.
- **Change:** use `scanParent(id, false)` here.
- **Saves:** 1 per DeleteVolume of an absent volume (3→2). This is the common CSI retry and post-success repeat.
- **Invariant:** a missing parent is Unavailable, not OK. A DeleteVolume that returned OK would make the CO forget a volume that might still exist.
- **Guarded by:** the new missing-parent test from P2 (its DeleteVolume case), `TestDeleteVolume`, and the `delete_scope_test.go` tests.
- **Risk:** low.

### P9. Fold the commit into the create when one atomic create is the whole provision. Saved: −390 in the suite.
- **Call site:** `plan.go:532`: `return append(plan, commit), nil`.
- **Change:**
  - When the plan is a single `OpCreateVol` (or `OpCreateFS` with `!RequireMounted`) and there is no NFS, add the commit properties (`provision_success`, `committed_at`) as `-o` on the create.
  - Update `commits()` (`plan.go:539`) to recognize them on a create op.
  - Restrict this to `nfs == nil`. Otherwise CreateVolume's NFS split (`endpoint.go:584`) would move the create itself after verifyExport.
- **Saves:** 1 per new plain block create (5→4). In production FS creates keep the separate commit: they need the mount check, and FileExport=true there.
- **Invariant:** a committed volume is complete. `zfs create -o` is one sync task, so the volume and its commit appear together. Ownership markers already ride on the create for the same reason (`plan.go:499-504`).
- **Guarded by:**
  - `TestCrashAtEveryMutationConverges` passes: one crash point goes away.
  - `TestGoldenPlans` must be reviewed and then `-update`d.
  - `TestCreateVolumeResumesInterruptedProvision`.
- **Risk:** medium-low.
  - It changes the domain definition in CONTEXT.md ("Commit: the last op of a create"), which needs an amendment.
  - A retry still converges: it finds the volume committed.

### P5. The FS create's resume pass re-reads only the new volume, not its sources. Saved: −90 in the suite.
- **Call site:** `endpoint.go:560`. On pass 1, `scanFor(id, src…)` re-scans the source volume that pass 0 read under the same `vol:<src>` lock.
- **Change:** pass 1 scans only `id` and merges in pass 0's source rows.
- **Saves:** 1 per new FS clone (11→10 alone; 9 with P2/P3).
- **Invariant:**
  - The resume branch (`plan.go:355-393`) uses the source only for `sourceSize` and `sourceBlockSize`.
  - The source is locked for the whole RPC, and the only change to it since pass 0 is the endpoint's own snapshot (`<src>@volume-source-for-volume-<id>`), which that branch never reads.
  - Ownership of the source was checked in pass 0, before anything was cut (`TestCloneSourcesMustBeOwned`).
- **Guarded by:** `TestCloneSourcesMustBeOwned`, `TestCloneFromVolumeReleasesInternalSnapshot`, `TestCloneResizeFailureConverges`, and the crash sweep.
- **Risk:** low.
  - An administrator who changes the source's refquota between the passes is no longer seen.
  - Pass 0 already sized the clone from the old value, so this is arguably more consistent.

### P8. verifyExport reuses the resume pass's table when nothing was applied before the commit. Saved: −8 in the suite (1 per NFS create).
- **Call sites:**
  - `endpoint.go:592`: `e.verifyExport(ctx, id)`.
  - `endpoint.go:627`: `verifyExport` → `e.scan(ctx, id)`.
  - When `plan[:len-1]` is empty, that scan reads exactly what pass 1 just read.
- **Change:** pass `&t` for try 0 only. Try 1, after a re-share, still re-scans.
- **Saves:** 1 per new NFS create (8→7; 6 with P2/P3).
- **Invariant:** the export is verified against the row as it is after every op that ran. If any op ran (mount, set root, sharenfs), the scan stays.
- **Guarded by:** the `nfs_create_test.go` tests and `export_contract_test.go` (Publish/verify), plus `TestDestroyNeverHitsAReplacedDataset`, which uses `setupNFS`.
- **Risk:** low.

### P7 (not prototyped). A committed clone's retry skips the source scan.
- **Call site:** `endpoint.go:560`. On a retry, `scanFor` reads the source only for `sourceBlockSize` and the `want` rounding (`plan.go:313-320`).
- **Change:** scan the target first. If it is a committed owned clone, use `row.BlockSz`: a clone keeps its origin's volblocksize, which is create-only.
- **Saves:** 1 per idempotent clone retry (2→1 after P1).
- **Invariant:** the compatibility verdict must not change.
- **Risk:** medium.
  - `mustCover(id, srcID)` (`plan.go:307`) needs the table to cover the source. Skipping the scan means either lying about coverage or adding a planner seam.
  - A source deleted since the create already rounds by the default today, so behaviour there stays the same.
- **Guarded by:** `TestCreateVolumeProperties` and the clone block-size tests (`clonesize_test.go`). Needs a new retry-after-source-deleted case.

### P4 (not recommended yet). Drop `readVolume` after a mutating create.
- **Call site:** `endpoint.go:603`. The pass-1 or verifyExport row, plus the commit just set, already describe the volume.
- **Saves:** about 1 per new create (~900 in the suite, the largest single item).
- **Invariant lost:** "only ours is ever returned: something replaced it after the plan" (`endpoint.go:708`). The commit `zfs set` goes by name with no guid check. The re-read is the only thing that catches a replacement between the last scan and the commit.
  - No test guards it today: nothing matches "vanished or changed owner after provisioning".
  - For zvols it also gives up "reported sizes are always re-read" (Config.VolBlockSize doc).
- **Risk:** medium-high. Do this only together with a swap-on-`set` variant of `swapRunner` (`crash_test.go:408`) that pins the expected behaviour.

## Production scan cost (fork count unchanged; less data per call)

- **CreateSnapshot whole-parent scan** (`endpoint.go:980`).
  - It reads O(all datasets + snapshots) × ~40 columns on every create and every retry. On large pools this is the endpoint's costliest call.
  - Names are global (`plan.go:977`, `mustBeWhole`), so the whole parent must be read. But only about 10 columns matter: type, volsize, refquota, creation, defer_destroy, managed, success, csi_volume_name, csi_snapshot_name, csi_snapshot_source.
  - A narrow column set roughly quarters the bytes.
  - Needs a distinct thin-table type so no planner can read the columns it never fetched.
- **CreateSnapshot re-read** (`endpoint.go:992`): `scan(vol)` uses `-d 1`, so it reads every snapshot of the volume. Reading just `<vol>@<snap>` is O(1). The source row is already in `t`. Same call count.
- **DeleteSnapshot** (`endpoint.go:1038`): narrowing to the one snapshot has the same benefit, but costs an extra parent/vol confirmation on the absent path. It is a trade, not a win; low priority.
- **DeleteVolume's parent snapshot listing** (`endpoint.go:798`): names-only, O(all snapshots). See below for why it must stay.
- **Headroom** (out of scope; another builder owns `headroom.go`).
  - When `MinFreeBytes > 0`, `childSpace` adds a parent `get`. That get could later merge with P2's parent proof and the mountpoint read into one `zfs get type,mountpoint,available,refreservation,refquota,quota,reservation,used <parent>`.
  - Saves up to 2 more per new create. Deferred until that work lands.

## Must stay

- **`sameDataset` guid get before every destroy** (`apply.go:113`, re-run after a settle at `apply.go:102`). OWN-15 and the planner's check in `apply.go:28`.
  - `TestDestroyNeverHitsAReplacedDataset`'s `swapRunner` swaps the dataset right before this get. Removing it fails that test.
  - Batching all the guids up front, for example with a multi-operand `zfs get guid a b c`, would widen the window for every destroy after the first. Rejected.
- **DeleteVolume's parent snapshot listing** (`endpoint.go:798`). It is the only way to find `<src>@volume-source-for-volume-<id>` orphans cut by a create that died before cloning, and DV;DV == DV idempotency depends on it (`plan.go:684-690`, `TestCloneFromVolumeReleasesInternalSnapshot`, `delete_scope_test.go`). zfs has no name filter, so it cannot be narrowed.
- **The FS create's pass-1 scan** (`endpoint.go:560`, pass 1). `zfs create` and `zfs clone` exit 0 when the mount fails, and the commit must wait for `mounted=yes` (`plan.go:525-530`, `TestPlanCreateCommitsOnlyMounted`). An explicit `zfs mount` instead would be a fork too: no saving.
- **verifyExport's re-scan after a re-share** (`endpoint.go:627`, try 1) and **Publish/Unpublish's export-table re-reads.** These are not zfs calls (etab).
- **The whole-parent scans of CreateSnapshot, ListVolumes and ListSnapshots.** One call each, inherent; only their width can shrink (see above).
- **ADR 0003 ownership checks.** Each proposal leaves `OwnedVolume`, `OwnedSnapshot`, `OwnedCloneSource` and `ServedVolume` exactly where they are. P1 and P5 reuse rows that already passed those checks under the same lock.

## Suggested order

1. P1 + P6, then P2/P3 together with the new missing-parent test. About −12% of endpoint calls, all low risk.
2. P5 + P8. Small, low risk.
3. P9, with a CONTEXT.md "Commit" amendment and a golden update.
4. Add a per-RPC call-budget test (probe-style: assert each RPC's zfs argv sequence on the simulator), so savings cannot regress silently. Nothing pins counts today; only `sets()` and `mutations()` count mutating calls.
5. P7 and P4 only with the new tests named above.
