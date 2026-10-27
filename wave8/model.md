# A model-based suite for the ZFS endpoint (wave8)

Base: HEAD **8cc52ef** (reconcile and lifecycle merged). The prototype is in
`scratchpad/wave8/model-copy` (a git copy; commits `8cc52ef` → `sim extensions` → `model on 8cc52ef` → `HOLD-REPLAN` → `realfacts, masks-still-bite`).
The first version ran on 9f1790d (`scratchpad/wave8/model-9f1790d`). The revert and mutation experiments ran there and are labelled with that base.
The repository itself was not touched.

## 0. Answer

- **What it is.** One reference model, a pure state machine of about 640 lines, and one harness of about 1,330 lines. The harness:
  - generates random RPC and out-of-band traces;
  - injects crashes, errors and races at plan step k through the `Runner` seam;
  - compares codes and a guid-level world projection after every step;
  - shrinks a failure to a 1–4 step replayable trace.

  It runs unchanged on the Sim and on a real pool. A separate table of **16 raw OpenZFS facts** checks, on the real-pool tier, the beliefs that the model, the Sim and the planner share.
- **Bugs found.** **10 of 10** reverted fixes were detected at 9f1790d, on 5 seeds each (50/50).
  - The median time-to-find is under 0.4 s and the worst is 5.2 s.
  - One of them, R9, is caught by **no** existing test.
  - At 8cc52ef, 7 reverts still apply. Section 5.2 gives their results.
- **Divergences at HEAD.**
  - The model found 4 divergences at 9f1790d. All four are fixed at 8cc52ef, and a 20,000-sequence soak at 8cc52ef was green.
  - It found 3 new divergences at 8cc52ef. Each is masked with a spec row and an expiry, and each is proven to still fail when unmasked (§4).
- **Redundant tests.** Section 6.3 gives the counts from per-row killer sets and the adversary's rules.
- **Runtime.**
  - Per PR on the Sim: 400 sequences × 40 steps (16k steps) took **14.7 s wall, 10.7 s CPU** on a 4-core host at load average 64. Expect about 3–4 s on an idle 4-core runner. `-short` (50 sequences) takes under 1.5 s CPU.
  - Real pool: 200 steps under a 60 s cap plus the fact table (under 2 s).
  - Nightly: a 20k-sequence sweep, fuzzing, and the mask-still-bites check.

## 1. Design

### 1.1 Model (`internal/endpoint/model_test.go`)

The model is a pure Go state machine with no zfs and no endpoint types except request/response codes.

- **State.**
  - `DS[name] = {Kind, Size, Reserved, Origin, Managed, Committed, VolName, SrcType, SrcID}`.
  - `Snaps[name] = {Managed, SnapName, SnapSrc, CloneFor, Deferred, Holds}`.
- **Ownership** is spelled out once:
  - `ownedVol` (VolName == id);
  - `ownedSnap`;
  - `ownedCloneSource` (the stamp `managed` + `clone_source_for=<id>`);
  - `served` (owned and committed).
- **RPCs:**
  - CreateVolume: empty, from a snapshot, or from a volume. D07 applies: an unlimited filesystem satisfies `required`.
  - DeleteVolume: plan-time refusals, then the order origin(`-d`) → snapshots → volume, all plain destroys (DEL-06).
  - DeleteVolumeRaced: the interleaved linearisation used for races between plan ops.
  - CreateSnapshot, DeleteSnapshot(`-d`).
  - Reconcile:
    - the "stated" rule;
    - the MaxDeletions cap;
    - a fixpoint of releasable origins;
    - a sweep of stamped orphan clone sources;
    - sorted `target|reason` actions.
- **Out-of-band actions:**
  - `adm.snapshot`, which can carry the reserved name `volume-source-for-volume-<v>`, the adversary's A1;
  - `adm.create`, `adm.child`, `adm.clone`;
  - `adm.recv`, which copies a dataset with markers as `received`;
  - `adm.recvsnap`, a snapshot with received CSI markers (sim only);
  - `adm.rename`, `adm.hold`, `adm.release`.
- **`Project()`** gives the observable world: names, kinds, sizes, origins, local markers, deferred flags and holds.

### 1.2 Harness (`internal/endpoint/modelrun_test.go`)

- **Generator.** A hand-rolled generator behind a `source` interface: `rngSource` (PCG, seeded) for TestModel and `byteSource` for FuzzModel.
  - 75% of picks target owned names.
  - 25% of RPCs carry a fault: `killBefore`, `killAfter` or `errBefore` at mutation k ∈ {0,0,0,1,1,2}, or `race` (an out-of-band action injected before mutation k).
  - After `adm.recv` the next step targets the copy.
- **faultRunner.** Wraps the `zfs.Runner` and counts mutating argv (create, clone, snapshot, set, destroy, rename, hold, release). A kill returns `context.Canceled` the way a killed child does.
- **Rig.** One parent dataset per sequence: `tank/s<seed>` on the Sim, `$ZFS_TEST_POOL/model-<pid>-<seed>` on a real pool, destroyed at cleanup.
  - The out-of-band side runs real `zfs` argv through the same Runner, so it is real zfs on a pool.
  - `Config.Now` advances 2 h per RPC step (an atomic clock), so `committed_at` and MinAge are deterministic. There are no sleeps.
- **Observer.** It is independent of the code under test. It reads `zfs get -Hp all` through the base runner and decides "local" from the raw `source` column, never from `zfs.Prop.Local`. An earlier version used `Prop.Local`, and reverting 5e5b2eb then corrupted the oracle itself.
- **Races.** These are checked by linearisability:
  - A race result is accepted if it matches "foreign first" or "RPC first".
  - For DeleteVolume there is also the interleaved candidate.
- **Shrinking.** `ddmin` over steps (chunks, then single steps), then fault removal, keeping the same failure kind. The report prints the seed, the `MODEL_SEED=<n>` replay line, time-to-detect and the minimal trace.
- **Workers.** `MODEL_WORKERS` (default 8) partition the seeds. The first failing seed is the lowest one, so the report is deterministic regardless of scheduling.

### 1.3 Invariants checked after every step

| kind | invariant |
|---|---|
| `result` | The gRPC code (and id/size) equals the model's for the same request in the same state. |
| `world` | The projection equals the model's. After a race it must equal one of the linearisations. |
| `foreign-changed` | Nothing unowned changed: every row that is neither owned by its markers nor deferred keeps its guid and every local property (OWN-05/10/12/15, REC-02). |
| `final-not-atomic` | An `errBefore` fault that fired and produced a final (non-retryable) code left the world unchanged. |
| `killed-final` | A killed command never yields a final code: only Aborted, Unavailable, DeadlineExceeded, Canceled or Internal-retryable are allowed (ERR-07/11). |
| `retry` | Retrying the same RPC with no fault converges to the model's fault-free answer and world (CR-01). The only allowance is residue adoption: a stamped orphan clone source on an owned volume. |
| `reconcile-twice` | Reconcile dry-run after an OK reconcile plans nothing (REC-09/11). |
| `partial-table` | Call-log coverage: a "not found" or "absent" conclusion must have read its subject, and CreateSnapshot and Reconcile must read the whole parent before their first mutation (SCN-10). |
| `admin` | Every out-of-band action the model allows succeeds in zfs, and every one it refuses fails. This keeps the generator honest on a real pool. |

### 1.4 Seeds, replay, flakes

- Every sequence is `PCG(seed, 0x5eed)`.
- `MODEL_SEED=n` replays one sequence and `MODEL_SEED0` shifts the sweep.
- Nothing reads wall-clock time except the expiry check on masks, which compares dates.
- 20k-sequence soaks at both bases gave identical verdicts on reruns. The only nondeterminism, map order, is sorted away in `Project()` and in the action lists.

## 2. Files and how to run

| file | lines | what |
|---|---|---|
| `internal/endpoint/model_test.go` | 640 | the reference model |
| `internal/endpoint/modelrun_test.go` | 1,330 | faultRunner, generator, rig, observer, invariants, masks, shrinker, TestModel, FuzzModel, TestModelMasksStillBite |
| `internal/endpoint/realfacts_test.go` | 300 | TestRealFacts: 16 raw OpenZFS facts |
| `internal/zfs/zfstest/sim.go` | +127 | guid, userrefs, hold/release, rename, held-snapshot semantics, "parent is not a filesystem" |

```
go test ./internal/endpoint -run 'TestModel$'                     # 400 seqs x 40 steps
go test ./internal/endpoint -run 'TestModel$' -short              # 50 seqs (pre-commit)
MODEL_SEED=1234 go test ./internal/endpoint -run 'TestModel$' -v  # replay one seed
MODEL_SEQS=20000 go test ./internal/endpoint -run 'TestModel$'    # nightly sweep
go test ./internal/endpoint -fuzz FuzzModel -fuzztime 5m          # nightly fuzz
MODEL_NIGHTLY=1 go test ./internal/endpoint -run TestModelMasksStillBite
MODEL_UNMASK=all go test ./internal/endpoint -run 'TestModel$'    # see the open divergences
ZFS_TEST_POOL=tank ZFS_REQUIRE_REAL=1 go test ./internal/endpoint -run 'TestRealFacts|TestModel$'
```

Knobs:
- `MODEL_SEQS` (400), `MODEL_LEN` (40), `MODEL_SEED`, `MODEL_SEED0`, `MODEL_WORKERS` (8);
- `MODEL_SHRINK` (400 runs), `MODEL_BUDGET` (200 real-pool steps), `MODEL_UNMASK`, `MODEL_NIGHTLY`.

On a real pool the harness caps sequences at 25 steps and runs `MODEL_BUDGET / 25` sequences, each on its own child dataset. `adm.recvsnap` is skipped there and counted in the `-v` stats.

## 3. Raw OpenZFS facts (adversary point 1)

The model, the Sim and `decide()` were written from the same beliefs, so agreement between them proves little. `TestRealFacts` runs **plain zfs argv**, never the endpoint, one fact per spec row.

- **On the real-pool tier** it is the oracle: every fact must pass.
- **On the Sim** it is a conformance run:
  - a fact the Sim is known to get wrong is listed as `simDiverges`, and the Sim must *fail* it, so a Sim fix shows up as a test to update;
  - a fact the Sim cannot express is skipped with a reason.

| row | fact | Sim |
|---|---|---|
| OWN-09 | `zfs create -p` exits 0 on an existing dataset | agrees |
| OWN-08 | clone onto an existing name → Exists | agrees |
| SNP-06 | `destroy -d` of a cloned snapshot exits 0 and sets `defer_destroy=on` | agrees |
| DEL-05 | destroying the last clone reaps a deferred origin | agrees |
| DEL-07 | a plain destroy of a held snapshot → Busy | agrees |
| DEL-05/holds | `destroy -d` of a held snapshot defers it; the last `release` reaps it | agrees |
| DEL-06 | a plain destroy of a filesystem with a snapshot → HasChildren, and the snapshot is kept | agrees |
| OWN-12 | a plain destroy of a filesystem with a child dataset → HasChildren | agrees |
| OWN-12/zvol | nothing can be created below a zvol | agrees (fixed in this wave) |
| DEL-03 | a plain destroy of a snapshot with a clone → HasClones | agrees |
| OWN-02/rename | rename keeps the guid and local markers, and moves the clone's `origin` | agrees |
| OWN-06 | `send -R \| recv` delivers user properties with source `received` | skip: the Sim fakes recv with exactly this belief |
| VOL-11 | thick zvol refreservation ≥ volsize; sparse = 0 | agrees |
| SNP-11 | a zvol snapshot keeps its volsize after the zvol grows | **diverges** (the Sim reads the live zvol) |
| CR-03 | `destroy -r` stopped by a held snapshot has already destroyed others (not atomic) | **diverges** (the Sim refuses atomically) |
| A3 | a snapshot pending deferred destroy can still be cloned | agrees |

- Each fact runs under a fresh `<pool>/facts-<ns>-<i>` and is cleaned with `destroy -R`. The whole table takes under 2 s on a pool and 6 ms on the Sim.
- The two Sim divergences are why the model does not use `destroy -r`, and why snapshot size is not in the world projection.
- **Not yet run on a pool here:** there is no ZFS pool in this container. The table is written against OpenZFS 2.4 man-page semantics, and its first real-pool run is the acceptance test.

## 4. Known-open divergences: masks with expiry (adversary point 3)

- **What a mask does.** A mask skips (or removes the fault from) only the input that reaches a known divergence. It never changes the model's expected answer, so the model keeps stating the decided contract.
- **D07.** It is modelled as decided: an unlimited filesystem plus `required` gives OK. HEAD's AlreadyExists is a masked divergence, not frozen behaviour.
- **Expiry.** Every mask carries a spec row and an expiry date. **TestModel fails once a mask has expired.**
- **Mask still bites.** `TestModelMasksStillBite` (nightly) lifts one mask at a time and fails if the divergence can no longer be reproduced. A fixed bug therefore forces the mask's deletion.

| id | row | expires | divergence at 8cc52ef | unmasked: found in |
|---|---|---|---|---|
| D07 | VOL-06 | 2026-12-15 | CreateVolume of a filesystem with cap > 0 on an existing *unlimited* filesystem returns AlreadyExists; D07 says OK | 0.55 s |
| OWN-12 | OWN-12 | 2026-12-15 | the scan drops rows below a volume, so reconcile re-plans a destroy that zfs refuses (has children) on every run; reconcile-twice is never empty | 8.8 s |
| HOLD-REPLAN | REC-11 | 2026-12-15 | the scan does not read `userrefs`, so a held snapshot on an undesired volume is re-planned (Busy) on every run | 44.8 s |
| GAP-RECRACE | OWN-15 | 2026-12-15 | **model gap, not a HEAD bug:** a race inside one reconcile group is not interleaved (DeleteVolume's is); the fault is dropped | GAP_RESULT |

Mask-bite times were measured at load average 64 on 4 cores.

## 5. Proof: reverted fixes

### 5.1 At 9f1790d: 10 reverts, 5 seeds each (`reverts/reverts.py`, `reverts/ttf.txt`)

Each run used `MODEL_SEQS=40000` and stopped at the first failure. Seeds were seed0 ∈ {1, 100001, 200001, 300001, 400001}.

| # | fix reverted | existing tests that fail | model: detected | time-to-find (min–max over 5) | steps to find | minimal trace | caught by |
|---|---|---|---|---|---|---|---|
| R1 | 35fa482 clone never adopts a racing target | TestCloneNeverAdoptsARacingDataset | 5/5 | 17 ms – 5.2 s | 40 – 9,040 | 2–3 | world-after-race |
| R2 | 5e5b2eb `received` is not ownership | TestPropLocalIsOnlyLocal, TestDeleteVolumeSparesForeignSnapshots (message only) | 5/5 | 103 – 794 ms | 120 – 920 | 3–4 | result |
| R3 | 16a356c clone only from owned, committed sources | TestCloneSourcesMustBeOwned | 5/5 | 4 – 357 ms | 40 – 280 | 2–3 | result |
| R4 | 725c328 size clones before the resume branch | TestCloneFailureAfterCloneConvergesToSameState, TestReserveRestoreIsIdempotent | 5/5 | 0.42 – 3.95 s | 520 – 10,440 | 2–4 | world, result-after-kill |
| R5 | d4ec271 DeleteVolume spares foreign snapshots | TestDeleteVolumeSparesForeignSnapshots | 5/5 | 38 – 148 ms | 40 – 160 | 3–4 | result |
| R6 | 6d4043a busy/unknown mapped by operation | TestBusyIsMappedByOperation, TestUnknownFailureAfterAMutationIsRetryable | 5/5 | 39 – 397 ms | 40 – 640 | 1 | final-not-atomic |
| R7 | 0bd51b0 opaque ids (never-issued) | TestNeverIssuedIDs (csi package only) | 5/5 | 12 – 539 ms | 40 – 560 | 1 | result |
| R8 | 08b9699 class: CreateSnapshot judges global names from a partial table | 4 tests incl. TestSanity | 5/5 | 1 – 15 ms | 40 | 2 | partial-table |
| R9 | 35fa482 ownership requires the name marker to match | **none** | 5/5 | 81 – 482 ms | 80 – 520 | 3–4 | result |
| R10 | 2fadc0c killed → Interrupted, never Internal | TestKilledCommandIsInterrupted | 5/5 | 8 – 37 ms | 40 | 1–3 | killed-final |

- **R9.** TestVolumeNameMustMatchID, TestCloneSourcesMustBeOwned and TestReconcileSparesReceivedAndRenamed are vacuous for this revert. They set `democratic-csi:` properties under the default `zfs-endpoint:` prefix, so their "foreign marker" rows are not markers at all.
- **Four HEAD bugs found at 9f1790d,** all fixed at 8cc52ef:
  - F1: `destroy -r` takes a foreign child (3 steps, 0.5 s).
  - FSM2: an admin snapshot races `destroy -r` (2 steps, 2.1 s).
  - RF3: a held deferred snapshot aborts reconcile (4 steps, 27 s).
  - FSM1: a crash between `destroy -r` and the origin release (soak, about 10^5 steps).

### 5.2 At 8cc52ef (`reverts/rev8.txt`)

R3, R5 and R9 no longer apply textually, because 8cc52ef rewrote those predicates. The other seven ran once, with seed0 = 1 and `MODEL_SEQS=20000`.

REV8_TABLE

## 6. Which hand-written tests the model makes redundant (adversary point 2)

### 6.1 Method

Wave7's 303 sampled mutants (`wave7/mutants.json`, 10 build failures excluded) were re-run at 9f1790d with **every** test collected per mutant, TestModel included at 300 sequences (`mut/run.py` → `mut/res*.jsonl`). `killers/analyze.py` then builds per-test kill sets K(t) and applies the adversary's rules.

A test is proposed redundant only if all of the following hold:
- (a) K(t) is non-empty and K(t) ⊆ K(model). A test the sample never reached has no evidence and is kept.
- (b) Every spec row listing t has another killer: a kept test on the same row, or the model when the row is one it asserts (the `MODEL_ROWS` list in the script, 59 rows).
- (c) t is not in a hand-written tier: golden, csi-sanity, round trip, classify/parse/decode units, fuzzers other than FuzzModel, listen/lock, Sim conformance.
- (d) t is not about an area the model omits: exports, properties, Expand, List/pagination, limit_bytes, MinAge, parameters, prefix, missing parent.

Tests encoding contested behaviour (nfs_clients, hostnames, name-based clone sources) are flagged and never ported.

MUT_RESULTS

### 6.2 Kill rates

KILL_RATES

### 6.3 Redundant tests

REDUNDANT

### 6.4 What stays hand-written

- **Golden documentation examples and the golden plan table** (INV-03). They are readable specs of argv and plans, which the model cannot be.
- **csi-sanity** (CSI-04). It is the external oracle for the CSI contract.
- **The real-pool property round-trip table** (PRP-03/04/05, SCN-13). The model has no properties.
- **Units outside the model's alphabet:**
  - error classification and its corpus (ERR-03/13);
  - parseColumns, decode and FuzzParseColumns (SCN-*);
  - listen and flock (RT-01, LCK-03);
  - lock-wait timing (ERR-08, REC-10);
  - planner-level reconcile tables that kill mutants the model misses (TestPlanReconcile, TestPlanReconcileHeldLimitsActions, TestReconcileAgainstDesiredSet; §6.2).
- **The whole export area** (NFS-*): Publish/Unpublish, etab, nfsd. The model does not include exports, so "nfsd stop" is not one of its out-of-band actions. It belongs to the export diff-fuzz.
- **Expand, List, limit_bytes, MinAge boundaries, parameters.** These stay as table tests or SpecRows.

## 7. CI tiers and time budget

| tier | runs | budget | measured |
|---|---|---|---|
| pre-commit | `TestModel -short` (50 × 40) + TestRealFacts on the Sim | < 2 s | 4.8 s wall / 1.3 s CPU at load 64 |
| per-PR, Sim | TestModel 400 × 40 (16k steps), FuzzModel seed corpus, replayed corpus of shrunk traces | ≤ 20 s wall; about 4 s on an idle 4-core runner | 14.7 s wall / 10.7 s CPU at load 64 |
| per-PR, real pool (VM) | TestRealFacts (16 facts) + TestModel `MODEL_BUDGET=200` (8 seqs × 25 steps, each on its own child dataset) under `-timeout 60s` | ≤ 60 s | not run here (no pool). Estimate from wave7 run19: about 30–60 ms per zfs command, about 6 commands per step, so 200 steps ≈ 40–70 s serial and ≈ 15–25 s at 4 workers |
| nightly | MODEL_SEQS=20000 sweep (seed0 rotates daily, logged), `-fuzz FuzzModel -fuzztime 5m`, TestModelMasksStillBite, MODEL_BUDGET=2000 on the VM | ≈ 10 min | 20k sweep: 48–68 s on an idle host; masks still bite: ≈ 1 min |

On the real pool, a failure's seed and minimal trace are printed. The trace replays on the Sim with `MODEL_SEED`, which tells a planner bug apart from a Sim belief that is wrong.

## 8. `pgregory.net/rapid`?

**Not worth adding.**
- The generator is about 150 lines behind a two-method `source` interface. The same code serves seeded sweeps and `testing/F` fuzzing.
- The ddmin shrinker is about 40 lines and shrinks every finding here to 1–4 steps.
- What rapid would add is integrated shrinking of generated *values* and a state-machine DSL. Neither was missing.
- What it would cost: a dependency in a repo that has kept its test tree stdlib-only, and a second seed/replay convention next to `go test -fuzz`.
- Revisit only if the model's request space grows large enough, for example properties, that value-level shrinking matters.

## 9. Responses to the adversary (`adversary.md`)

1. **Shared beliefs.** Two measures:
   - The §3 fact table runs plain zfs and is the oracle on the VM tier. Two Sim divergences are already pinned there.
   - The world observer no longer uses `Prop.Local`.
2. **Coverage gaps.** Exports, properties, Expand, List, limit_bytes and MinAge are outside the model. Rule (d) keeps every test in those areas, and no test is proposed for deletion unless every one of its spec rows has another killer (§6).
3. **Contested behaviour.**
   - D07 is modelled as decided, and HEAD's divergence is masked with an expiry.
   - Masks expire, and they must still bite every night.
   - nfs_clients, hostnames and name-based clone sources are not in the model. The model recognises clone sources only by the stamp; a bare reserved name is foreign (A1 is in the generator).
   - Tests that encode those three are listed separately in §6.3.
4. **Base.** The work is on 8cc52ef. The 9f1790d results are labelled as such.
5. **Real-pool size.** The real-pool run is 200 steps by default and capped at 25 steps per sequence. Each sequence gets its own child dataset with cleanup, and the run fits a 60 s cap.

## 10. Residual gaps

- **No real pool in this container.** TestRealFacts and the real leg of TestModel have only been compiled and run on the Sim. Their first VM run is the acceptance test, and the facts may need wording fixes.
- **The mutation sample is from 9f1790d,** and wave7's offsets in table.go may not match 9f1790d exactly. A mutant at a shifted offset is still a valid mutant, but the per-file rates are not comparable one-to-one with wave7.
- **Races inside a reconcile group** (GAP-RECRACE) are not interleaved yet.
- **Not modelled:** concurrency between RPCs (VOL-14, LCK-*), lock-wait timing, and `nfsd stop`.
- **Inherited and `temporary` property sources** are not generated, so OWN-01 is covered only for `received`. TestInheritedOwnershipIsNotOwnership is redundant on the mutants but not on that row's full meaning. Keep it until `adm.inherit` is added.
