# Impossible-state reach: zfs get text → parseColumns → Table → planners → apply

Target: HEAD bdf89d0, tested in the copy `scratchpad/wave5/reach-copy` with GOTOOLCHAIN=go1.27.3. The repo was not edited.
Excluded because another builder is fixing them: received counted as owned, clone from unowned, partial table read as full, duplicate-line forgery, missing default in apply, error classification.

**In-flight note.** While this ran, `/home/user/empty` had an uncommitted, conflicted merge staged (fixes-wave3: `OwnedVolume`/`OwnedSnapshot`, `sourceSize`, `OkExists` dropped from `OpClone`, table coverage, foreign-snapshot refusal). From reading that staged diff, F3, F4, F8 and F11 appear to be fixed there, and F7 is partly fixed. I have **not** re-run anything against it, because the tree is mid-merge. Every finding below is reproduced at bdf89d0.

## How to run

```
cd scratchpad/wave5/reach-copy
GOTOOLCHAIN=go1.27.3 go test -tags reach -run Reach -v ./internal/endpoint/                     # directed repros (each FAILS while its bug is present)
GOTOOLCHAIN=go1.27.3 go test -tags reach -run '^$' -fuzz '^FuzzRawScan$' -fuzztime 60s ./internal/endpoint/
REACH_MASK=1 ...   # hides the known findings below, so a fuzz run surfaces only new classes
```

Files (build tag `reach`, so the normal suite is unaffected):
- `internal/endpoint/reach_helpers_test.go`:
  - `rawRunner`: answers every `zfs get` with fixed hostile bytes and records mutations.
  - `hookRunner`: runs an out-of-band writer outside the simulator lock.
  - Cell builders, and `simDump` for comparing end states.
- `internal/endpoint/reach_test.go`: one directed test per finding (R1–R15, R9b).
- `internal/endpoint/reach_fuzz_test.go`:
  - `FuzzRawScan`: arbitrary bytes as `zfs get -Hp` output → scan → every planner.
  - `FuzzHostileScan`: a structured generator over name shapes (space, unicode, trailing `/`, `@a@b`, 255 bytes, `#bm`, child, orphan snapshot, clone-source), type (`bookmark`, `-`, `""`, `Volume`, or one that disagrees with the name), numerics (`-`, `none`, `""`, `-5`, `1G`, 2^64-1, > 2^64), values/sources (`TRUE`, NUL, `temporary`, `LOCAL`, `""`), origins (inside, outside, garbage), dropped columns, and unsorted rows.
  - Invariants checked by both scan fuzzers:
    - INV1: no panic.
    - INV2: every op targets something under the parent.
    - INV3: no op touches an existing unowned row.
    - INV4: `Volumes()` yields only filesystem/volume rows without `@`.
    - INV5: no size `Set` on a row whose size cells were not decimals.
  - `FuzzCreateCrashPrefix`: CreateVolume (fs or zvol; plain, from volume or from snapshot; capacity 0 or set; reserve/sparse) with mutation k failing as a "crash", then the CO's retries. Checks:
    - the end state equals the uninterrupted run (`simDump`);
    - an identical repeat plans nothing.
  - `FuzzReconcileForeign`: FuzzReconcileSafe's world plus clones outside the parent, unowned child datasets and a crash after any prefix. Checks:
    - no unowned dataset is destroyed;
    - reconcile does not error;
    - reconciling a second time plans nothing.
  - The saved failing inputs are in `internal/endpoint/testdata/fuzz/`.

Fuzz results:
- **Unmasked:** every target fails within one second, on the findings below.
- **Masked (`REACH_MASK=1`), 90 s each:** no new class and no panic.
  - Plain fs/zvol creates and zvol clones converge after any crash prefix.
  - Reconcile with crash prefixes converges, and its second pass is empty when nothing outside the scan depends on the volumes.

## Findings (ranked)

| # | Severity | Input | What happens | Where | Fix (layer) |
|---|---|---|---|---|---|
| F1 | **High: data loss** | A managed volume `tank/v/a` with an unowned child `tank/v/a/user-data` (`zfs create`, a recv target, a backup). Real zfs produces this easily. | The scan drops every row deeper than a volume ("can't matter"). PlanDeleteVolume and Reconcile then plan `destroy -r tank/v/a`, which **destroys the unowned child**. Repro: R4. | table.go:230-236 (drop) and plan.go:334 (`Recursive: true`) | **Parse boundary:** keep deeper rows as "dependents" of their volume instead of dropping them. **Consumer:** PlanDeleteVolume refuses a volume with children, as it already does for CSI snapshots, or destroys without `-r` after destroying its own snapshots. |
| F2 | **High: shrink / data loss** | zvol `volsize` of `-`, `none`, `""`, `-1`, `1G`, `1.5G` or > 2^64 (also refquota/refreservation) | `Prop.Uint` maps `-`/`none`/`""` to 0, and `decodeColumns` drops parse errors (`v, _ := ...Uint()`), so every non-decimal reads as **0**. Effects:<br>- **ExpandVolume(1 GiB)** plans `volsize=1073741824` on a zvol of unknown real size. zfs shrinks volsize without `-f`.<br>- **Uncommitted resume** plans `volsize=want`.<br>- **Filesystems:** refquota "0" means unlimited, so a resume sets `refquota=want` below the real quota.<br>Repro: R1 (ExpandVolume and the resume path, both for all 7 values); INV5. | zfs.go:35-40, table.go:158 | **Parse boundary:** strict numerics. A column that applies to the row's type must be a decimal, or the scan fails (`-` is allowed only where it is the real answer, e.g. volsize on a filesystem). Never default a size to 0. |
| F3 | **High: contract** | Filesystem clone (from a volume or a snapshot) with capacity 0, and a crash after `zfs clone` and before the size `Set` | The fresh plan copies the source refquota (and, with Reserve, its refreservation), but the resume branch uses `want`=0. The retried create commits with **no quota**. With Reserve, every identical later CreateVolume returns **AlreadyExists** ("reserve=false"). **Without any crash**, clone-from-*snapshot* with Reserve and capacity 0 never reserves, so its identical repeat is already AlreadyExists. Repro: R2, FuzzCreateCrashPrefix shape 0x26. | plan.go:144 vs 195-199 and 226-228 | Consumer: derive the target size before the resume branch. The staged fixes-wave3 `sourceSize` looks like this fix. Re-run R2 against it. |
| F4 | **High: TOCTOU** | Out-of-band `zfs create tank/v/c` (unowned, refquota 5 GiB) between the scan and `zfs clone` | `OpClone{OkExists:true}` reads EEXIST as "our clone exists". Apply then **sets refquota=1 MiB (a shrink) and provision_success=true on the foreign dataset** and returns success. `CreateFS`/`CreateVol` correctly have no OkExists; clone was the odd one out. Repro: R3. | plan.go:193, 223; apply.go:272 | Consumer: drop OkExists on clone. A replay re-plans from the scan and takes the resume branch anyway. Staged fixes-wave3 seems to do this. |
| F5 | **Medium: liveness (GC wedge)** | A clone of a volume's snapshot outside the parent (`zfs clone tank/v/a@manual tank/backup/x`), or a volume renamed out of the tree | ClonesOf sees only the scanned tree, so `destroy -r` is planned. zfs refuses ("has dependent clones"), and `apply` aborts the whole reconcile at that op, **on every run**. Undesired volumes sorted after it are never collected, and the skipped list is empty. Repro: R5, FuzzReconcileForeign. | reconcile.go:156 and 352; apply.go:276; table.go `ClonesOf` | **Parse boundary:** scan the snapshots' `clones` property so dependents outside the tree are visible. **Consumer:** Reconcile applies per action and turns a failed action into a Skip instead of aborting. |
| F6 | **Medium: wrong target** | A managed volume whose `origin` is a clone-source snapshot outside the parent (source renamed to `tank/archive/s`, or adopted from another parent) | PlanDeleteVolume releases *any* origin that contains `@volume-source-for-volume-`. The result is `destroy -d tank/archive/s@…`, **an op outside the endpoint's tree**. Reconcile also takes lock key `vol:tank/archive/s`, which no request ever takes. The suffix is never checked against this volume's id. Repro: R6, FuzzRawScan INV2. | plan.go:335-337; reconcile.go:164-168 | Consumer: release only `parent/<src>@volume-source-for-volume-<id>`, where `<id>` is this row and `<src>` passes volumeID. |
| F7 | **Medium: silent misclassification** | `type` disagrees with the name: `a@s` typed filesystem; `b` typed snapshot; `bookmark`, `-`, `""`, `Volume`; or the `type` line missing (dropped column) | `Volumes()` trusts Type, while `snapsOfVol` and `ID` trust the name. Effects:<br>- **ListVolumes** publishes `a@s`, `c`(bookmark) and `d`(`-`) as filesystem volumes.<br>- **CreateVolume(b)** adopts a snapshot-typed row as a committed filesystem and returns it.<br>- **`Kind()`** treats every unknown type as Filesystem.<br>Repro: R7, INV4. | table.go:52-64, 105-113, 168-170 | **Parse boundary:** accept only the 3 requested types, require `@` exactly when the type is snapshot, and require a `type` cell for each row. Staged `OwnedVolume` covers only the "typed snapshot" case. |
| F8 | Medium | A zvol snapshot `a@s` whose volume row is missing (a concurrent destroy or rename while `zfs get` iterates) | The source-kind check consults only the volume row (`if src, ok := …; ok && …`). A Filesystem request clones a zvol, the refquota Set then fails, and an uncommitted zvol is orphaned. A retry gets AlreadyExists, not InvalidArgument. Repro: R8. | plan.go:185-188 | Consumer: compare `snap.Kind()` or require the source row. Staged fixes-wave3 requires `srcOK`. |
| F9 | Medium: liveness | Names zfs accepts but `ValidDataset`/`ValidSnapshot` reject: a space, a leading `_` or `.`, or the clone-source suffix `x y` | Volumes like these fail closed: PlanDeleteVolume's `volumeID` refuses them and they are skipped (R9 passes). But the **clone-source sweep and the origin release skip that validation**. The client refuses the target, and every reconcile aborts. Repro: R9b. | reconcile.go:198-228; plan.go:335 | **Parse boundary:** mark rows with unaddressable names (or drop them with a skip reason). **Consumer:** per-action apply (as in F5). |
| F10 | Medium | A capacity in (2^64-bs, 2^64) through the endpoint gRPC API (`uint64`; only the CSI front end refuses negatives) | `roundTo` wraps to 0. ExpandVolume(MaxUint64) returns **success at 1 MiB**. A Block CreateVolume(MaxUint64-1) from a snapshot commits at the snapshot size. A plain create fails closed only because the client refuses size 0. Repro: R11. | plan.go:71-79; server.go:109, 128 | **API boundary:** refuse capacity/limit above MaxInt64. **Consumer:** overflow-checked roundTo that returns OutOfRange. |
| F11 | Low/Med: inconsistency | A snapshot with a local `csi_snapshot_name` but no `managed` (e.g. `zfs inherit managed_resource`) | CreateSnapshot returns success for the same name, but ListSnapshots hides the snapshot and DeleteSnapshot is FailedPrecondition. The assumption that "SnapshotName set implies Managed" is false. Repro: R10. | plan.go:397-403 | Consumer: require ownership in the match. Staged `OwnedSnapshot` does this. |
| F12 | Low | Uncommitted managed row | GetVolume, ExpandVolume and Publish serve the row (`Managed` only), while ListVolumes hides it and Reconcile collects it as abandoned. Managed and Committed are treated as independent where they should be ordered. Repro: R15. | endpoint.go:410; plan.go:348; export.go:264 | Consumer: serve committed rows only. Resume stays CreateVolume's job. |
| F13 | Low | etab line `path\thost(` or `host()` | `mode` becomes `""`, which is read as **rw**. A failed export can verify as served read-write. Repro: R13. | export.go:192-194 | Parse boundary (ParseEtab): skip lines without `ro`/`rw`, or treat them as not served. |
| F14 | Low | Hand-edited `nfs_clients` with `a=ro,a=rw` or `a=rw,a=rw` | Accepted as canonical. ShareNFS gives `rw=a,ro=a` or `rw=a:a`, and Publish/Unpublish then act on the first match only. The canonical check can flip, because `SortFunc` is unstable for equal hosts. Repro: R14. | export.go:117-137 | Parse boundary (`decodeClients`): reject duplicate hosts. |
| F15 | Low: latent panic | `Planner{}` built without VolBlockSize (exported type and fields) | Integer divide by zero in `roundTo` on any Block request. `New` always sets it, so this is reachable only from outside callers. Repro: R12. | plan.go:75 | Consumer: guard `bs==0`, or make the Planner fields unexported. |
| F16 | Low: TOCTOU, design | Out-of-band destroy+recreate (or rename) of a planned target between scan and apply | Every op addresses its target by name. `destroy -r` and `set` hit whatever holds the name at apply time. The in-process locks and per-parent flock don't cover other zfs writers. | apply.go | Fetch `guid` in the scan and run destructive ops as a channel program that checks guid and the local `managed` prop atomically. This is different from the dropped fencing use. |

### Assumptions that held (fail-closed)

- **Ownership markers:** empty, `-`, `TRUE`, NUL-suffixed or unknown-source values read as not owned. A committed row without `managed` is AlreadyExists/FailedPrecondition and is skipped by Reconcile.
- **Creation:** `-`, negative or garbage creation gives `SkipNoCreation`; a creation in the future is SkipTooYoung.
- **Names:** trailing `/`, `tank/v/`, and the parent's own snapshots are dropped. `@a@b` is refused by `parseSnapshotID` (DeleteSnapshot is a no-op). Over-long and space names are refused by `volumeID` on the volume paths.
- **Ordering:** rows out of order are fine (`NewTable` sorts, and parseColumns is order-independent).
- **Same name, different types:** this is a duplicate `type` line, caught by the parser.
- **Out-of-range index:** `Columns.Col` returning -1 is unreachable, because every asked-for column is in Props. Note that `Cell(i,-1)` would silently read the previous row's last cell rather than panic, except at i=0.
- **Crash prefixes:** plain filesystem and zvol creates, zvol clones and reconcile (without out-of-tree dependents) converge after any crash prefix. Reconcile-twice is empty.
- **By design, not counted:** DeleteVolume crashing between `destroy -r` and the origin release leaks the internal snapshot until Reconcile sweeps it.
