# Port of the wave7 mutation-killing tests onto HEAD

Branch `fixes-mutation` (local, not pushed), based on `origin/claude/gallant-johnson-1qaxp5` at b5c4ec4. Go: `GOTOOLCHAIN=go1.27.3`.

| commit | what |
|---|---|
| 9520322 | **fix** CreateVolume requires `OwnedVolume` on an existing row; **fix** DeleteVolume of an absent id releases its stamped clone source. Also adds the planner tables that prove both, and a sim crash-window test. |
| 0ce18c6 | test: port the wave7 killing tests as planner and row tables |
| a4b4368 | test: `simulateDestroyAll -r` takes child datasets (kills the successor of #597) |
| cc97b3b (HEAD) | test: kill the HEAD-only survivors found by re-running the sample |

Final checks on HEAD are all clean: `gofmt -l .` (empty), `go vet ./...`, `go mod tidy -diff`, and `go test -race -count=1 ./...`.

## 1. Bugs found and fixed

### Bug A (dangerous survivor #3): CreateVolume on a managed dataset that is not this volume

- **What HEAD did:** `PlanCreateVolume` refused a mismatched name marker only when one was set: `row.VolumeName != "" && row.VolumeName != r.Name`. A row that is `managed_resource=true` with no `csi_volume_name` is not `OwnedVolume` (table.go: "managed without a name is a state neither makes: not ours"). Even so:
  - **committed:** CreateVolume answered "already exists, compatible" (success). The volume was then unusable: GetVolume and Expand returned NotFound, and DeleteVolume returned FailedPrecondition.
  - **uncommitted:** CreateVolume planned size/props ops and **set `provision_success=true` on a dataset that is not ours**. That is a mutation against ADR 0003.
- **Fix** (plan.go, PlanCreateVolume): after the `!row.Managed` check, require `row.OwnedVolume()`. This is the one rule every other path uses. The result is AlreadyExists, with the marker in the message.
- **Proof:** `TestPlanCreateRefusesForeignAndIncompatible` gained the rows `renamed`, `renamed-half`, `nameless` and `nameless-half`, and each must be AlreadyExists with no plan. Before the fix it failed: `nameless` gave `[] <nil>`, and `nameless-half` gave `[set provision_success=true]`.

### Bug B (coordinator, from suite.md): clone-source snapshot leaked by a delete of an absent clone

- **The reported window is already closed at HEAD.** suite.md describes the plan `destroy c; destroy -d src@volume-source-for-volume-c`. That was the older copy. At HEAD (07c3f98), `deletion.plan()` releases the origin first (`-d`), so zfs reaps it with the clone. A crash between the two ops is covered by `TestDeleteCloneInterruptedThenRetriedReleasesOrigin`.
- **The remaining window was real:**
  1. `CreateVolume(c, SourceVolumeID=src)` cuts `src@volume-source-for-volume-c` (stamped).
  2. The process dies, or the clone fails, before `zfs clone`.
  3. The caller gives up and `DeleteVolume(c)` is called.
  4. `c` is absent, so the plan was empty: the result was OK and the snapshot leaked until Reconcile's orphan rule ran, MinAge later.
- **Fix** (plan.go): `PlanDeleteVolume` of an absent id now plans `releaseCloneSourcesFor(t, id)`. For every volume it plans `destroy -d, OkMissing` of `<vol>@volume-source-for-volume-<id>` only when all of these hold:
  - `OwnedCloneSource()` holds with `clone == id` (the stamp, not the name alone);
  - the volume is `OwnedVolume`;
  - the snapshot is not already deferred;
  - it has no clones;
  - the name is a `ValidSnapshot`.

  The release uses `-d`, exactly like the normal clone-delete path.
- **Coverage:** those snapshots hang off any volume, so the absent path calls `t.mustBeWhole(...)`. A partial table is now `Internal`, never "absent". DeleteVolume already scans the whole parent, so nothing changes at runtime.
  - `TestPlannersRequireACoveringTable` was updated. A table read for `b` alone can no longer conclude "b absent, nothing to do": that is Internal, and the whole empty table gives an empty plan.
- **Locking:** DeleteVolume holds `vol:<id>`, the same key a `CreateVolume(id)` that cuts the snapshot holds, so the two are serialized. A concurrent DeleteVolume of the source is harmless, because `-d` is OkMissing.
- **Proof:**
  - **Planner table** `TestPlanDeleteAbsentVolumeReleasesItsCloneSource`, 10 rows plus a partial-table check. The controls are: stamped for c (released); two sources (both released). The negatives are: nothing cut; a bare reserved name; stamped for another clone; a CSI name under the reserved name; on an unmanaged volume; on a renamed volume; in use by a clone; already deferred.
  - **Sim crash test** `TestDeleteOfAbsentCloneReleasesItsCloneSource` (deleteorder_test.go): inject a failure at `clone`, then call DeleteVolume twice. The snapshot is gone, the source volume stays, and both calls are OK. It failed with the fix stubbed out.
  - It uses the always-simulator `newA4` harness, not `h.Sim(t)`.

### Top-10 dangerous survivors: verdict at HEAD

| # | survivor | verdict | evidence / test |
|---|---|---|---|
| 1 | clone from snapshot/volume of a source that is not ours | (a) correct, untested at planner level | HEAD checks `src.ServedVolume()` (owned **and** committed) on both paths (plan.go 252, 276). Rows in `TestPlannerErrorCodes` cover snapshot and volume sources that are unmanaged, renamed, nameless or uncommitted, plus an admin snapshot and a deleted snapshot. Each is NotFound, with owned controls that are OK. |
| 2 | OwnedVolume relies only on local props | (a) correct | Ownership is read only through `own()` (source `local`; `received` is not local, per `TestPropLocalIsOnlyLocal`). HEAD's `OwnedVolume` also requires `VolumeName == ID`, which is stricter than the old copy (no marker is now not owned). `zfs rename` keeps local props, so the name marker is required. `TestRowOwnedVolume` and `TestRowOwnedSnapshot` have one row per conjunct. |
| 3 | CreateVolume on a managed dataset of another name | **(b) REAL BUG** for the no-marker case | Fixed: Bug A above. |
| 4 | OwnedSnapshot conjuncts | (a) | `TestRowOwnedSnapshot` |
| 5 | DeleteSnapshot of a deferred foreign snapshot | (a) correct | Already pending destruction, no op, and nothing of anyone's is touched. Answering OK is right for CSI. `TestPlanDeleteSnapshot` covers: own, own-deferred, admin (FailedPrecondition), admin-deferred (OK, no op), missing, and a malformed id. |
| 6 | MinAge boundary | (a) | Two rows in `TestPlanReconcile`: exactly 3600 s old is collected; 3599 s is SkipTooYoung. |
| 7 | simulation reaps deferred snapshots a pinned origin still holds | (a) correct | `simulateDestroyAll` reaps only `cand && Deferred && clones==0`. `TestSimulateDestroyAllCases` (7 rows) shows that two deferred origins, after one clone is destroyed, leave the other still pinned. |
| 8, 9 | parseColumns malformed / unrequested property | (a) | `TestParseColumns` (8 rows) |
| 10 | classifier aliases | (a) | `TestClassify` gained every remaining phrase, hardcoded and not ranged from `patterns`, plus an upper-case row and an unmatched-text check. |

## 2. Tests added or merged

Every test below is pure (Table in, Plan out) unless noted. New tests use `t.Parallel()`, the `TestPlanReconcile` subtests now run in parallel, and there are no sleeps.

### internal/endpoint/plan_test.go

**Merged into existing tests:**
- `TestPlanCreateRefusesForeignAndIncompatible`: 4 rows for foreign or unnamed rows.
- `TestPlanSnapshotLongAndUnusualNames`: the 128-byte id boundary.

**New table tests:**
- `TestPlannerErrorCodes`: 40 rows, one per refusal code, with OK boundary controls. It covers:
  - the content-source and size validations;
  - clone kind mismatch on both paths;
  - a clone source above the limit and exactly at the limit;
  - every source-ownership case;
  - idempotency mismatches: limit, unlimited, reserve both ways, sparse zvol;
  - delete, expand and snapshot of renamed or nameless volumes;
  - a snapshot name pending deletion;
  - an empty snapshot name.
- `TestPlanCreateConvergesUncommittedSize`: 6 rows. The quota grows and the reservation is never lowered or re-set.
- `TestPlanDeleteSnapshot`: 6 rows.
- `TestPlanDeleteVolumeReleasesOnlyItsOwnOrigin`: 6 rows for releasedOrigin's ownership conjuncts.
- `TestPlanDeleteAbsentVolumeReleasesItsCloneSource`: Bug B.

### Other files in internal/endpoint
- **table_test.go (new):** `TestRowOwnedVolume`, `TestRowOwnedSnapshot`, `TestRowOwnedCloneSource`, `TestRowKindAndCapacity`, `TestTableVolumesSkipsSnapshots`.
- **reconcile_test.go:**
  - `TestPlanReconcile` gained 4 cases: the MinAge boundary ×2, a desired id naming a snapshot, and an uncommitted row aging from creation.
  - `TestPlanReconcileHeldLimitsActions` gained the locked re-plan's reason check.
  - New `TestSimulateDestroyAllCases` (7 rows), replacing the old `simulateDestroy` cases. That function is now test-only; production uses `simulateDestroyAll`.
- **errors_test.go:** `TestZfsErrCodes` gained NotFound, ParentNotFound, Exists and nil rows. New `TestErrorText`.
- **props_test.go:** `TestValidatePropertyValues` gained 6 rows: gzip-9, 512B, logbias and dedup on filesystems, and atime and recordsize refused on zvols. New `TestParseSize` (7 rows).
- **coverage_test.go:** the absent-delete coverage rule (see Bug B).
- **deleteorder_test.go:** `TestDeleteOfAbsentCloneReleasesItsCloneSource` (always-simulator harness).

### internal/zfs
- `zfs_test.go`:
  - `TestClassify` gained 15 rows.
  - New `TestErrorIsMatchesBareSentinelsOnly`.
  - New `TestErrorTextPrefersStderrThenCause`, including the case with neither stderr nor cause.
- `fuzz_test.go`: new `TestParseColumns`.

### Dropped
- The old copy's `"no marker" -> owned` row (HEAD made it not owned).
- `simulateDestroy` index-0 / non-recursive cases, rewritten for `simulateDestroyAll`.
- The old copy's `TestMutCloneFromSnapshotNeedsOwnedSourceVolume` `"actually snap"` row. A volume id cannot contain `@`.

## 3. Kill rates (mutator re-run)

**Tooling:** all in `scratchpad/port/`.
- `mutgen/` is wave7's, with targets adapted to the HEAD layout:
  - reconcile.go by function: `PlanReconcile`, `refuses`, `targets`, `Plan`, `tooYoung`, `hasAny`, `simulateDestroyAll`, `digest`, `undesired`;
  - table.go lines 1-327, up to `scan`.
- It enumerated `mutants.json` against `base/`, a `git archive` of the branch.
- `map_survivors.py` maps each wave7 survivor to the same op, desc, replacement and source text on HEAD.
- `survivor_ids.py` handles the 9 whose code was rewritten, by taking every mutant on the HEAD line that now holds that invariant.
- `run.py` is wave7's runner (`go test ./internal/... -count=1 -failfast`, 4 workers), pointed at `base/`.

**Results files:** `survivors.jsonl`, `sample.jsonl`, `rerun2.jsonl`.

### Previously surviving set (wave7's 71)
- 62 map to an exact successor mutant on HEAD. The other 9 (#123, #127, #187, #568, #579, #593, #597, #602, #624) map to line successors.
- That gives 113 mutants, of which 9 do not compile (dropped `!ok` leaves `ok` unused), so **104 are valid**.
- First run, against commit 0ce18c6: 96 killed, 8 survived.
- After a4b4368: **97/104 killed (93.3% raw). The 7 survivors are all known equivalents, so 100% of the non-equivalent set is killed.**
- In wave7 ids, every one of the 63 non-equivalent survivors is killed.

### Full sample (fresh, same quotas, seed 7, on HEAD's enumeration)
- 303 valid (16 build failures replaced).
- Before the last test commit: 272/303 (89.8%). Most of the new survivors were in code HEAD added after wave7: releasedOrigin, OwnedCloneSource and the locked re-plan.
- After cc97b3b: **286/303 = 94.4% raw; all 17 survivors are equivalent, so 100% of non-equivalent.**

| bucket | killed / valid |
|---|---|
| plan.go | 81/90 |
| props.go | 37/40 |
| reconcile.go | 49/50 |
| table.go | 37/40 |
| zfs/errors.go | 39/40 |
| errors.go | 27/27 |
| parseColumns | 16/16 |

(The wave7 baseline on the old head was 76.6% raw from the existing suite.)

### The 8 known-equivalent mutants (wave7), status on HEAD

| wave7 | HEAD id | why equivalent |
|---|---|---|
| #298 | 306 | `compatible` drops `have == 0` in the limit check. The earlier `want > 0 && (have == 0 \|\| …)` already fires, because with a limit and no capacity, `want` is the limit, and a zvol never has size 0 (decode rejects it). |
| #887 | 1096 | Dropping "pool or dataset is busy" changes nothing: "dataset is busy" is a substring with the same kind. |
| #778 | 987 | `parseSize` `ParseUint` error branch: at most 9 ASCII digits cannot fail. |
| #96 | 108 | `r.Kind == Filesystem && size == 0` loses its Filesystem test. A Block clone with no capacity takes size = source volsize, and `sizeOps` sets volsize only when larger, so the plan is identical. |
| #238 | 246 | `sourceSize`: `s.Type == TypeSnapshot` is always true for a row found by a snapshot id (it contains `@`). |
| #253 | 261 | `sourceSize`'s no-source `return 0, false` → true. The only effect is a reserve sizeErr that `validateCreate` already refused. |
| #259 | 267 | `cloneBlockSize`'s `src.Type != TypeVolume`. The kind equality is checked first, and volblocksize is refused for filesystems by `ValidateProperties`. |
| #597 | 726 | `under()`'s `/` child test. The scan never yields deeper rows and nothing plans `-r` any more. Equivalent in production, but **now killed** by the `-r` children row in `TestSimulateDestroyAllCases`, so it leaves the list. |

### New equivalents found on HEAD (sample)

- **1095:** "pool or dataset is busy" kind changed. It is shadowed, like #887.
- **966 / 973 / 979:** the `parseSize` digit-scan, `i == 0` and ParseUint guards are mutually redundant. Each one alone is caught by another.
- **429 / 434 / 437:** `releasedOrigin`'s `!ok`, `volumeID` and `Covers(src)` guards. The next `t.Get(src)` fails for the same inputs, so they are defence in depth.
- **11:** `want()` `LimitBytes > 0` → `>= 0`. With limit 0 it returns 0, the same value.
- **145:** `row.SourceType != srcType` dropped. Snapshot and volume source ids are disjoint (`@`), so SourceID alone differs.
- **342:** the mutated `row.Refres > 0` is the argument of an error message.
- **591:** `kindType`'s result is ignored by `sizeOps`, which branches on `r.Kind`.
- **660:** an orphan clone source that is deferred but has no clones cannot exist in zfs (it is destroyed at once), and with clones it is skipped as in use.
- **756:** `Capacity` without the zvol test only matters for a zvol with volsize 0, which decode rejects.
- **856:** only which of two scan errors is reported.
- **891:** `committed_at` 0 is 0 either way.

## 4. expected-skips changes

**None.**
- No added test uses `h.Sim(t)`.
- Every new test is pure (planner, row or parser), except `TestDeleteOfAbsentCloneReleasesItsCloneSource`. That one uses `newA4`, which always builds its own `zfstest.Sim` and never skips. It runs, and passes, on every leg.
- `ci/check-skips.sh` rules are unaffected: there is no new SKIP line.
