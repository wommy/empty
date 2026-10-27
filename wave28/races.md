# Wave 28: concurrency audit of the endpoint RPC paths (read-only)

Scratch copy: `/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave28/race`.
Tests: `internal/endpoint/wave28_race_test.go` in that copy.
Run: `go test -race -count=1 -run TestW28 ./internal/endpoint/`.
The existing `internal/lock` and `internal/endpoint` suites pass under `-race` (the TestW28 tests were skipped for that run).

Short version: no correctness race was found. Every mutation of a volume holds `vol:<id>`, waiting acquisitions take their keys in sorted order, and keys added later are only tried. Two resource findings were demonstrated, both about the whole-parent scan slots. Everything else was disproved or found benign.

## Findings

### F1: DeleteVolume's whole-parent snapshot listing takes no scan slot (demonstrated)
- **Where:** `endpoint.go:911` (`scanDelete`: `ListColumns(parent, snapshots, depth 1)`).
- **Interleaving:** N concurrent DeleteVolume calls (a namespace deletion) each list every snapshot under the parent. `maxWholeScans` (`table.go:457`) does not cover these listings. Only the zfs process limit bounds them. Each `Columns` stays alive while the loop scans the source volumes.
- **Consequence:** the memory bound H-06 was added for ("a namespace deletion of N volumes held N copies at once") is bypassed on the very path it names. The copies are smaller (names plus `type`, snapshots only), but they still grow as O(snapshots) per call. It does not affect correctness.
- **Demonstrated:** `TestW28DeleteListingBypassesScanSlots`. With 8 deletes and `zfs.Limit(8)`, 8 whole-parent listings were in flight at once, against `maxWholeScans=2`.
- **Fix:** take an `e.scans` slot around the listing, then copy the matching `<src>` names out and release the slot before the per-source `e.scan`. This is deadlock-safe: nothing that holds a slot ever waits for a lock (see D1).

### F2: lock holders wait for a scan slot while holding `vol:` keys (demonstrated; a priority inversion)
- **Where:** `endpoint.go:1092-1098` (CreateSnapshot holds `snapname`, `vol:<src>` and `snap:` while it runs `scan(ctx, "")`). `reconcile.go:712-721` does the same: it holds every tried `vol:` key while it waits for its locked rescan's slot.
- **Interleaving:** two unlocked ListVolumes or ListSnapshots calls hold both slots during a slow scan. A CreateSnapshot(src) takes `vol:src` and blocks on the slot. ExpandVolume, Publish, DeleteVolume and CreateSnapshot on `src` then return Aborted after `LockWait`.
- **Consequence:** unlocked readers can stall locked operations on unrelated volumes. Nothing deadlocks: slot waits end with the context.
- **Demonstrated:** `TestW28SnapshotSlotWaitHoldsVolLock`. `vol:src` is held while CreateSnapshot waits, and `ExpandVolume(src)` returns "operation pending on [vol:src]" (Aborted).
- **Fix:** do **not** take the slot before the locks. That inverts the order against Reconcile, which holds locks and then needs a slot, and could deadlock. Instead, reserve a slot for lock-holding scans: a separate one-slot channel used by CreateSnapshot and Reconcile's locked rescan. Alternatively, have CreateSnapshot check name uniqueness with a narrow names-only listing of `<prefix>:csi_snapshot_name` on snapshots, instead of a full whole-parent scan.

### F3: DeleteVolume pass 1 plans from a source volume read made before `vol:<src>` is held (demonstrated, benign)
- **Where:** `endpoint.go:933` (`scan(ctx, src)` inside `scanDelete`, under `vol:<id>` only), then `TryAcquire` at `:882`, then `apply` at `:889`.
- **Interleaving:** the scan of src is parked. While it is parked, ExpandVolume(src) and CreateSnapshot(src) complete, because `vol:src` is free (`TestW28DeleteReadsSourceUnlocked`). In `TestW28DeleteOrphanSourceRace`, DeleteVolume(src) runs to completion in the window and src is then re-created. DeleteVolume(gone) then applies its stale plan.
- **Consequence:** none observed. The only operation planned on src is `destroy -d` of `<src>@volume-source-for-volume-<id>`. It is guid-pinned (`sameDataset`), tolerates a missing target (`OkMissing`), and is deferred. Only CreateVolume(id from src) can re-create that snapshot or give it a new clone, and it needs `vol:<id>`, which is held. Both tests pass: the result is OK, nothing is lost, and the re-created src is untouched.
- **Fix:** none needed. If plans that read src ever grow beyond this one guarded destroy, rescan src after the TryAcquire succeeds.

## Disproved or benign
- **D1, deadlock between `vol:`, `snap:`, `snapname:` keys and the scan slots:** disproved by reasoning. Every waiting acquisition is a single `Acquire` call with its keys sorted (`lock.go:35`). Keys added while others are held go through `TryAcquire`, which never waits (DeleteVolume `:882`, Reconcile `:712`). A slot is held only for the duration of one `ListColumns` and decode, and its holder never waits for a key, so no wait cycle exists. The FIFO handoff in `Keyed.unlock` prevents starvation of a waiter. DeleteVolume's re-acquire loop is bounded at 3 passes and ends in Aborted. The existing lock tests pass under `-race`.
- **Reuse of `servedFrom` / `t` (CreateVolume `:648`, ExpandVolume `:981`):** safe. The table comes from the same lock hold, and every writer of a volume's row holds `vol:<id>`: create/clone source, snapshot, delete with TryAcquire on the source, expand, publish/unpublish, and Reconcile.
- **scanForUnproven "parent proven if any volume exists":** sound. The proof and its use are under one lock set. CreateVolume never drops its lock between passes.
- **DeleteVolume of an absent volume proven by the listing (`:907-918`):** sound under the lock. A residual window remains: the pool is imported between the volume scan, which reads not-found because the pool is exported, and the parent listing, which then succeeds. That would turn a present volume into "OK, deleted". This window is not new; the `Exists` call it replaced had the same one. Closing it means re-scanning the volume after the listing, or listing first.
- **CreateSnapshot vs DeleteVolume of its source:** both hold `vol:<src>`, so they are serialized. A snapshot reported cut makes the source undeletable (stress invariant held).
- **ExpandVolume vs CreateSnapshot size recording:** both hold `vol:<src>`. `snapshot_size` comes from the whole scan taken under that lock. In the stress test, every recorded size was a capacity the volume had. Rollbacks (`rollbackExpand`, `restoreShareNFS`) run before the deferred release.
- **CreateSnapshot after the cut (`:1113`):** the rescan and `afterChange` both run under the same locks. No reuse across a release.
- **Reconcile apply vs CreateVolume/DeleteVolume of the same id:** handled. Reconcile never waits for a lock: a busy key gives `SkipBusy`, and the locked re-plan is filtered by (reason, guid) from the unlocked plan. Locks released early (after the last group that needs them) are safe: any later group's dependency on a released volume would need `vol:<still-held volume>` to change it.
- **PublishFileExport vs DeleteVolume:** both hold `vol:<id>`. The reuse of `t` after reshare or apply in Publish (`:1273`, `:1285`) happens within one hold and reads only `Mount`.
- **Headroom (`childSpace`, `headroom.go:237`):** the parent and its level up are read by two non-atomic gets. A concurrent create between them skews the result by at most that one operation's size. This is within the documented advisory window ("racing creates can each pass").
- **Cosmetic: the `snap:` key does not match a hashed snapshot id.** CreateSnapshot locks `snap:<vol>@<name>`, while the dataset may be `<vol>@csi-<hash>` (`snapComponent`), and DeleteSnapshot and CreateVolume lock `snap:<real id>`. This is harmless, because all three also hold `vol:<vol>`. The `snap:` keys are redundant and could mislead later readers.

## Stress
`TestW28Stress` (20 seeds × 8 workers × 30 ops, with `-race`) runs CreateSnapshot, Expand, Delete, re-create, clone, Publish and Reconcile (dry run then apply) concurrently on 3 volumes. Results: no data race, no Internal error, and every reported snapshot still exists with a size its source actually had. This is weak evidence: the operations are serialized per volume, so the stress mainly confirms D1 and the size and delete invariants.
