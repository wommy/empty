# Adversary review: typed core and model-based suite (wave8)

Inputs. `wave8/core.md` and `wave8/model.md` had not been written when this was done (polled until 05:38 UTC). I attacked the code the architects had in progress instead:
- `wave8/core/internal/core/{name,world,op,code,req}` (05:24 snapshot);
- `wave8/model-copy/internal/endpoint/{model_test.go,modelrun_test.go}`;
- the brief's description: typed world, ownership enum, owned-handle tokens, decide(), an FSM table, and a stateful model test replacing the contract tests.

Each finding refers to rows in `wave8/spec.json` by id.

The yardstick is the wave7 mutation run: `wave7/results.jsonl`, 242 of 303 quota mutants at 05:25. It shows 186 killed, 46 survived and 10 build failures, so the kill rate is **80.2%**, on the simulator only. No `wave7/mutation.md` exists yet.

## A. Regressions the refactor is likely to drop

**A1. The clone-source snapshot is still recognised by its name; the refactor re-encodes adv4 F6 as a type (OWN-13).**
- Code: `parse.go classifySnapMarkers`: `!mOK && reserved && ParseVolID(suffix) ok -> SnapCloneSource`. The op.go comment says "ownership is the name".
- Scenario:
  - An admin runs `zfs snapshot tank/k8s/pvc-a@volume-source-for-volume-pvc-b` by hand, or sanoid is configured with that prefix.
  - The world mints `SnapCloneSource` for it. Reconcile's orphan rule (REC-08) or `ReleasableOrigin` then destroys a snapshot nobody owns.
  - The type now makes the bug look proven: there is no row-level marker, and the token exists.
- Mitigation:
  - `CutCloneSource` stamps `managed=true` and `<prefix>:clone_source_for=<dst>` with `zfs snapshot -o`.
  - Classification requires the stamp. A name match without the stamp is `SnapForeign`.
  - Add a SpecRow with a prefix-named admin snapshot. Legacy democratic-csi clone sources need an explicit adoption-mode rule.

**A2. `DestroySnap` is a plain `zfs destroy`, not `-d` (SNP-06, REC-01).**
- `op.go:324` renders `destroy <snap>`. Only `ReleaseOrigin` uses `-d`.
- `DoomedSnap` is documented for DeleteVolume, so this is right there: a non-deferred destroy fails with HasClones instead of hiding the snapshot.
- Risk: when DeleteSnapshot and Reconcile are ported they will reach for the one snapshot-destroy op that exists.
- Scenario: DeleteSnapshot of a CSI snapshot that has a restored clone gives `snapshot has dependent clones`, mapped to FailedPrecondition. The external-snapshotter retries forever.
  - HEAD returns OK and hides the snapshot (TestSnapshotLifecycleWithClones).
  - The model's `DeleteSnapshot` uses `destroyDeferred`, so the model would catch this, but only if the port runs the model before the old test is deleted.
- Mitigation: make the two forms separate types. `DeferDestroySnap` takes `CSISnap | OrphanCloneSource`. `DestroySnap` takes `DoomedSnap` only and is constructible only inside the DeleteVolume decision. Keep SNP-06 as a golden-argv row.

**A3. The `OkExists` tolerance on the internal snapshot is gone (ERR-10, CLN-05, CR-01).**
- Every op is now `TolNone` except destroys. `NewCutCloneSource` refuses when `c.Exists()` at scan time.
- This is safe only if every retry rescans. It is not safe against the race inside one request: the clone-source name is deterministic per (src, dst).
- Scenario:
  - DeleteVolume(dst) runs, but its deferred origin is still pinned by a hold, so the name stays in use.
  - A new CreateVolume(dst, source=src) scans, sees the stale deferred snapshot and judges "exists, reuse".
  - `zfs clone` from a deferred snapshot succeeds, and the CSI snapshot-hiding semantics differ.
  - Alternatively the scan says absent while zfs says exists. That gives AlreadyExists on a deterministic name, mapped to `codes.AlreadyExists`, which the provisioner treats as final.
- Mitigation:
  - A deferred clone source must never be minted as a reusable `CloneSource`. The handle refuses `defer_destroy=on`.
  - `ErrExists` on `CutCloneSource` maps to Aborted (retry), not AlreadyExists.
  - Add a scenario row: delete dst with a hold on its origin, then re-create dst from the same source.

**A4. The model hard-codes a contested decision as truth (VOL-06, D07).**
- `Model.CreateVolume`: `want > 0 && (d.Size == 0 || d.Size < want) -> AlreadyExists`. An adopted unlimited filesystem (refquota 0) with a `required` request is therefore AlreadyExists.
- dod3/type12 D07 decides the opposite. Once the model is the oracle, nobody can implement D07 without "breaking the model", and the model wins arguments because it is green.
- Mitigation:
  - Every model branch that encodes a decision cites its spec row id, and the row's `oracle` field says whether the model may be authoritative.
  - Rows with status `open-finding` or `decided-pending` are listed in a `knownOpen` table with an expiry, never folded silently into model logic.

**A5. Sim-only facts the model inherits, oracle=openzfs.**
- Rows: VOL-11, DEL-05, SNP-11, SCN-13, NFS-04, NFS-16, OWN-09, CR-03.
- The model's zfs half is `reap`, `destroyDeferred`, `destroyTree` and `hasClones`. It is a third copy of dsl_destroy semantics: Sim, HEAD's `simulateDestroy`, and now the model.
- Concrete gaps in model_test.go:
  - `Reserved bool` cannot express `refreservation=auto` vs a hand-set byte value (VOL-11 is invisible).
  - `roundUp(want, 16384)` ignores volblocksize (VOL-02, VOL-16).
  - `hasClones` sees only datasets under the parent, so a clone outside the parent cannot exist (DEL-03).
  - There is no `create -p` (OWN-09).
  - There is no sharenfs at all (NFS-01..22: 22 rows, 0 modelled).
  - There are no properties (PRP-01..08).
  - There is no Expand, List or pagination (EXP-*, LST-*).
  - There is no time, so MinAge is absent (REC-05, REC-06).
- The mutation data shows what that costs. The top killing tests at 05:25 were:

  | Test | Kills |
  |---|---|
  | TestPlanReconcile | 36 |
  | TestSanity | 28 |
  | TestExpandNodeExpansionRequired | 28 |
  | TestValidatePropertyValues | 28 |
  | TestParametersAndAccessChecks | 7 |

  Three of those five cover areas the model does not model.
- Mitigation: no contract test is deleted until a mutant set restricted to its file shows the model kills at least as many (B3). Rows outside the model's alphabet stay SpecRows (spec_rows.go.txt).

**A6. "received is not owned" passes for the wrong reason (OWN-01, OWN-06).**
- `Model.AdminRecv` creates the copy with no markers. The comment says "received or inherited values are not ownership and are not modelled at all."
- On the real or Sim side the copy carries every marker with source `received`. The model therefore agrees with any implementation that ignores `source`, provided the implementation also happens to treat the copy as foreign. That is not the property under test.
- Scenario:
  - A refactor bug makes `parse.go` read `source.type=="RECEIVED"` as local. The copy becomes owned and ListVolumes shows it.
  - The model diverges only if the trace calls ListVolumes or Delete on that exact name after `adm.recv`.
  - The generator picks ids from a shadow model that does not know the copy is "ours-looking". Detection is probabilistic and seed-dependent.
- Mitigation: one deterministic SpecRow per source type (local, received, inherited, default, temporary, none) through the real decoder. This is OWN-01; keep TestPropLocalIsOnlyLocal. Also bias genTrace so that every `adm.recv` is followed by an RPC on the copy.

**A7. The parse bitset overflows past 64 properties (SCN-03, PRF-06).**
- `parse.go:397-418` tracks seen properties in a `uint64` with `1<<j` and checks `seen != 1<<len(p.props)-1`.
- The schema has 9 core natives, about 7 markers, the per-kind allowlist (PRP-03 has about 20) and the export columns. Narrowed scans are fine; a whole-parent scan with all columns is close to 64.
- At 64 the shift wraps. Go defines the result as 0 rather than panicking, and the completeness check becomes `seen != 0-1`, which no longer means "all present".
- Mitigation: `if len(p.props) > 64 { panic }` at schema build time, plus a test that builds the widest schema.

**A8. HEAD tests that encode HEAD bugs will be ported as spec.**
- `FuzzReconcileSafe`'s oracle protects only CSI snapshots and identifies clone sources by name (reconcile_fuzz_test.go:140-147, OWN-13).
- `TestShareNFSAndClientEncoding` asserts the `nfs_clients` encoding, which ADR 0004 removes (NFS-01).
- `TestParseClient` accepts hostnames (NFS-08).
- A mechanical "port every test to the new core" carries all three forward.
- Mitigation: port from `spec.json`, not from test files. A test whose only row is `open-finding` or `decided-pending` is rewritten to the decided behaviour or quarantined in `knownOpen`.

## B. Typed tokens and TOCTOU

**B1. Handles prove a property of the scan, not of the pool at exec time (OWN-15, LCK-02).**
- `OwnedVol`, `DoomedSnap` and `FreeVol` are minted from a `*World`. Nothing ties a handle's world to the locks held, or to an exec that happens after a context switch.
- Scenarios, all type-correct:
  - **C1. Sanoid race.** Sanoid snapshots `pvc-a` between scan and `DestroyVol`.
    - Today: the plain non-recursive `zfs destroy pvc-a` fails with "has children", so FailedPrecondition. This is the CX2 fix, and it holds only because destroy is non-recursive.
    - Residual test: BeforeOp injection of a snapshot before every destroy op.
  - **C2. Admin rename.** An admin runs `zfs rename pvc-a pvc-a-keep` and then creates a fresh `pvc-a`, between scan and `DestroyVol(pvc-a)`.
    - zfs destroys the new, foreign `pvc-a`, because the name matches.
    - Only a guid check closes this: a channel program, or `zfs get guid` immediately before destroy, which still leaves a microsecond window.
    - Residual: a scenario row, plus an ADR sentence accepting the window if it is not closed.
  - **C3. Hold race.** An admin places a hold on a `DoomedSnap` before `DestroySnap`. zfs returns "it's being held", which is FailedPrecondition. This is safe, but the plan has partially applied, so ERR-11 applies (retryable code after mutation).
  - **C4. Admin sharenfs edit.** An admin edits sharenfs between the publish scan and the set. It is rewritten as MANAGED. This is csi-nfs C8 and not closable; document it.
  - **C5. Clone target race.** A dataset is created at the clone target. `zfs clone` returns ErrExists, which must be AlreadyExists with no follow-up ops. This holds only because `Clone` has `TolNone` (OWN-08).
- Mitigation:
  - Keep `DestroyVol` non-recursive and `Clone` `TolNone` as golden argv/tolerance rows. These are the real TOCTOU defences; the types are not.
  - Mint handles only from a `LockedWorld` produced by `ScanAfterLock(locks)`. A `*World` from List single-flight (PRF-04) cannot mint destructive handles.
  - The scenario registry gets a `BeforeOp(k, adminAction)` matrix: every destructive plan, every k, actions {snapshot, child, hold, rename+recreate, recv}.

**B2. `Bound[R]` checks coverage but not freshness.**
- `Bind(w, r)` checks `scope.Includes`. A world scanned for request A can be bound to request B with the same scope, for example the retry loop in apply re-using the pre-apply world.
- Mitigation: the world carries a generation number, and apply refuses a world older than its lock acquisition.

## C. The reference model as oracle (common mode)

**C1. Same author, same misreading.** The model, the Sim and decide() are written by the same agents from the same reports. A wrong belief about zfs ends up in all three and they agree. Examples: the snapshot size after zvol growth (SNP-11 is already a known Sim bug), and xattr printing on 2.2 vs 2.4.
- Mitigation: the model run must pass on a real 2.4 pool (`modelrun_test.go` already supports `ZFS_TEST_POOL`). A divergence on the pool that is absent on the Sim is a model or Sim bug and blocks the merge.
- Budget: the pool run is the only oracle for the 23 scenario rows with oracle openzfs or nfs-utils (spec.md lists them). Gate CI on a fixed-seed pool run of at least 500 steps plus these 23 scenarios. The full seed sweep stays Sim-only.

**C2. What only OpenZFS (or nfs-utils) can answer.** These rows must take their expected values from the real system, never from the model:

| Row | Fact |
|---|---|
| OWN-09 | `create -p` exits 0 on an existing dataset |
| VOL-11 | refreservation=auto equals zvol_auto_resv, including copies and metadata |
| SNP-06 / DEL-05 | `-d` deferral and reaping |
| OWN-01 / OWN-06 | received markers have source `received` |
| NFS-16 | inherited sharenfs exports `*` until pinned |
| NFS-15 | root squash: new root owned by nobody unless chowned |
| NFS-04 | `inherit` vs `off` |
| NFS-05 / NFS-06 | etab written while nfsd is stopped |
| SCN-04 | `-j` with invalid UTF-8 truncates with exit 0 |
| SCN-13 | index property printing |
| CR-03 | `destroy -r` post-order non-atomicity |
| CR-04 | create -o atomicity |
| DEL-03 | clones outside the parent |

- Mitigation: a `realfacts` package of about 15 tiny tests that run raw `zfs` commands, not the endpoint. Each asserts one fact and is tagged with its spec id. The Sim and the model each get a conformance test against the same table, so when OpenZFS 2.4.x changes a fact, one test fails, not three models silently.

**C3. Masked inputs hide regressions.** The modelrun header says HEAD divergences are masked in the generator (`knownOpen`). A mask that never expires turns the model into an assertion that "the bug is still there".
- Mitigation: each mask names a spec row with status `open-finding` and fails CI when the row's status changes to `current`, or after a date.

## D. Strangler migration

**D1. Dual-running cost and divergence.**
- `wave8/core` is a full repo copy that already edits `endpoint.go`, `table.go`, `export.go`, `zfs.go` and `sim.go` alongside the new `internal/core`. That is a big-bang branch, not a strangler.
- Scenario: the old planner and decide() both exist for N weeks. A shadow mode (plan with both, compare `Plan.String()`, apply the old one) costs one extra planning pass per RPC. Planning is cheap; the extra scan is not. The shadow must reuse the same scan bytes, otherwise the two diverge on timing instead of logic.
- Mitigation:
  - Migrate one RPC at a time behind an `-core=rpc1,rpc2` flag, ordered by risk: List/Get, then Create, Snapshot, Expand, then Delete, then Reconcile, then Export.
  - In tests, run `Plan` equality on the same world for every SpecRow (old vs new).
  - Never shadow-apply.

**D2. CI time.**
- The wave7 run shows a sim-only `go test ./internal/...` at about 16 s per mutant run.
- The model adds `MODEL_SEQS x MODEL_LEN` steps. With crash and fault injection at every op k, a trace of L steps with an average of p ops gives about L·p replays.
- A pool run serialises on zfs (roughly 20-50 ms per exec).
- Scenario: 200 seeds x 40 steps x about 4 ops x 5 fault kinds is about 160k zfs execs, roughly 1.5-2 h on a pool. It will be cut to a smoke run within a week and lose its value.
- Mitigation:
  - Three tiers:
    1. Per-PR: SpecRows plus a fixed seed corpus (replay of shrunk failing traces), on the Sim, under 60 s.
    2. Per-PR on the pool: the 23 upstream-oracle scenarios plus 500 model steps, under 10 min.
    3. Nightly: seed sweep and fuzz.
  - Record steps per second in CI so that a cut is visible.

**D3. Error-code drift.** `core/code/code.go` is a new code mapping alongside `endpoint/errors.go`. Two mappings during the migration means `TestZfsErrCodes` (ERR-04) passes on one while the other ships. Mitigation: a single table (ERR-04 and ERR-05 as data) imported by both paths from day one.

## E. "Fewer tests": measured, or McNamara?

**E1.** The claim is meaningful only as a before/after mutation kill rate on the same mutant set. The test count and line count are irrelevant.
- Baseline: 80.2% at 05:25, Sim only, on HEAD's files: plan.go, props.go, reconcile.go, table.go and zfs/errors.go.
- Survivors by bucket:

  | File | Survived |
  |---|---|
  | table.go | 10/42 |
  | plan.go | 21/92 |
  | props.go | 6/41 |
  | reconcile.go | 5/55 |
  | zfs/errors.go | 4/12 |

- Problem 1: the refactor deletes plan.go and table.go, so the "after" mutants live in a different code base. Comparing 80% on old code with X% on new code compares two different denominators.
- Problem 2: a model test that kills more mutants on the simulator can still miss every oracle=openzfs row (C2), because the Sim and the model share the fact.
- Mitigation (the measurement protocol):
  1. Generate mutants for `internal/core/**` with the same operators.
  2. Report the kill rate per spec kind: map each mutant to the rows whose code it touches, or simply by file and function.
  3. Report the per-row "killed by" set, so that deleting a test shows exactly which rows lost their only killer.
  4. Run the pool tier on a 10% mutant sample, so that "killed on the Sim only" is visible.
  5. Report the share of spec.json rows with at least one killer, not the test count.
  - The claim holds only if the kill rate is at least the baseline AND no current row loses its last killer AND the uncovered-row count (57 today, all of them open-finding, decided-pending or current-untested) does not grow.

**E2. Equivalence classes.**
- Some survivors are equivalent mutants, for example defensive branches. Types remove those branches, which raises the kill rate without any change in safety.
- Mitigation: report killed/(killed+survived) both with and without mutants on lines the type system now makes unreachable. Say which is quoted.

## Top 8 refactor risks

1. A1: the clone-source snapshot is still owned by name, now encoded as a type, so a user snapshot named `volume-source-for-volume-x` is destroyable by a "proven" handle (OWN-13).
2. B1: handles prove scan-time ownership only. The CX2 and racing-clone defences are the non-recursive destroy and `TolNone` on clone, which are argv and tolerance details a refactor can silently change. Pin them as golden rows and add a BeforeOp race matrix (OWN-15).
3. C1/C2: the model, the Sim and decide() are three copies of the same zfs beliefs (refreservation=auto, -d reaping, received source, inherited sharenfs, snapshot size). Without a pool-run tier and a `realfacts` table the model certifies its own errors.
4. A5/E1: the model does not cover export, properties, Expand, List, limits or time. That is 22 NFS rows and 8 PRP rows, and the tests that kill about 40% of today's mutants. Deleting contract tests "because the model covers it" drops them.
5. A2: one snapshot-destroy op without `-d` will be reused for DeleteSnapshot and Reconcile, turning "delete a cloned snapshot" into FailedPrecondition forever (SNP-06, REC-01).
6. A4/C3/A8: contested or buggy HEAD behaviour gets frozen. The model says unlimited fs + required = AlreadyExists (against D07), knownOpen masks never expire, and HEAD tests that encode `nfs_clients`, hostnames and name-based clone sources are ported as spec.
7. D1/D2: the big-bang branch rather than a strangler, plus a model run whose pool cost (an estimated 1.5-2 h) will get trimmed. Migrate one RPC at a time with SpecRow plan equality, and use a three-tier CI with measured steps per second.
8. E1: "fewer tests" is unmeasured. The before/after kill rates are on different code, Sim-only, and test count is the reported metric. Require per-row killer sets, a pool sample, and no row losing its last killer.

Minor: A3 (the deferred clone source reused, or ErrExists becoming final AlreadyExists), A7 (the uint64 bitset over 64 properties), B2 (world freshness) and D3 (two code maps).
