# zfs-endpoint core: a data-oriented, typed rewrite (wave8)

**Prototype:** `/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave8/core`

- It is a copy of HEAD **8cc52ef**, which includes the reconcile and lifecycle merges.
  - Relative to 8cc52ef it adds exactly two things: `internal/core/**` and `internal/endpoint/zz_wave8_bench_test.go`. It edits no existing file (`diff -rq` against `git archive 8cc52ef`).
  - adversary.md D1 saw `endpoint.go`, `table.go`, `export.go` and `sim.go` differ. That was HEAD moving from d45b457 to 8cc52ef underneath the old copy, not edits by this branch. The old copy is kept at `wave8/core-d45b457`.
- Toolchain: `GOTOOLCHAIN=go1.27.3`.
- `go vet ./internal/core/...` and `go test ./internal/core/...` pass.

**Scope.** These are built:
- the parse boundary;
- the immutable World with an ownership enum;
- handles;
- per-kind ops;
- `decide` for CreateVolume and DeleteVolume;
- the FSM table;
- the error table;
- Apply;
- a pure zfs model for tests.

Not built: CreateSnapshot, DeleteSnapshot, Expand, List, Reconcile, Export.

## 1. Shape

```
lock (lease) -> zfs get -j -p (scope) -> Parse -> *World (immutable, typed, owned-or-not decided once)
             -> Bind(world, lease, request) -> decide.X(Bound) -> op.Plan | Refusal
             -> Apply(plan): guid guard + argv + error table -> re-read
```

- Only package `world` can mint a handle, and only `op` constructors turn handles into ops. `decide` is pure: it does no I/O, reads no clock and holds no locks.
- Apply holds the only judgement made at run time: the error table, plus the guid guard.
- The World is the zfs source only. Other sources (etab, configfs, the clock) stay per-edge, as dod3/future.md argued.

## 2. Core types (the ≤15-line version)

```go
type VolID struct{ s string }; type Dataset struct{ s string }; type Snapshot struct{ s string; at int } // parsed once, valid by construction
type World struct{ parent Dataset; scope Scope; lease Lease; vols []Vol; snaps []Snap; local []Local }        // split per kind, sorted
type Vol  struct{ id Dataset; origin, srcID, volName string; guid, size, refres uint64; creation int64; snapLo, snapHi, bs uint32; children uint16; kind VolKind; own VolOwn; src SrcKind } // 120 B
type Snap struct{ comp, volID, csiName string; guid, volsize uint64; creation int64; vol, clones uint32; own SnapOwn; deferred bool }               // 72 B
type VolOwn  uint8 // Foreign | Invalid | Uncommitted | Committed            (derived once, in Parse)
type SnapOwn uint8 // Foreign | Invalid | CSI | CSIDeferred | CloneSource    (CloneSource = stamped clone_source_for, never the name)
type State   uint8 // Absent | Foreign | Invalid | Uncommitted | Committed   (FSM row key)
type Lease struct{ n uint64 }; func Bind[R Request](w *World, l Lease, r R) (Bound[R], error) // ErrScope | ErrStale
// handles (unexported fields, minted only by world): FreeVol OwnedVol UncommittedVol CommittedVol RestoreSource CloneSource ReleasableOrigin DoomedSnap DeferrableSnap
type Op interface{ Kind() Kind; Tol() Tol; Target() string; Argv() []string; sealed() } // CreateFS CreateZvol CutCloneSource Clone Set Commit ReleaseOrigin DestroySnap DestroyVol DeferDestroySnap
func CreateVolume(Bound[req.CreateVolume]) (op.Plan, *code.Refusal);  func DeleteVolume(Bound[req.DeleteVolume], Policy) (op.Plan, *code.Refusal)
var Table [NStates][NReqKinds]Cell  // Cell{Refuse, Steps, To, Mid, Errs}: decisions conform at run time (check), tests generated from it
var ErrTable [NClass][NZKind][2]Outcome // (op class, zfs error kind, mutated?) -> Done | Code, expanded from 15 Rules
type Bytes uint64; type BlockSize struct{ n uint32 } // request types: no overflow on round-up; zero value = 16 KiB
```

## 3. The parse boundary (`world/parse.go`, `world/scope.go`)

- **Input.** `zfs get -j -p -t filesystem,volume,snapshot -d 2|1 <props> <root>`. There is one document per scope root: the whole parent, or one per volume.
- **Properties.**
  - The narrow scan reads 17 columns: 9 natives plus 8 markers.
  - The wide scan reads 28: it adds the 11 allowlisted local properties.
  - The markers are the 7 baseline ones plus `clone_source_for` (new at 8cc52ef).
  - `committed_at` is not read yet; see §11.
- **The decoder** is a strict `jsontext` token stream:
  - `output_version` must say 0.1 and `"zfs get"`;
  - every requested property must appear exactly once, and no other;
  - `{value, source{type, data}}` with the 6 source types;
  - the header fields must agree with the key;
  - truncated, trailing or duplicate documents are refused.

  Retained strings live in an arena via `unsafe.String`. Ids and snapshot components are substrings of the arena-held key.
- **Invariants checked once per row** (wave5/zfs-invariants.md I1–I10):
  1. type and delimiter agree;
  2. the name grammar;
  3. the type-gated `-`;
  4. a readonly property has source NONE;
  5. a PROP_DEFAULT property is never inherited;
  6. the quota family is 0 iff DEFAULT;
  7. zvol geometry;
  8. origin only on fs/vol, and it must name a snapshot;
  9. refres ≤ refquota;
  10. the user-property source shape.

  Cross-row checks: a snapshot's volsize is present iff its head is a zvol, and a snapshot must have its dataset.
- **Errors.** Every violation is a `*ParseError{Dataset, Rule}`. There is no partial World: a pool that prints something zfs never prints is a refusal, not a guess.
- **Ownership is derived once, here.**
  - `classifyVol`:
    - a bad marker shape → `VolInvalid`;
    - `csi_volume_name` ≠ id (including a literal "-") → `VolForeign`;
    - a received marker → `VolForeign`;
    - committed or uncommitted from `provision_success`.
  - `classifySnapMarkers`:
    - a marker without `managed_resource` → Invalid;
    - no `managed_resource` at all → **Foreign, including a bare reserved name** (OWN-13);
    - a reserved name is `CloneSource` only with `managed_resource=true` plus `clone_source_for=<the VolID suffix>`, set locally. A half stamp, or a stamp for another clone, is Invalid;
    - `csi_snapshot_source_volume_id` ≠ volume → Foreign;
    - deferred → CSIDeferred.

    `build` downgrades every snapshot of a non-owned volume to Foreign.
- **Bitset bound (A7).** The per-dataset seen-set is a `uint64`. `ParseUnder` panics if a schema ever exceeds 64 properties, and `TestWidestSchemaFits` pins the widest schema at 28.

**Bug found by the benchmark, now fixed.**
- What happened: `member()` returned the property name as a slice of the decoder's buffer. In `source()`, that slice was used after the next `ReadValue`.
- Every small fixture passed. The 22 MiB synthetic document failed when the decoder refilled its buffer mid-object: "unknown member `endp`".
- Fix: names are copied into `p.kbuf`, and error messages use the schema's name. `TestCoreRetained`, which parses both synthetic sizes, now guards it in the non-short run.
- Lesson for the migration: one full-size fixture must be in the per-PR tier.

## 4. Layout: split per-kind slices vs kind-tagged rows (measured)

**Shapes.**

| | split (`World`) | tagged (one fat row per dataset, HEAD's shape, typed) |
|---|---|---|
| row size | `Vol` 120 B, `Snap` 72 B | `tagRow` 312 B (HEAD `Row` 432 B) |
| snapshots of a volume | `snaps[v.snapLo:v.snapHi]` (contiguous) | `map[vol][]int` index |
| retained, 10k / 110k | **2.2 / 22.2 MiB** | HEAD Table: 31.4 / 345.3 MiB |

**Measurements** (`BenchmarkLayout`): same decoded records, min of 3 runs, shared 4-CPU host.

| | 10k split | 10k tagged | 110k split | 110k tagged |
|---|---|---|---|---|
| build (sort + index) | 5.5 ms, 6 allocs | 39.5 ms, 14,024 allocs | 64 ms, 6 allocs | 338 ms, 150,083 allocs |
| judge every volume's snapshots | 5.4 µs | 276 µs | 146 µs | 10.1 ms |
| list committed volumes | 3.1 µs | 15.6 µs | 36.7 µs | 778 µs |
| list CSI snapshots | 25.7 µs | 23.6 µs | 666 µs | 2.09 ms |

**Verdict: split.** It is 7–50× faster on every per-volume query and builds in 6 allocations. The tagged build includes 9k/100k name concatenations, so read the build gap as an upper bound. The query gap is pure layout.

**Where tagged ties.** Flat "all snapshots" scans tie, since both are linear.

**Agreement with impossible-types.md.** It found the same: split builds in 4.9–5.3 ms with 32 allocs, vs 7.6–9.2 ms with 5,025.

## 5. Handles, ops, and the race defences

**How handles are minted.**
- **Every handle is minted from a `*World`.**
  - `w.Free` checks coverage, absence and the name length.
  - `w.Owned`, `w.Uncommitted` and `w.Committed` check state.
  - `w.Restore(SnapID)` requires a committed volume and a SnapCSI snapshot.
  - `w.CloneSourceFor` returns ErrCloneSourceTaken for someone else's snapshot (AlreadyExists, as HEAD) and ErrCloneSourceDeferred for our own released one that is still pinned (Unavailable; A3).
  - `OwnedVol.ReleasableOrigin()` returns a verdict: Keep, Release, or Uncovered.
  - `SnapView.Doom(foreign)` excludes CSI snapshots, cloned or deferred ones, and Invalid ones.
  - `SnapView.Deferrable()` accepts CSI or CloneSource only.
- **Construction is guarded twice.**
  - `TestNoHandleLiterals` bans `world.X{}` and `op.X{}` literals outside their packages (an AST walk).
  - Op constructors panic on zero handles (`must`).
- **Lease (B2).**
  - `ParseUnder(lease, ...)` stamps the world, and `Bind(w, lease, r)` requires the same non-zero lease (`ErrStale`).
  - A world from lock-free `Parse` (List, single-flight) can never bind, so it can never lead to an op.
  - What the lease proves: the world was scanned after this acquisition. It does not prove that the pool is unchanged; §5's table covers that.

**Ops: golden rows** (`TestGoldenPlans`, plus the AST guard). These argv and tolerance details are the real time-of-check/time-of-use (TOCTOU) defences. The types are not (B1).

| op | argv | Tol | defence |
|---|---|---|---|
| CutCloneSource | `snapshot -o <p>:clone_source_for=<id> -o <p>:managed_resource=true <src>@volume-source-for-volume-<id>` | None | stamp set atomically with creation (OWN-13); Exists → **Aborted**, never final (A3, ERR-10) |
| Clone | `clone -o ... <origin> <target>` | **None** | a racing dataset at the target is AlreadyExists, with no follow-up ops (OWN-08, C5) |
| DestroyVol | `destroy <vol>` (never `-r`) | Missing | a racing snapshot or child makes zfs refuse (CX2/F1, `TestRacingSnapshotStopsTheDestroy`) |
| DestroyVol and DestroySnap | guarded by guid | | Apply runs `Runner.Guid(ds)` immediately before; a different guid → Aborted (OWN-15, `TestRenameAndRecreateIsNotDestroyed`); a probe failure other than NotFound stops the plan (`TestGuidProbeFailureStops`) |
| DestroySnap | `destroy <snap>` (no `-d`) | Missing | only `decide.deleteVolume` may call `op.NewDestroySnap` (`TestDestroySnapOnlyInDeleteVolume`, SNP-06) |
| DeferDestroySnap | `destroy -d <snap>` | Missing | for DeleteSnapshot and Reconcile; takes `DeferrableSnap` (CSI or CloneSource), a different type from `DoomedSnap` (A2) |
| ReleaseOrigin | `destroy -d <src>@volume-source-for-volume-<id>` | Missing | stamped clone source only, and never one already deferred |

**Residual guid window.** One zfs exec remains between the guid read and the destroy. Closing it fully needs a channel program: `zfs program` doing `if zfs.get_prop(ds,"guid")==g then zfs.sync.destroy(ds)`. That is a later step, and needs channel programs enabled.

**Other residuals (not closable; document them).**
- C3, a hold race: zfs refuses, and the code is retryable after the mutation.
- C4, an admin sharenfs edit.

## 6. Lifecycle FSM as data (`decide/table.go`)

`Table[State][ReqKind]Cell`. The request kinds are CreatePlain, CreateFromSnapshot, CreateFromVolume and Delete.

| state \ req | Create* | Delete |
|---|---|---|
| Absent | Steps per kind (create / restore / cut+clone, opt Set, Commit); To Committed; Mid {Absent, Uncommitted}; Errs {AlreadyExists, NotFound, InvalidArgument, OutOfRange, Unavailable} | no ops, OK |
| Foreign | refuse AlreadyExists | refuse FailedPrecondition |
| Invalid | refuse AlreadyExists | refuse FailedPrecondition |
| Uncommitted | resume [opt Set, opt Set, Commit]; To Committed; Mid {Uncommitted} | [opt ReleaseOrigin, many DestroySnap, DestroyVol]; To Absent; Mid {Uncommitted}; Errs {FailedPrecondition} |
| Committed | no ops (idempotent), Errs {AlreadyExists} | as Uncommitted, Mid {Committed} |

**Conformance at run time.** `check()` makes every decision conform to its cell. The op kinds must match the cell's step pattern, and the refusal must be in `Errs`; otherwise the result is Internal.

**Tests generated from the table:**
- `TestTableIsTotal`;
- `TestCreateCells`: 2,205 decisions over 5 states × 21 request variants × fixtures;
- `TestCrashConvergence`: 139 crash/retry runs, cut after every op and kill-mid-set subsets. Each run checks that the state after the cut is in `Mid` and that the retry's dump equals the uncut one;
- `TestDeleteCells`: 384 delete crash/retry runs, with extras for sanoid, child dataset, clone outside the parent, stamped orphan clone source, **unstamped reserved name**, **clone source stamped for another clone**, and an inconsistent snapshot.

**Crash convergence is a table property:** for each cell, for each k, cut after op k, then the retry converges to `To`.

**Delete order R1** (lifecycle-fsm.md): release the origin (`-d`), then the judged snapshots, then `destroy` without `-r`.

**Delete scope.**
- The scan covers the volume's own subtree. Clones anywhere are visible through the `clones` property.
- A volume-to-volume clone's origin lives on another volume, so `decide.DeleteScope` widens the request to cover it. That costs one extra `zfs get`, only for such clones.
- An uncovered origin reaching `decide` is a shell bug, and returns Internal.
- This replaces HEAD's full-parent scan for DeleteVolume.

## 7. Error mapping as data (`decide/errmap.go`)

**Shape.**
- Classes: CCreate (create-fs, create-zvol), **CCut** (cut-clone-source, new), CClone, CSet (set, commit), CDestroy (release-origin, destroy-snap, destroy-vol, defer-destroy-snap).
- Kinds: `zfs.Kind` plus a ZDeadline pseudo-kind.
- `ErrTable` is expanded once from the 15 first-match rules below.
- `TestErrTableIsTotal` checks the table.

**Rules** (first match wins):

| class | zfs error kind | outcome |
|---|---|---|
| destroy | NotFound | done (replay) |
| destroy | Busy, HasChildren, HasClones | FailedPrecondition |
| clone | NotFound | NotFound |
| cut | Exists | Aborted |
| create, clone | Exists | AlreadyExists |
| any | ParentNotFound, Unavailable | Unavailable |
| set | NotFound | Aborted |
| any | Busy | Unavailable |
| any | NoSpace | ResourceExhausted |
| any | InvalidProperty | InvalidArgument |
| any | Permission, Precondition, ... | FailedPrecondition |
| any | deadline | DeadlineExceeded |
| any | Interrupted | Aborted |
| any | Unknown, before any mutation | Internal |
| any | Unknown, after a mutation | Aborted (CX6) |

**One mapping, not two (D3).** Migration step 0 (§14) moves HEAD's `errors.go`/`opcodes` mapping into this table, and HEAD's `CodeOf` reads it. Until then, a `TestErrTableMatchesHead` (not yet written; it is part of step 0) must pin cell-for-cell equality with HEAD for every (op, kind, mutated). There is never a second mapping in production.

Known intended differences, each gated by a spec row:
- cut + Exists: Aborted here, AlreadyExists or OkExists in HEAD;
- delete + guid mismatch: Aborted, which is new.

## 8. Requests (`req/req.go`)

- **CreateVolume.**
  - `Bytes`: the round-up cannot overflow, which makes F10 unrepresentable.
  - `BlockSize`: the zero value is 16 KiB.
  - The source is a sum type, so two sources are unrepresentable.
  - The parser rejects: sparse on a filesystem; reserve on a block volume; a block volume with capacity 0 and no source; reserve with no size.
  - An unparseable source → NotFound; over the limit → OutOfRange.
  - It reuses `endpoint.ValidateProperties`. That import moves into core at migration step 3.
- **DeleteVolume.**
  - An empty id → InvalidArgument.
  - An id we could never issue → OK, already deleted (D14).
  - `Origin` is set only by `DeleteScope`.
- **Not built.** The CIDR floor for export clients belongs to the Export port (NFS-08 says hostnames must be refused). It is not built.

## 9. Where this subsumes, and where it disagrees

**wave5/impossible-types.md**

| theirs | here |
|---|---|
| `Own` enum classified in decode | `VolOwn` / `SnapOwn` / `State`, classified in `Parse` (same idea) |
| `FullTable` | replaced by `Scope` + `Bind`: coverage is a property of the World, and `Bind` refuses a request it does not cover |
| decode rejects rows zfs never prints | kept as is: 31 tamper rows + control in `TestParseRejectsWhatZfsNeverPrints` |
| AST construction guard | kept, extended (op literals, the `NewDestroySnap` call site) |
| layout numbers | reproduced (§4) |
| "don't build" list | honoured: no generic visitor, no ORM-like row type, no reflection |

Disagreements:
1. **Real package boundaries.** world and op have unexported fields, not only an AST guard. A guard catches literals, but packages also stop field writes.
2. **Per-kind op structs** (they rejected these). Each struct renders its own argv and owns its `Tol`, and only this lets the golden rows in §5 be per-kind facts.
3. **Invalid is distinct from Foreign** (they map both to Foreign). `-destroy-foreign-snapshots` can then never destroy a half-ours row.
4. **A subtree scan for Delete via `clones`**, plus `DeleteScope`, instead of FullTable.

**wave5/lifecycle-fsm.md**

| theirs | here |
|---|---|
| the FSM with 13 impossible combinations | the impossible combinations are unrepresentable: `State` is one enum per volume, derived once |
| depth-4 explorer | replaced by `TestCreateCells` / `TestCrashConvergence`, enumerated per cell rather than as random paths. A depth-4 multi-RPC explorer is still worth keeping for cross-volume interleavings (clone, then delete the source) |
| R1 reorder | adopted |

**HEAD 8cc52ef**
- HEAD releases a *legacy unstamped* origin of the exact name. The core does not: the coordinator's OWN-13 directive is "the stamp only".
- `TestLegacyUnstampedOriginReleasedWithItsClone` is therefore the one test marked **rewrite**: invert it, or put it in `knownOpen` with an expiry plus an explicit adoption mode.

**HEAD's committed_at**
- The core commit does not stamp `committed_at` yet: that needs `now` in the request, since decide reads no clock.
- Plan equality will differ by that one KV until step 2 adds `req.Now`.

## 10. Adversary findings (adversary.md): disposition

| # | finding | disposition |
|---|---|---|
| 1 (D1) | strangler, not big bang | the branch only adds files over 8cc52ef. §14 moves one RPC at a time behind `-core=`, with plan equality per spec row on the **same scan bytes**, and never shadow-applies |
| 2 (A1) | name ≠ ownership | `SnapCloneSource` requires the `clone_source_for` stamp. A bare name is Foreign; half-stamped or stamped-for-another is Invalid. `CutCloneSource` stamps with `snapshot -o`. `ReleasableOrigin` requires the stamp **and** an owned, covered source volume. Rows: `TestSnapshotOwnership` (4 shapes), `TestCloneSourceIsOursOnlyByStamp`, two `TestDeleteCells` extras |
| 3 (B1) | golden race rows, guid | `TestGoldenPlans` (argv and Tol for the cut, clone, release, destroy-snap and destroy-vol), `TestRacingSnapshotStopsTheDestroy`, `TestRenameAndRecreateIsNotDestroyed` (guid guard), `TestGuidProbeFailureStops`. Residual window documented (§5) |
| 4 (A2) | no `-d` reuse | `DeferDestroySnap` and `DeferrableSnap` are separate types. An AST test pins `NewDestroySnap` to `decide.deleteVolume` |
| 5 (A7) | uint64 bitset | bound + panic + `TestWidestSchemaFits` |
| 6 (B2) | freshness | `Lease`: `ParseUnder` + `Bind(w, lease, r)` → `ErrStale`; `TestBindNeedsTheScanLease` |
| 7 (D3) | one error mapping | §7: step 0 makes it a single table |
| 8 (A3) | `ErrExists` on cut; deferred reuse | CCut+Exists → Aborted (`decide.Map` row); a deferred clone source is never minted (Unavailable). Scenario `TestDeferredCloneSourceIsNotReused`: clone, pin, delete, re-create |
| A4/A8/C3 | contested HEAD behaviour frozen | the core implements D07 (an unlimited fs satisfies `required`, `compatible()`) against the model in wave8/model-copy. Port from spec.json rows, not test files; `knownOpen` entries carry an expiry |
| A5/C2 | sim-only facts | the core's `coretest.Model` is a fourth copy of dsl_destroy beliefs. It must pass the same `realfacts` conformance table as the Sim (§13 limits). DEL-03 is modelled (clone outside the parent). VOL-11 is not (refreservation=auto is a string here) |
| A6 | received passes for the wrong reason | `TestOwnershipIsDerivedOnce` runs every source type through the real decoder. HEAD's `TestReceivedCopyIsNotOwned` is **kept** |
| E1/E2 | "fewer tests" unmeasured | §11 reports counts, but the gate is the protocol: same-operator mutants on `internal/core/**`, a per-row killer set, no row losing its last killer, and a pool sample |

## 11. Test burden: every HEAD test classified

**Baseline.** At 8cc52ef there are 40 test files, 6,862 file LOC, and 161 Test/Fuzz/Benchmark funcs with 4,897 LOC in their bodies.

**Classification.**
- Per function, with the replacement or reason: `wave8/tools/classified.tsv`.
- Script: `wave8/tools/classify.py`.

| verdict | funcs | body LOC | what |
|---|---|---|---|
| **delete**: impossible to violate | 5 | 77 | `TestApplyRefusesAnUnknownOpKind` (sealed Op), `TestOpKindString`, `TestScanRecordsCoverage` (Scope set by Parse), `TestReachCapacityWrap` (`req.Bytes`), `TestReachPlannerZeroBlockSize` (`BlockSize` zero value) |
| **replace**: generated from the table, or a row | 45 | 1,045 | plan_test Create*/Delete, clonesize, converge (FuzzCreateConverges → exhaustive crash table), deleteorder, foreign, opcodes, errors, dash, decode, coverage(1), ownership(3), endpoint_test Create/Delete/Clone (9), reach(4), adv4 clone/origin rows (2), prefix(1), zfs `FuzzParseColumns` + forged-lines (they go with the `-Hp` parser) |
| **rewrite** | 1 | 14 | `TestLegacyUnstampedOriginReleasedWithItsClone` (§9) |
| **keep** | 110 | 3,761 | everything outside create/delete/parse: snapshots, expand, pagination, props validation (PRP), reconcile (incl. FuzzReconcileSafe), export (all NFS), lock, daemon, server, unixsock, runner, csi, cmd, sim facts, `TestReceivedCopyIsNotOwned` |

**After** (CreateVolume, DeleteVolume and parse migrated):

| | before (that slice) | after |
|---|---|---|
| funcs | 51 | 22 core tests + 1 rewrite |
| body LOC | 1,136 | ~690 |
| what runs | hand-picked cases | 2,205 create decisions + 139 create crash runs + 384 delete crash runs + 34 regression rows + ~32 tamper rows + 7 adversary scenarios |

- Whole suite: **161 → 133 funcs; body LOC 4,897 → ≈4,464; file LOC 6,862 → ≈6,630.**
  - Core test files total 1,697 LOC including benchmarks.
  - Without the benchmarks and layout hooks: 1,361.
- The line-count drop is modest. The claim worth making is coverage per line, which is generated rather than sampled.
- Per adversary E1 the gate is the mutation protocol, not these numbers.
- Every remaining endpoint test file shrinks as its RPC moves (snapshots, expand, reconcile, export are each a later step).

**Regression corpus** (`decide/regress_test.go`, 34 rows by mechanism):
- type 13;
- parse 9;
- table 6;
- out of scope 3;
- rule 3.

It ports CHECKPOINT F1–F16, CX1/2/5/6, D01/02/04/05/07/09/12/14/17, I1–3/13/14, R7/R8, Q1/Q2, O1–3, T1, E1 and N1.

**New finding N1** (confirmed on HEAD earlier):
- Resuming an uncommitted *thick* zvol with a *sparse* request committed it.
- Core: the resume refuses thick vs thin and capacity > limit (AlreadyExists).

## 12. Benchmarks (synthetic JSON from `coretest.Synthetic`; bench.txt)

**Setup.**
- Fixtures:
  - 10k = 1,000 volumes × 9 snapshots;
  - 110k = 10,000 volumes × 10 snapshots;
  - half filesystems, half thick zvols, every 20th volume a clone.
- HEAD: the current `scan` (`zfs get -Hp` text, 26 MiB / 292 MiB) through a canned runner.
- Core: `Parse` of the JSON (22 MiB / 252 MiB for the 28-column scan).
- Host: shared with other agents (4 CPUs), so ns/op is noisy. Ranges are min–max of 3 runs; B/op and allocs are exact.

| | HEAD 10k | core 10k | HEAD 110k | core 110k |
|---|---|---|---|---|
| full scan → table/world | 396–1102 ms, 113.7 MB, 345k allocs | 205–413 ms, 9.6 MB, 48k allocs | 5.40–6.96 s, 1.30 GB, 3.79M allocs | 3.47–4.02 s, 110.7 MB, 532k allocs |
| **DeleteVolume, per RPC up to apply** | 168–268 ms, 113.7 MB, 345k allocs (full scan) | **0.47–0.68 ms, 278 KB, 102 allocs** (subtree scan) | 2.49–8.28 s, 1.30 GB, 3.79M allocs | **0.50–0.69 ms, 280 KB, 108 allocs** |
| core DeleteVolume on a full scan (to separate the scope win from the parser win) | | 222–376 ms, 9.4 MB | | 3.09–7.59 s, 109 MB |
| retained after the scan | 31.4 MiB | 2.2 MiB | 345.3 MiB | 22.2 MiB |

**Reading.**
- **DeleteVolume is about 350× faster at 10k and about 5,000× at 110k**, and constant in pool size. The scope does that, not the parser.
- The full parse is about 1.5–2× faster, allocates about 12× less, and retains about 15× less.
- After this run, a fix removed the per-snapshot header copies (2 strings per snapshot). Re-measured, under "after header fix" in bench.txt:
  - 10k: **30,281 allocs, 8.9 MB**;
  - 110k: **332,143 allocs, 103.5 MB**;
  - DeleteVolume: 84 / 88 allocs.

  Wall times in that rerun were 1.5–2× worse on all benchmarks: the host load had risen.
- In bench.txt, the names "27cols" and "16cols" mean the 28-property and 17-property scans; `clone_source_for` was added after the names were chosen.
- wave6's jsontext prototype reached 150 allocs at 10k with an interned `-` and no per-row strings. The remaining allocs here are per-volume map entries (the children index) and per-row user strings, which are the next things to cut.

## 13. Limits and open items

- **Not ported:** CreateSnapshot, DeleteSnapshot (`DeferDestroySnap` exists, its decide does not), Expand, List and pagination, Reconcile, Export (the CIDR floor lives there), `committed_at`, the property allowlist (borrowed from endpoint).
- **The lease** proves acquisition, not exclusivity of the source volume. Releasing an origin on a sibling volume is done without that volume's lock, as HEAD does. Step 2 should take `vol:<src>` as well, in id order, through the FIFO lock.
- **The guid guard window** (§5) remains; `zfs program` would close it.
- **coretest.Model** must join the `realfacts` conformance table (adversary C2) before any HEAD test is deleted on its strength. Facts still unconfirmed on a real 2.4 pool:
  - `clones` prints `-` when empty;
  - `-j` source shapes;
  - `-d` reaping.
- **Mutation protocol (E1)** has not been run on `internal/core`.

## 14. Strangler migration, one RPC per step, CI green at each

Each step ships behind `-core=<rpcs>` (default off, then on, then the old path is deleted one release later). Each step adds plan equality, on the same scan bytes, old vs new, for every spec row the RPC touches. Nothing is ever shadow-applied.

0. **One error table; a JSON scan beside `-Hp`.**
   - Move HEAD's zfs-error → code mapping into `core/code` + `decide.Rules`. `endpoint.CodeOf` reads it; `TestZfsErrCodes` becomes rows.
   - Add `core/world.Parse` behind the runner's `get -j`.
   - In tests, parse both formats from the Sim and assert that the HEAD `Table` and the World agree on ownership for every row (a differential test, not a code path).
   - Ships no behaviour change.
1. **DeleteVolume on core.**
   - Lock → `NewLease` → subtree `ParseUnder` → `DeleteScope` → decide → `Apply` with the guid guard.
   - Plan equality vs HEAD on every DEL/OWN row. The expected differences are listed rows: no legacy release; guid Aborted.
   - The golden rows of §5 go into CI.
   - Delete the 5 "impossible" tests and the replaced delete tests once the per-row killer set shows no row losing its last killer.
2. **CreateVolume on core.**
   - Add `req.Now` for `committed_at`, and move `ValidateProperties` and the allowlist into `core/req`.
   - `TestCreateCells` and `TestCrashConvergence` replace plan_test/clonesize/converge.
   - Plan equality on VOL/CLN/PRP rows. Expected differences: N1, D07, the cut Exists → Aborted.
3. Snapshots: CreateSnapshot, then DeleteSnapshot (`DeferDestroySnap`).
4. Expand.
5. List and Get from a read-only World (single-flight; cannot bind).
6. Reconcile, from a whole-parent leased World.
7. Export, with the per-edge etab and configfs sources.
8. Delete `table.go`, `plan.go` and the `-Hp` parser.
