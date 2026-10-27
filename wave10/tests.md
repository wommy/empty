# wave10: a faster test suite (branch `speed-tests`)

Base: b5c4ec4 (ADR 0005 on 8cc52ef). Head: 0548c7a. 13 commits, local only, not pushed. Go 1.27.3.

The host had 4 vCPUs and a load average of 50-80 from sibling agents the whole time. Wall times are therefore 2-5x inflated and noisy. **CPU seconds (user+sys of the go command and its children) are the load-independent number**; sleeps are the only part of wall time that load does not inflate. The sibling's `wave10/timing.md` (measured on 8cc52ef) confirms: compile+link are ~70% of a warm plain run, and the tests themselves are ~2.7 CPU-s plain, ~11.5 CPU-s race.

## 1. Before and after

TIMINGS

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
- `go test -race -count=1 ./...` green (also green 50x for the server package, 10x for lock and endpoint `-shuffle=on`).
- **Skip budget.** Every one of the 17 entries of `ci/expected-skips.txt` still exists, all in internal/endpoint. With `ZFS_TEST_POOL=nopool` (the real-leg switch), all 17 report `--- SKIP` and nothing else skips. On b5c4ec4 the same command made all 17 **fail** on a box without zfs: they created a dataset on the pool before `h.Sim(t)` skipped. On the VM, which has a pool, both trees skip all 17, so the semantics are unchanged; the after tree just skips first (`requireSim(t)` as the first line; commit 0548c7a for the last five export tests). No test was added to or removed from the list.
- csi-sanity on the simulator: "Ran 44 of 103 Specs" for mount and block (floor 44 met).

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
7. **a3917b2 endpoint tests: fold 12 redundant tests** into golden rows and existing tables (list and proof in §4). Golden rows added: `create/incompatible-kind`, `create/thick-on-sparse-zvol`, `clone/malformed-source`. `TestPlanReconcile` gained an orphan/released-clone-source case and a per-row lock-cover loop.
8. **13a1758 endpoint tests: 1000 equivalence seeds per PR, the rest as a fuzz target** (`FuzzSimulateDestroyAllEquivalent`, seeds 0,1,2,5). Evidence: of 45 mutgen mutants of `simulateDestroyAll`, the latest first-failing seed is 5; 10 survive all 20000 seeds (`wave10/mut/seeds.txt`). 2.49 s → ~0.1 s.
9. **6b02032 zfs tests: Limit tests in synctest; WaitDelay 200 → 50 ms.** An off-by-one limit is now killed deterministically (peak 5 > 4).
10. **510e87c tests: concurrent creates proven serialized; t.Parallel where no globals.** `TestConcurrentCreatesSerialize` **never proved serialization** (verified on the old tree: a lock-free CreateVolume passed it) and flaked under race. Now: a runner parks the first `zfs create`, `synctest.Wait()` proves every other create is blocked, a planted lock-free mutant fails 5/5. `TestMessageSizes` builds its 1005 rows directly. `t.Parallel` in server, zfs, endpoint where no process-global state is touched.
11. **a77519f unixsock.Target.** The endpoint-target check moved out of `cmd/zfs-csi` into `unixsock.Target`, tested in unixsock. One grpc-linked test binary (352 packages to link) is gone.
12. **0548c7a endpoint tests: the five sim-only export tests skip before touching a pool.**

## 3. Mutation results

Method: full kill sets (no `-failfast`, every failing top-level test) of 303 mutants on HEAD offsets (`wave10/mut/mutants-head.json`: 245 wave7 mutants re-anchored by context, 58 fresh mutgen samples on code wave7 never reached, ids ≥10000), run on the before tree (b5c4ec4). A killed mutant can change status only if **every** test that killed it was changed or removed (or it had an empty kill set, i.e. a panic truncated it). Those, and only those, were re-run on the after tree.

MUTATION

Additional deterministic kills that the before tree only got by luck or not at all (planted, not in the mutgen set): lock barging (4/5 → 10/10), lock-free CreateVolume (0 → 5/5), Limit off-by-one (probabilistic → always), two lock-cover mutants in the reconcile planner (2 rows each), Shutdown not bounded when a handler is stuck (hang → fail).

## 4. Per spec row: which test kills it now

`wave8/spec.json`, 175 rows. Generated by `wave10/map_rows.py` (before tree vs after tree). Only 21 rows cited a removed test; all are listed. **No row lost its last covering test.** The three rows whose only test was removed have a replacement that kills the same mutants (§3):

| row | status | covering tests now | change |
|---|---|---|---|
ROWS

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
- **`.github/workflows/ci.yml` not edited.** `speed-ci` rewrites the same file (its test step is still `go test -race -count=1 ./...`). Proposed for that branch (race only where there is production concurrency; csi and unixsock have none):
  ```
  go test -race -count=1 ./internal/endpoint ./internal/server ./internal/zfs ./internal/lock ./cmd/zfs-endpoint
  go test -count=1 ./internal/csi ./internal/unixsock ./cmd/zfs-csi
  ```
  Measured saving in §1 ("race subset + plain rest"). Also from the sibling's timing.md: `GORACE=atexit_sleep_ms=0` saves ~1 s per race binary.
- **csi-sanity over bufconn / in parallel.** The endpoint side already uses bufconn; csi-sanity dials a unix socket that costs microseconds. Ginkgo allows one suite per process, so parallelism comes from separate processes: the VM script now runs mount and block concurrently (commit 6). Spec floor 44 unchanged.
- **`t.Parallel` not added** where tests touch process-global state: unixsock and cmd/zfs-endpoint (`unixsock.Listen` sets the process umask around bind), `TestRequestIDReachesTheAuditLog` (`slog.SetDefault`), `TestServeHealthNotifyAndStop` (`t.Setenv`), `TestSanity` (Ginkgo). Adding it was tried and reverted.
- **`TestKilledCommandIsInterrupted` keeps real time**: it execs; synctest cannot bubble a child process. WaitDelay shortened only.
- **`TestMessageSizes` kept** (~0.9 s): the 100k-id Reconcile through gRPC is its purpose.
- **Crash table cost on the VM is unmeasured.** It runs on the real pool (93 runs, ~3 ops each). Wave7 modelled −4.2 s at parallel 8 against the two sim tests it replaces, but run the VM once before relying on commit 6's numbers.
- The before mutation run is partial (see §3); targets that were never run keep their wave7 first-kill evidence only.

## 6. ADR 0005: where it should change

- **"`t.Parallel` by default"** needs a stated exception: tests that touch process-global state (umask, `slog.SetDefault`, `Setenv`, Ginkgo's one suite per process, signal handlers) stay serial, with a one-line comment saying which global. Otherwise someone will "fix" them and get rare cross-test failures (umask leaks into a parallel test's temp files).
- **"No real waiting"** should say *synctest where no exec is involved; injected durations of a few ms plus polling on observable state where real goroutines or grpc must run*; and that a `t.Skip` inside a synctest bubble reports PASS (call `requireSim` outside the bubble), and that a failing bubbled test must release its goroutines before `Fatal`.
- **"Assert at the lowest level that proves the row"** needs a guard: keep at least one **independent, hand-written oracle** per planner area. Golden files regenerated by `-update` from the code under test are a change detector, not an oracle (adversary C1).
- **Fast tests found bugs.** Shortening timers exposed the grpc `Stop()` hang; making the create race deterministic exposed that `TestConcurrentCreatesSerialize` never tested serialization. The ADR could require that a test made faster is re-proved with a planted mutant (as done in §3), since "same assertions" is not enough: a test that never actually waited can pass for the wrong reason.
- **Mutation scope**: wave7's sampler stopped at reconcile.go line 296, so `simulateDestroyAll` was never mutated. The nightly mutant sample in the ADR should be whole-file.
- **Expected-skips**: suggest every sim-only test calls `requireSim` first (no pool access before the skip), and that ci/check-skips.sh could verify the listed tests skip with zero zfs calls.
