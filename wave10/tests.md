# wave10: a faster test suite (branch `speed-tests`)

Base: b5c4ec4 (ADR 0005 on 8cc52ef). Head: 0548c7a. 12 commits, local only, not pushed. Go 1.27.3.

The host had 4 vCPUs and a load average of 50-80 from sibling agents the whole time. Wall times are therefore 2-5x inflated and noisy. **CPU seconds (user+sys of the go command and its children) are the load-independent number**; sleeps are the only part of wall time that load does not inflate. The sibling's `wave10/timing.md` (measured on 8cc52ef) confirms: compile+link are ~70% of a warm plain run, and the tests themselves are ~2.7 CPU-s plain, ~11.5 CPU-s race.

## 1. Before and after

`wave10/bench2.sh`, warm build cache, `-count=1`, three interleaved runs per tree (before, after, before, after), raw output in `wave10/bench-final.txt`. Medians, with the minimum wall in brackets:

| Phase | before (b5c4ec4) wall | after (0548c7a) wall | before CPU | after CPU | CPU change |
|---|---|---|---|---|---|
| `go test ./...` | 18.5 s (12.2) | 12.3 s (11.4) | 9.12 s | 7.44 s | −18% |
| `go test -race ./...` | 36.0 s (33.8) | 25.5 s (22.7) | 20.28 s | 15.58 s | −23% |
| race on endpoint, server, zfs, lock, cmd/zfs-endpoint + plain csi, unixsock | 39.9 s (33.9) | 28.4 s (26.4) | 18.93 s | 15.09 s | |
| build+link only (`-run XXXNONE`) | 10.4 s (8.8) | 12.8 s (9.2) | 6.11 s | 5.73 s | −6% (one test binary fewer) |
| `go vet ./...` (warm) | 1.5-2.6 s | 1.6-2.9 s | 0.87 s | 1.0 s | same |

What this means:
- **Test execution** (total minus build+link, CPU): plain ~3.0 s → ~1.7 s; race ~14.2 s → ~9.9 s. Wall time spent sleeping fell from ~6.7 s (2.58 + 2.49 + 1.6 s on the critical paths of three packages) to ~0.1 s.
- **On this loaded host, `go test ./...` is now bounded by compile+link** (~6 CPU-s across 8 test binaries), not by tests. On an idle 4-core machine that is a few seconds of wall; the longest package (server) is now ~2 s instead of ~4.3 s.
- **CI unit step** (37 s today, `-race`): the race CPU drop (−4.7 CPU-s, ~−23%) plus no sleeps on the server critical path should bring it to about 25-28 s on the runner. The remaining cost is race compile+link and `TestMessageSizes` under race (server package ~6 s in CI). Reaching under 20 s needs the `speed-ci` cache work (restored race build cache), not more test changes.
- **Race scoping (Q6) does not pay at warm cache**: the subset + plain run was the same CPU as full race after the changes (15.1 vs 15.6 s), because csi and unixsock tests are cheap and their race builds are cached. It saves only race *compilation* of grpc/ginkgo for the csi binary on a cold cache. So the recommendation is: keep `-race ./...` and invest in the build cache.

Per-package test wall (`go test -json -count=1 ./...`, package Elapsed, same load):

| Package | before (b5c4ec4) | after (0548c7a) | What changed |
|---|---|---|---|
| internal/server | 4.28 s | 2.04 s | Shutdown test: 2.58 s of real drain/stop timers → 5-20 ms injected durations |
| internal/endpoint | 2.88 s | 2.42 s | equivalence test 2.49 s → 1000 seeds (~0.1 s), synctest commit race, MiB zvols, t.Parallel; but +crash table (110 subtests), +golden, +props round trip |
| cmd/zfs-endpoint | 1.78 s | 0.35 s | parent-wait backoff 1.6 s of real sleeping → injected timer |
| internal/zfs | 0.69 s | 0.27 s | Limit tests in synctest; WaitDelay 200 → 50 ms; t.Parallel |
| internal/lock | 0.63 s | 0.01 s | all waiting tests in synctest bubbles |
| internal/csi | 0.85 s | 0.43 s | (load) |
| internal/unixsock | 0.75 s | 0.11 s | (load) |
| cmd/zfs-csi | 0.02 s, grpc-linked test binary | no test binary | its one test moved to unixsock (`unixsock.Target`) |

Sum of top-level in-test time went from 9.87 s to ~4.9 s, and **no test sleeps on a real clock any more except `TestKilledCommandIsInterrupted`** (it execs a real process; 50 ms WaitDelay).

Checks on the final tree (0548c7a):
- `go vet ./...` clean; `gofmt -l .` empty.
- `go test -race -count=1 -shuffle=on ./...` green on 0548c7a; the new Shutdown test was also run 50x under race.
- **Skip budget.** Every one of the 17 entries of `ci/expected-skips.txt` still exists, all in internal/endpoint. With `ZFS_TEST_POOL=nopool` (the real-leg switch), all 17 report `--- SKIP` and nothing else skips. On b5c4ec4 the same command made all 17 **fail** on a box without zfs: they created a dataset on the pool before `h.Sim(t)` skipped. On the VM, which has a pool, both trees skip all 17, so the semantics are unchanged; the after tree just skips first (`requireSim(t)` as the first line; commit 0548c7a for the last five export tests). No test was added to or removed from the list.
- csi-sanity on the simulator: TestSanity on 0548c7a reports "Ran 44 of 103 Specs" (floor 44 met; the VM runs the mount and block binaries separately).

## 2. The changes, commit by commit

1. **bc02b56 tests: port the wave7 suite redesign onto HEAD.** `wave7/suite-data/suite-copy.diff` adapted to HEAD:
   - crash-injection table (`crash_test.go`, 17 scenarios, every mutating zfs call k crashed before/after, restart, retry, compare with uninterrupted, idle re-run must make 0 mutations; real-pool capable via `faultRunner`);
   - golden plans (`golden_plan_test.go`, `testdata/plans.golden`, `-update` after the package path). `Plan.String` lives in the new `plan_string.go`, so `plan.go` (owned by `fixes-mutation`) is untouched;
   - `props_roundtrip_test.go`; `internal/leakcheck` + TestMain goroutine-leak gate in csi, endpoint, server, zfs; `requireSim` (skip outside a synctest bubble, because a `t.Skip` inside one reports PASS); `t.Parallel` across endpoint and csi; Ginkgo `-count` guard in the sanity test.
   - **The `delete-clone-releases-source` xfail rows were dropped**: HEAD 07c3f98 destroys the deferred origin first, so the wave7 crash window no longer diverges; all 93 crash runs converge and the ratchet would fail if they did not. No xfail remains; the fix for any further crash window stays with `fixes-mutation`.
   - Golden regenerated for HEAD (no `+recursive`, stamped clone-source props, explicit `destroy a@s`, origin first); export fixtures use canonical `/32` clients.
2. **3527989 server: Shutdown is bounded by stopWait even when a handler is stuck.** A real bug found by making the test fast: grpc 1.84 `Server.Stop()` blocks until every handler returns, so a handler stuck in an unkillable zfs held Shutdown past `stopWait` forever (the shortened test hung ~1 run in 30). Fix: `go d.GRPC.Stop()` then wait for idle or `stopWait`. Test: injected 5-20 ms durations, a `stuck` handler, `waitFor` polling observable state. 2.58 s → ~0.05 s; green 50x under race.
3. **ca6bdbc zfs-endpoint: inject the parent-wait backoff timer** (`deps.after`, nil = `time.After`). The test asserts the backoff sequence `[1s 2s]` instead of sleeping it. 1.6 s → 0.02 s.
4. **2ef4aaf lock: run the waiting tests in synctest bubbles.** InOrder, NoStarvation (now asserts `waited <= 9*200µs` in fake time), TryAcquire, CancelledWaiters. Kills became deterministic: a planted barging lock is killed 10/10 (was 4/5 at 2 s each); a planted LIFO handover is killed by InOrder.
5. **de36265 endpoint tests: exact interleaving for the commit race; MiB thick zvols.** `TestAdv4CommitRace…` in a bubble; thick zvols 64-192 MiB instead of GiB (VM pool is 10 G, and writes are faster).
6. **e6f23b1 ci(vm): endpoint tests 8-wide, the three binaries at once** (`-test.parallel=8`, endpoint/csi-mount/csi-block backgrounded with their own logs, every exit code collected). **Not yet run on the VM.**
7. **a3917b2 endpoint tests: fold 13 redundant tests** (the commit title says 12; it removes 13 functions) into golden rows and existing tables (list and proof in §4). Golden rows added: `create/incompatible-kind`, `create/thick-on-sparse-zvol`, `clone/malformed-source`. `TestPlanReconcile` gained an orphan/released-clone-source case and a per-row lock-cover loop.
8. **13a1758 endpoint tests: 1000 equivalence seeds per PR, the rest as a fuzz target** (`FuzzSimulateDestroyAllEquivalent`, seeds 0,1,2,5). Evidence: of 45 mutgen mutants of `simulateDestroyAll`, the latest first-failing seed is 5; 10 survive all 20000 seeds (`wave10/mut/seeds.txt`). 2.49 s → ~0.1 s.
9. **6b02032 zfs tests: Limit tests in synctest; WaitDelay 200 → 50 ms.** An off-by-one limit is now killed deterministically (peak 5 > 4).
10. **510e87c tests: concurrent creates proven serialized; t.Parallel where no globals.** `TestConcurrentCreatesSerialize` **never proved serialization** (verified on the old tree: a lock-free CreateVolume passed it) and flaked under race. Now: a runner parks the first `zfs create`, `synctest.Wait()` proves every other create is blocked, a planted lock-free mutant fails 5/5. `TestMessageSizes` builds its 1005 rows directly. `t.Parallel` in server, zfs, endpoint where no process-global state is touched.
11. **a77519f unixsock.Target.** The endpoint-target check moved out of `cmd/zfs-csi` into `unixsock.Target`, tested in unixsock. One grpc-linked test binary (352 packages to link) is gone.
12. **0548c7a endpoint tests: the five sim-only export tests skip before touching a pool.**

## 3. Mutation results

Method: full kill sets (no `-failfast`, every failing top-level test) of 303 mutants on HEAD offsets (`wave10/mut/mutants-head.json`: 245 wave7 mutants re-anchored by context, 58 fresh mutgen samples on code wave7 never reached, ids ≥10000), run on the before tree (b5c4ec4). A killed mutant can change status only if **every** test that killed it was changed or removed (or it had an empty kill set, i.e. a panic truncated it). Those, and only those, were re-run on the after tree.

| | before (b5c4ec4) | after (0548c7a) |
|---|---|---|
| mutants | 303 (5 do not build) | same |
| killed | 229 | **229** |
| survived | 69 | 69 |
| kill rate (of 298 that build) | 76.8% | **76.8%** |
| killed before, survived after | | **0** |

- **Targeted re-run**: 9 mutants had a before kill set lying wholly in changed or removed tests (or an empty one: a timeout or a panic). All 9 are still killed on the after tree (`wave10/mut/after-t.jsonl`). Two are kills that were moved, not lost:
  - 203 (`plan.go:243`): before, only `TestPlanCreateMalformedSourceIsNotFound`; after, `TestGoldenPlans`.
  - 10590, 10592, 10634 (reconcile planner): before and after, `TestPlanReconcile`, which was rewritten (orphan-source row plus lock-cover loop).
- The other 220 killed mutants each keep at least one killing test that this branch did not touch, so they cannot change status.
- **All 69 survivors re-run on the after tree: all 69 still survive** (`after-s.w*.jsonl`). The new golden rows, crash table and props round trip did not catch any of them. The survivors are in `plan.go` (20), `zfs/errors.go` (19), `table.go` (10), `zfs.go` (7), `props.go` (6), `reconcile.go` (5) and `endpoint.go` (2). Writing kill tests for them is `fixes-mutation`'s work (wave7 `*mutation_kill_test.go`), and I deliberately did not port those.
- So the mutation score is held, not raised. The gains are in determinism (next paragraph), which mutgen sampling on one run does not measure.
- Data:
  - per-mutant fail sets: `wave10/mut/before.w*.jsonl`;
  - mutant list: `wave10/mut/mutants-head.json`;
  - runner: `wave10/mut/mutrun.py` (`MUT_IDS` filters the ids);
  - target selection: `wave10/mut/targeted.py`.

Additional deterministic kills that the before tree only got by luck or not at all (planted, not in the mutgen set): lock barging (4/5 → 10/10), lock-free CreateVolume (0 → 5/5), Limit off-by-one (probabilistic → always), two lock-cover mutants in the reconcile planner (2 rows each), Shutdown not bounded when a handler is stuck (hang → fail).

## 4. Per spec row: which test kills it now

`wave8/spec.json`, 175 rows. Generated by `wave10/map_rows.py` (before tree vs after tree). Only 21 rows cited a removed test; all are listed. **No row lost its last covering test.** The three rows whose only test was removed have a replacement that kills the same mutants (§3):

| row | status | covering tests now | change |
|---|---|---|---|
| OWN-05 | current | TestNeverAdoptsOrDeletesUnmanaged, TestGoldenPlans/create/foreign, create/incompatible-* | removed: TestPlanCreateRefusesForeignAndIncompatible |
| OWN-11 | current | TestCloneFromVolumeReleasesInternalSnapshot | removed: TestDeleteVolumeAfterItsCloneIsGone; SOLE killer replaced |
| VOL-01 | current | FuzzCreateConverges, TestCreateVolumeIdempotent, TestCreateVolumeProperties, TestReserveRestoreIsIdempotent, TestRoundTripAndCodes, TestThickCloneRetry | removed: TestThickCloneRetryIsIdempotent |
| VOL-03 | current | TestGoldenPlans/create/fs-limit-only | removed: TestPlanCreateHonorsLimitOnly; SOLE killer replaced |
| VOL-05 | current | TestCreateVolumeIdempotent, TestParametersAndAccessChecks, TestRoundTripAndCodes, TestGoldenPlans/create/foreign, create/incompatible-* | removed: TestPlanCreateRefusesForeignAndIncompatible |
| VOL-07 | current | TestCreateVolumeIdempotent, TestCreateVolumeResumesInterruptedProvision, TestGoldenPlans/create/uncommitted-clone-converges | removed: TestPlanCreateConvergesUncommittedClone |
| VOL-08 | current | TestCreateVolumeResumesInterruptedProvision, TestPlanCreatePropertiesConvergeUncommitted, TestCreateVolumeProperties, TestGoldenPlans/create/uncommitted-clone-converges | removed: TestPlanCreateConvergesUncommittedClone |
| VOL-10 | current | TestPlanCreateAppliesProperties, TestGoldenPlans/create/fs | removed: TestPlanCreateFilesystemsAreNotShared |
| VOL-11 | current | TestGoldenPlans/create/zvol-thick, create/thick-on-sparse-zvol, TestGoldenPlans/create/uncommitted-clone-converges | removed: TestPlanCreateThickZvolLeavesReservationToZFS, TestPlanCreateConvergesUncommittedClone; SOLE killer replaced |
| CLN-02 | current | TestCloneFromVolumeReleasesInternalSnapshot, TestReconcileCollectsLeakedCloneSource, TestGoldenPlans/delete/* | removed: TestPlanDeleteVolume |
| CLN-05 | current | TestCloneFailureAfterCloneConvergesToSameState, TestCloneResizeFailureConverges, TestGoldenPlans/create/uncommitted-clone-converges | removed: TestPlanCreateConvergesUncommittedClone |
| CLN-08 | current | TestSnapshotLifecycleWithClones, TestGoldenPlans/restore/malformed-source, clone/malformed-source | removed: TestPlanCreateMalformedSourceIsNotFound |
| DEL-01 | current | TestDeleteVolume, TestRoundTripAndCodes, TestGoldenPlans/delete/* | removed: TestPlanDeleteVolume |
| DEL-02 | current | TestDeleteVolume, TestRoundTripAndCodes, TestGoldenPlans/delete/* | removed: TestPlanDeleteVolume |
| DEL-03 | current | TestCloneFromVolumeReleasesInternalSnapshot, TestGoldenPlans/delete/* | removed: TestPlanDeleteVolume |
| DEL-04 | current | TestPlanReconcile, TestGoldenPlans/delete/* | removed: TestPlanDeleteVolume |
| SNP-05 | current | TestGoldenPlans/snapshot/reserved-prefix | removed: TestPlanSnapshotReservedPrefix; SOLE killer replaced |
| EXP-01 | current | TestExpand, TestGoldenPlans/expand/* | removed: TestPlanExpand |
| EXP-03 | current | TestExpandUnlimitedFilesystem, TestGoldenPlans/expand/* | removed: TestPlanExpand |
| ID-01 | current | TestNeverIssuedIDs, TestSnapshotLifecycleWithClones, TestGoldenPlans/restore/malformed-source, clone/malformed-source | removed: TestPlanCreateMalformedSourceIsNotFound |
| REC-10 | current | TestReconcileLocksTargets, TestPlanReconcileHeldLimitsActions, TestPlanReconcile (lock-cover loop) | removed: TestPlanReconcileLocksEveryTarget |

The other 154 rows are unchanged: their cited tests all still exist (renames none). 57 rows cite no test (open, decided-pending or untested), as before; INV-01 cites a file glob, not a test name.

Why each fold is safe (wave7 §1.3 plus my check):
- The ten `TestPlan*` planner tests are each one or a few (Table, request) → plan/code cases, all reproduced byte for byte as golden rows; mutant 203 (`plan.go:243`, killed before only by TestPlanCreateMalformedSourceIsNotFound) is killed after by TestGoldenPlans.
- `TestPlanReconcileLocksEveryTarget` → a lock-cover loop over **every** TestPlanReconcile row (was one fixture). Finding while doing it: `ReasonWithVolume` actions do not lock the named clone; safe because CreateVolume locks `vol:<source>`. The loop checks the clone lock only for `ReasonReleasedWithVolume` and `ReasonOrphanCloneSource`.
- `TestDeleteVolumeAfterItsCloneIsGone` → TestCloneFromVolumeReleasesInternalSnapshot runs the same steps and asserts more.
- `TestThickCloneRetryIsIdempotent` → TestThickCloneRetry plus crash row `clone-volume-zvol-thick` (idle re-run = 0 mutations).

## 5. Deliberately not done

- **Only 13 of wave7's ~16 folds.** Kept:
  - `TestCloneResizeFailureConverges`, `TestCloneFailureAfterCloneConvergesToSameState`: listed in `ci/expected-skips.txt`, and the brief requires every listed test to keep existing with the same skip semantics. The crash table now covers the same convergence on the real pool, so the right follow-up is to delete them **and** their two skip-budget lines in one reviewed change.
  - `TestSimulateDestroyReapsDeferredOrigins`: a literal oracle. The adversary's C1 risk is that model, sim and planner agree on the same wrong thing; a hand-written expected result is the independent witness. Folding it into golden rows generated by the code under test would remove that.
  - `TestReconcileCollectsAbandonedCreate` (3 unique statements on the real leg via the dry run), `TestScanRecordsCoverage`, `TestClassify`/`FuzzClassify` (wave7 conditioned this on a corpus ratchet, not built).
- **No DeleteVolume crash-window fix**: owned by `fixes-mutation` (plan.go). It turned out to be fixed already at HEAD (07c3f98).
- **No wave7 `*mutation_kill_test.go` ports**: `fixes-mutation` does that.
- **`.github/workflows/ci.yml` not edited.** `speed-ci` rewrites the same file (its test step is still `go test -race -count=1 ./...`). Race scoping was measured (§1) and saves nothing at warm cache, so I do not propose it. The sibling's timing.md measured `GORACE=atexit_sleep_ms=0` saving ~1 s per race binary wall; that one is worth adding on `speed-ci`.
- **csi-sanity over bufconn / in parallel.** The endpoint side already uses bufconn; csi-sanity dials a unix socket that costs microseconds. Ginkgo allows one suite per process, so parallelism comes from separate processes: the VM script now runs mount and block concurrently (commit 6). Spec floor 44 unchanged.
- **`t.Parallel` not added** where tests touch process-global state: unixsock and cmd/zfs-endpoint (`unixsock.Listen` sets the process umask around bind), `TestRequestIDReachesTheAuditLog` (`slog.SetDefault`), `TestServeHealthNotifyAndStop` (`t.Setenv`), `TestSanity` (Ginkgo). Adding it was tried and reverted.
- **`TestKilledCommandIsInterrupted` keeps real time**: it execs; synctest cannot bubble a child process. WaitDelay shortened only.
- **`TestMessageSizes` kept** (~0.9 s): the 100k-id Reconcile through gRPC is its purpose.
- **Crash table cost on the VM is unmeasured.** It runs on the real pool (93 runs, ~3 ops each). Wave7 modelled −4.2 s at parallel 8 against the two sim tests it replaces, but run the VM once before relying on commit 6's numbers.
- No full after mutation run of all 303 mutants (about 60 s per mutant under this load). It is unnecessary by construction: §3 re-ran every mutant whose status could change.

## 6. ADR 0005: where it should change

- **"`t.Parallel` by default"** needs a stated exception: tests that touch process-global state (umask, `slog.SetDefault`, `Setenv`, Ginkgo's one suite per process, signal handlers) stay serial, with a one-line comment saying which global. Otherwise someone will "fix" them and get rare cross-test failures (umask leaks into a parallel test's temp files).
- **"No real waiting"** should say *synctest where no exec is involved; injected durations of a few ms plus polling on observable state where real goroutines or grpc must run*; and that a `t.Skip` inside a synctest bubble reports PASS (call `requireSim` outside the bubble), and that a failing bubbled test must release its goroutines before `Fatal`.
- **"Assert at the lowest level that proves the row"** needs a guard: keep at least one **independent, hand-written oracle** per planner area. Golden files regenerated by `-update` from the code under test are a change detector, not an oracle (adversary C1).
- **Fast tests found bugs.** Shortening timers exposed the grpc `Stop()` hang; making the create race deterministic exposed that `TestConcurrentCreatesSerialize` never tested serialization. The ADR could require that a test made faster is re-proved with a planted mutant (as done in §3), since "same assertions" is not enough: a test that never actually waited can pass for the wrong reason.
- **Mutation scope**: wave7's sampler stopped at reconcile.go line 296, so `simulateDestroyAll` was never mutated. The nightly mutant sample in the ADR should be whole-file.
- **Expected-skips**: suggest every sim-only test calls `requireSim` first (no pool access before the skip), and that ci/check-skips.sh could verify the listed tests skip with zero zfs calls.

## Appendix: all 175 spec rows, covering tests on the after tree

Covering = tests the row cites in wave8/spec.json that exist on 0548c7a, plus the replacement for any removed one. "-" = the row cites no test (unchanged from before).

| row | status | covering tests now | change |
|---|---|---|---|
| OWN-01 | current | TestPropLocalIsOnlyLocal, TestInheritedOwnershipIsNotOwnership, TestReceivedCopyIsNotOwned |  |
| OWN-02 | current | TestVolumeNameMustMatchID, TestReconcileSparesReceivedAndRenamed |  |
| OWN-03 | open-finding | - |  |
| OWN-04 | current | TestInheritedOwnershipIsNotOwnership, TestReceivedCopyIsNotOwned, TestDeleteVolumeSparesForeignSnapshots |  |
| OWN-05 | current | TestNeverAdoptsOrDeletesUnmanaged, TestGoldenPlans/create/foreign, create/incompatible-* | removed: TestPlanCreateRefusesForeignAndIncompatible |
| OWN-06 | current | TestReceivedCopyIsNotOwned, TestReconcileSparesReceivedAndRenamed, FuzzReconcileSafe |  |
| OWN-07 | current | TestCloneSourcesMustBeOwned |  |
| OWN-08 | current | TestCloneNeverAdoptsARacingDataset |  |
| OWN-09 | current-untested | - |  |
| OWN-10 | current | TestDeleteVolumeSparesForeignSnapshots |  |
| OWN-11 | current | TestCloneFromVolumeReleasesInternalSnapshot | removed: TestDeleteVolumeAfterItsCloneIsGone; SOLE killer replaced |
| OWN-12 | open-finding | - |  |
| OWN-13 | open-finding | - |  |
| OWN-14 | open-finding | - |  |
| OWN-15 | open-finding | - |  |
| OWN-16 | current | TestDefaultPropertyPrefixIsOurOwn, TestPropertyPrefixIsValidated |  |
| OWN-17 | current | TestFileExportAdoptedVolumes, TestCloneNeverAdoptsARacingDataset |  |
| VOL-01 | current | FuzzCreateConverges, TestCreateVolumeIdempotent, TestCreateVolumeProperties, TestReserveRestoreIsIdempotent, TestRoundTripAndCodes, TestThickCloneRetry | removed: TestThickCloneRetryIsIdempotent |
| VOL-02 | current | TestCreateVolumeIdempotent, TestPlanCreateAppliesProperties, FuzzCreateConverges |  |
| VOL-03 | current | TestGoldenPlans/create/fs-limit-only | removed: TestPlanCreateHonorsLimitOnly; SOLE killer replaced |
| VOL-04 | current | TestPlanCreateAppliesProperties |  |
| VOL-05 | current | TestCreateVolumeIdempotent, TestParametersAndAccessChecks, TestRoundTripAndCodes, TestGoldenPlans/create/foreign, create/incompatible-* | removed: TestPlanCreateRefusesForeignAndIncompatible |
| VOL-06 | open-finding | - |  |
| VOL-07 | current | TestCreateVolumeIdempotent, TestCreateVolumeResumesInterruptedProvision, TestGoldenPlans/create/uncommitted-clone-converges | removed: TestPlanCreateConvergesUncommittedClone |
| VOL-08 | current | TestCreateVolumeResumesInterruptedProvision, TestPlanCreatePropertiesConvergeUncommitted, TestCreateVolumeProperties, TestGoldenPlans/create/uncommitted-clone-converges | removed: TestPlanCreateConvergesUncommittedClone |
| VOL-09 | open-finding | - |  |
| VOL-10 | current | TestPlanCreateAppliesProperties, TestGoldenPlans/create/fs | removed: TestPlanCreateFilesystemsAreNotShared |
| VOL-11 | current | TestGoldenPlans/create/zvol-thick, create/thick-on-sparse-zvol, TestGoldenPlans/create/uncommitted-clone-converges | removed: TestPlanCreateThickZvolLeavesReservationToZFS, TestPlanCreateConvergesUncommittedClone; SOLE killer replaced |
| VOL-12 | current | TestLifecycleAgainstSim, TestParametersAndAccessChecks |  |
| VOL-13 | current | TestReserveRestoreIsIdempotent, TestCloneFailureAfterCloneConvergesToSameState |  |
| VOL-14 | current | TestConcurrentCreatesSerialize |  |
| VOL-15 | current-untested | - |  |
| VOL-16 | current | TestCreateVolumeProperties, TestPlanCreateAppliesProperties |  |
| VOL-17 | current | TestPlanCreatePropertiesConvergeUncommitted |  |
| VOL-18 | current | TestRoundTripAndCodes, TestFileExportLifecycle |  |
| VOL-19 | open-finding | - |  |
| CLN-01 | current | TestSnapshotLifecycleWithClones |  |
| CLN-02 | current | TestCloneFromVolumeReleasesInternalSnapshot, TestReconcileCollectsLeakedCloneSource, TestGoldenPlans/delete/* | removed: TestPlanDeleteVolume |
| CLN-03 | current | TestCloneLargerThanLimitIsOutOfRange |  |
| CLN-04 | current | TestReserveRestoreIsIdempotent |  |
| CLN-05 | current | TestCloneFailureAfterCloneConvergesToSameState, TestCloneResizeFailureConverges, TestGoldenPlans/create/uncommitted-clone-converges | removed: TestPlanCreateConvergesUncommittedClone |
| CLN-06 | current-untested | - |  |
| CLN-07 | current-untested | - |  |
| CLN-08 | current | TestSnapshotLifecycleWithClones, TestGoldenPlans/restore/malformed-source, clone/malformed-source | removed: TestPlanCreateMalformedSourceIsNotFound |
| CLN-09 | current-untested | - |  |
| DEL-01 | current | TestDeleteVolume, TestRoundTripAndCodes, TestGoldenPlans/delete/* | removed: TestPlanDeleteVolume |
| DEL-02 | current | TestDeleteVolume, TestRoundTripAndCodes, TestGoldenPlans/delete/* | removed: TestPlanDeleteVolume |
| DEL-03 | current | TestCloneFromVolumeReleasesInternalSnapshot, TestGoldenPlans/delete/* | removed: TestPlanDeleteVolume |
| DEL-04 | current | TestPlanReconcile, TestGoldenPlans/delete/* | removed: TestPlanDeleteVolume |
| DEL-05 | current | TestSnapshotLifecycleWithClones, TestLifecycleAgainstSim, TestSimulateDestroyReapsDeferredOrigins |  |
| DEL-06 | open-finding | - |  |
| DEL-07 | current | TestBusyIsMappedByOperation |  |
| DEL-08 | open-finding | - |  |
| SNP-01 | current | TestSnapshotLifecycleWithClones, TestPlanSnapshotLongAndUnusualNames, TestReceivedCopyIsNotOwned |  |
| SNP-02 | current | TestSnapshotLifecycleWithClones, TestPlannersRequireACoveringTable |  |
| SNP-03 | current | TestPlanSnapshotLongAndUnusualNames |  |
| SNP-04 | current | TestPlanSnapshotLongAndUnusualNames |  |
| SNP-05 | current | TestGoldenPlans/snapshot/reserved-prefix | removed: TestPlanSnapshotReservedPrefix; SOLE killer replaced |
| SNP-06 | current | TestSnapshotLifecycleWithClones |  |
| SNP-07 | current | TestSnapshotLifecycleWithClones, TestPlannersRequireACoveringTable |  |
| SNP-08 | current | TestInheritedOwnershipIsNotOwnership, TestReceivedCopyIsNotOwned |  |
| SNP-09 | current | TestSnapshotLifecycleWithClones, TestInheritedOwnershipIsNotOwnership, TestReceivedCopyIsNotOwned |  |
| SNP-10 | current-untested | - |  |
| SNP-11 | current | TestSnapshotLifecycleWithClones |  |
| SNP-12 | current | TestNeverIssuedIDs |  |
| SNP-13 | decided-pending | - |  |
| SNP-14 | current | TestVolumeNameMustMatchID |  |
| EXP-01 | current | TestExpand, TestGoldenPlans/expand/* | removed: TestPlanExpand |
| EXP-02 | current | TestExpand |  |
| EXP-03 | current | TestExpandUnlimitedFilesystem, TestGoldenPlans/expand/* | removed: TestPlanExpand |
| EXP-04 | current | TestExpandNodeExpansionRequired |  |
| EXP-05 | current | TestDecodeRejectsImpossibleZvolSizes |  |
| LST-01 | current | TestPagination, TestRoundTripAndCodes |  |
| LST-02 | current | TestCreateVolumeResumesInterruptedProvision, TestVolumeNameMustMatchID, TestReceivedCopyIsNotOwned |  |
| LST-03 | current | TestRoundTripAndCodes |  |
| LST-04 | open-finding | - |  |
| ID-01 | current | TestNeverIssuedIDs, TestSnapshotLifecycleWithClones, TestGoldenPlans/restore/malformed-source, clone/malformed-source | removed: TestPlanCreateMalformedSourceIsNotFound |
| ERR-01 | current | TestExistsDoesNotSwallowUnknownErrors, TestClassify |  |
| ERR-02 | current | TestMissingParentIsUnavailableNotAbsent, TestMissingParentIsUnavailableForWholeScans |  |
| ERR-03 | current | TestClassify, FuzzClassify |  |
| ERR-04 | current | TestZfsErrCodes |  |
| ERR-05 | current | TestBusyIsMappedByOperation |  |
| ERR-06 | current | TestUnknownFailureAfterAMutationIsRetryable |  |
| ERR-07 | current | TestKilledCommandIsInterrupted, TestZfsErrCodes |  |
| ERR-08 | current | TestReconcileLocksTargets |  |
| ERR-09 | current | TestApplyRefusesAnUnknownOpKind, TestOpKindString |  |
| ERR-10 | current | TestPlanReconcile, FuzzReconcileSafe, TestCloneNeverAdoptsARacingDataset |  |
| ERR-11 | current | TestUnknownFailureAfterAMutationIsRetryable |  |
| ERR-12 | open-finding | - |  |
| ERR-13 | decided-pending | - |  |
| SCN-01 | current | TestParseColumnsRejectsForgedLines, FuzzParseColumns |  |
| SCN-02 | open-finding | - |  |
| SCN-03 | current | FuzzParseColumns |  |
| SCN-04 | decided-pending | - |  |
| SCN-05 | current | TestDecodeRejectsImpossibleZvolSizes |  |
| SCN-06 | open-finding | - |  |
| SCN-07 | decided-pending | - |  |
| SCN-08 | open-finding | - |  |
| SCN-09 | open-finding | - |  |
| SCN-10 | current | TestPlannersRequireACoveringTable, TestScanRecordsCoverage |  |
| SCN-11 | open-finding | - |  |
| SCN-12 | current | TestRejectsFlagLikeAndShellNames |  |
| SCN-13 | current | TestSimPrintsIndexPropertiesLikeOpenZFS |  |
| SCN-14 | current | TestLifecycleAgainstSim |  |
| PRP-01 | current | TestPlanCreatePropertiesDenyList, TestCreateVolumeProperties, FuzzCreateConverges, TestRoundTripAndCodes |  |
| PRP-02 | current | TestPlanCreatePropertiesDenyList |  |
| PRP-03 | current | TestValidatePropertyValues, TestSimPrintsIndexPropertiesLikeOpenZFS |  |
| PRP-04 | current | TestPlanCreateAppliesProperties, TestCreateVolumeProperties, FuzzCreateConverges |  |
| PRP-05 | current | TestPlanCreatePropertiesRetry, TestRoundTripAndCodes |  |
| PRP-06 | current | TestPlanCreatePropertiesConvergeUncommitted, TestCreateVolumeProperties |  |
| PRP-07 | decided-pending | - |  |
| PRP-08 | current | TestParametersAndAccessChecks |  |
| NFS-01 | decided-pending | - |  |
| NFS-02 | decided-pending | TestShareNFSAndClientEncoding |  |
| NFS-03 | current | TestPlanPublishExport, TestFileExportAdoptedVolumes, TestPlanUnpublishExport |  |
| NFS-04 | current | TestPlanUnpublishExport, TestFileExportLifecycle, TestExportConverges |  |
| NFS-05 | current | TestFileExportVerifiesKernelTable, TestPlanPublishExport, TestPlanUnpublishExport |  |
| NFS-06 | decided-pending | - |  |
| NFS-07 | current | TestFileExportLifecycle, TestPlanPublishExport, TestPlanUnpublishExport, TestExportConverges, TestFileExport |  |
| NFS-08 | open-finding | TestParseClient, TestFileExportPreconditions, TestFileExport |  |
| NFS-09 | open-finding | TestParseEtab |  |
| NFS-10 | current | TestFileExportPreconditions, TestPlanPublishExport |  |
| NFS-11 | open-finding | TestPlanUnpublishExport |  |
| NFS-12 | open-finding | - |  |
| NFS-13 | current | TestFileExportPreconditions, TestFileExport |  |
| NFS-14 | decided-pending | - |  |
| NFS-15 | decided-pending | - |  |
| NFS-16 | current | TestFileExportAdoptedVolumes, TestPlanPublishExport |  |
| NFS-17 | decided-pending | - |  |
| NFS-18 | decided-pending | - |  |
| NFS-19 | decided-pending | - |  |
| NFS-20 | decided-pending | - |  |
| NFS-21 | current | TestFileExportLifecycle |  |
| NFS-22 | decided-pending | TestFileExport |  |
| REC-01 | current | TestPlanReconcile, FuzzReconcileSafe |  |
| REC-02 | current | FuzzReconcileSafe, TestPlanReconcile, TestReconcileSparesReceivedAndRenamed, TestReconcileAgainstDesiredSet |  |
| REC-03 | current | TestPlanReconcile, TestReconcileAgainstDesiredSet, FuzzReconcileSafe |  |
| REC-04 | open-finding | - |  |
| REC-05 | current | TestReconcileCollectsAbandonedCreate, TestPlanReconcile |  |
| REC-06 | open-finding | - |  |
| REC-07 | current | TestPlanReconcile, TestReconcileAgainstDesiredSet, TestSimulateDestroyReapsDeferredOrigins |  |
| REC-08 | current | TestPlanReconcile, TestReconcileCollectsLeakedCloneSource |  |
| REC-09 | current | TestReconcileCollectsAbandonedCreate, TestReconcileLocksTargets, FuzzReconcileSafe |  |
| REC-10 | current | TestReconcileLocksTargets, TestPlanReconcileHeldLimitsActions, TestPlanReconcile (lock-cover loop) | removed: TestPlanReconcileLocksEveryTarget |
| REC-11 | current | TestReconcileCollectsAbandonedCreate, TestReconcileAgainstDesiredSet, FuzzReconcileSafe |  |
| REC-12 | open-finding | - |  |
| REC-13 | current | TestReconcileAgainstDesiredSet, TestReconcile |  |
| REC-14 | current | TestPlanReconcile, TestReconcile, TestDeleteVolumeSparesForeignSnapshots |  |
| REC-15 | current-untested | - |  |
| LCK-01 | current | TestConcurrentCreatesSerialize, TestReconcileLocksTargets |  |
| LCK-02 | decided-pending | - |  |
| LCK-03 | current | TestLockParentIsExclusive |  |
| RT-01 | current | TestListenIsOwnerOnly, TestListenRefusesALiveSocket, TestListenReplacesAStaleSocket, TestListenNeverDeletesANonSocket |  |
| RT-02 | decided-pending | - |  |
| RT-03 | decided-pending | - |  |
| RT-04 | decided-pending | - |  |
| INV-01 | current | (not a Go test name: (all plan_test.go, props_test.go, export_test.go, reconcile_test.go tests)) |  |
| INV-02 | decided-pending | FuzzCreateConverges, TestExportConverges |  |
| INV-03 | decided-pending | - |  |
| CR-01 | current | TestCloneFailureAfterCloneConvergesToSameState, TestCreateVolumeResumesInterruptedProvision |  |
| CR-02 | current-untested | - |  |
| CR-03 | current-untested | - |  |
| CR-04 | current-untested | - |  |
| CSI-01 | current | TestCapabilitiesAdvertised |  |
| CSI-02 | current | TestParametersAndAccessChecks, TestNeverIssuedIDs |  |
| CSI-03 | current | TestExpandNodeExpansionRequired |  |
| CSI-04 | current | TestSanity |  |
| CSI-05 | current-untested | - |  |
| CSI-06 | open-finding | - |  |
| PRF-01 | current-untested | - |  |
| PRF-02 | decided-pending | - |  |
| PRF-03 | decided-pending | - |  |
| PRF-04 | decided-pending | - |  |
| PRF-05 | decided-pending | - |  |
| PRF-06 | decided-pending | - |  |
