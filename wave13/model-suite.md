# model-suite: report

Branch `model-suite`, in the worktree `/home/user/empty/.claude/worktrees/agent-a5e3d463acd0910f8`, rebased onto c1205e9. Not pushed.

## Anomalies (read these first)

1. **The ownership tests were vacuous.** internal/endpoint/ownership_test.go set markers under a prefix the endpoint never reads, so the guards they claimed to test could be deleted and the tests still passed. Fixed in 396db70. Three mutants each survive the old test and are killed by the new one (table below).
2. **The CR-03 belief was wrong.** The wave8 spec said a multi-snapshot destroy can partly succeed. libzfs_core's `lzc_destroy_snaps` is atomic within one batch. realfacts_test.go now states the atomic belief, and the real pool checks it.
3. **SNP-11 now agrees.** The wave8 copy expected the simulator to diverge on SNP-11, but the upstream simulator gets it right, so that `simDiverges` entry was dropped.
4. **The explorer cannot see MH2.** With "plan ignores holds" mutated, TestExploreLifecycle (depth 4) still passes: no undesired committed volume holding a held snapshot is reachable within 4 steps. TestPlanDeleteVolumeRefusesHoldsAndChildren and TestReconcileReportsHeldSnapshots kill MH2 on PR, and TestModel kills it at 2,000 sequences (nightly).
5. **The model cannot see M2.** With the clone source using OwnedVolume, the mutant survives TestModel at any size, because the generator only makes uncommitted volumes through crashes. TestCloneSourcesMustBeOwned kills it on PR.
6. **Nothing has run on a real pool yet.** TestModel (MODEL_BUDGET=50 steps on the real pool) and TestRealFacts have only run on the simulator. Their first VM run is the acceptance test. The ci/real-zfs-weights.txt entries (5.00 and 1.50) are estimates. One risk: an admin zvol operation could come back busy, which would make the real leg flaky. If that happens, add a retry or a skip in ci/expected-skips.txt rather than raising the budget.
7. **Upstream already read part of the holds.** HOLD-REPLAN was partly in place upstream. c1d33b7 adds what was missing:
   - the userrefs column in the scan;
   - the planner refusing a held snapshot or a child dataset;
   - the fixpoint not reaping a held snapshot.
8. **Task 5: no tests were retired.** No row in S/wave10/rowmap.md had per-row killer proof done, so every test is kept, as the brief's default says.

## Commits (c1205e9..HEAD)

| commit | subject |
|---|---|
| 396db70 | endpoint: ownership tests set markers the endpoint actually reads |
| 64d4cab | zfstest: model rename and guid; fuzz no longer nests below a zvol |
| c1d33b7 | endpoint: plan around user holds and datasets below a volume |
| 0712a22 | endpoint: recovery FSM explorer runs nightly, and the guard that it ran |
| 91d1f8f | endpoint: model-based suite with expiring masks, and real-zfs facts |

The simulator changes are kept small for the zfsdiff builder:
- New code is in the new file internal/zfs/zfstest/sim_ext.go.
- sim.go only gets hooks: node.guid, Sim.guids, the `rename` case, and `guid` in known() and in the property switch.

Nothing was touched in internal/csi, cmd/, deploy/ or ci/real-zfs-*.sh. One exception to that last pattern: ci/real-zfs-weights.txt got two weight rows; the scripts themselves were not edited.

## Mutant evidence

Logs are in S/wave14ms/: mut_own.log, mut_holds.log, mut_fsm.log, mut_model.log and the 2,000-sequence run.

**Ownership** (old test passes, new test fails):
- M1 (OwnedVolume without the name check): killed.
- M2 (the clone source uses OwnedVolume): killed.
- M3 (received markers read as local): killed.

**Holds and children** (planner, scan and reconcile tests): MH1 (scan ignores userrefs), MH2 (plan ignores holds), MH3 (fixpoint reaps a held snapshot), MC1 (scan ignores children) and MC2 (plan ignores children) are all killed.

**FSM explorer** (depth 3, plus TestClassify):

| mutant | result |
|---|---|
| AN-09 revert | killed by TestClassify and TestExploreLifecycle |
| MH1 | killed |
| MC1 | killed |
| MC2 | killed |
| MH2 | survives (anomaly 4) |

**TestModel**:

| mutant | 64 seqs (PR) | 2,000 seqs |
|---|---|---|
| M1 name guard | killed | killed |
| M3 received-as-local | killed | killed |
| M4 clone source unstamped counted as ours | killed | killed |
| MH2 | survived | killed (seed 1005, reconcile-twice) |
| MC2 | survived | killed (seed 525) |
| MH1, MH3, MC1 | survived | (killed by PR planner tests) |
| M2 | survived | survived (anomaly 5; killed by TestCloneSourcesMustBeOwned) |
| M5 | invalid (did not build) | – |

**Masks**:
- TestExploreModelMasksStillBite: D07 bites in 489 ms; GAP-RECRACE bites in 12 s.
- Both masks expire on 2026-12-15, and checkMaskExpiry fails TestModel once that date passes.

## Budget (ADR 0005)

**TestModel alone, under -race:**

| sequences | time |
|---|---|
| 100 | 4.2 s |
| 200 | 8.0 s |
| 400 | 14.4 s |

Without -race, 400 sequences take 4 s.

**PR default:** 64 sequences (`-short` runs 16).
- The endpoint package under -race is now 7.9 to 9.0 s.
- The whole `go test -race -count=1 ./...` takes 14 to 22 s.

**Real pool:**
- At most 25 steps per sequence and MODEL_BUDGET=50 steps in total, which is 2 sequences.
- That is about 6 zfs commands a step at 30 to 60 ms each, well under 60 s.

**Nightly (`-tags nightly`):**
- TestExploreModelSweep: 20,000 sequences from a seed that rotates daily, measured at 4m13s.
- TestExploreLifecycle: about 40 s.
- MasksStillBite: about 13 s.
- ci/check-explore-ran.sh fails the nightly job if no TestExplore test ran:
  - With the tag: 1 test passed, and the guard exits 0.
  - Without the tag: "[no tests to run]", and the guard exits 1.

## Gate (final, at 91d1f8f)

- `gofmt -l .`: empty
- `go vet ./...`: ok
- `go vet -tags nightly ./...`: ok
- `go mod tidy -diff`: ok
- `bash -n ci/check-explore-ran.sh`: ok
- `actionlint`: ok
- `go test -race -count=1 ./...`: all packages pass, 14 s
