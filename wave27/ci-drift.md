# CI drift on PR #1: wave 27 timing analysis

Read-only. The data covers 15 `ci.yml` runs, numbers 76 to 90 (6629b7e to 2f72064, 15:58 to 17:19 UTC on 2026-10-27). Timings come from `list_workflow_jobs` step times, which have 1 s resolution. Guest timelines come from the job logs of runs 76, 89 and 90, for both endpoint parts. Raw rows are in `ci-jobs.jsonl`, and the scripts are in `bin/` next to this file.

## Verdict

- **Typical push-to-green has grown from about 38 s to about 45 s.** Measured as `updated_at - created_at`: run 76 took 38 s, runs 80 to 90 have a median of 45 s, and run 89 took 61 s.
- **Our own growth adds about 3.5 to 5 s, all of it in the endpoint VM job's test step.** The critical job's test step averaged 17.8 s over runs 76 to 79 and 21.4 s over runs 80 to 90. Two causes:
  1. **Part imbalance (about 3 s).** The hand-raised weights in `ci/real-zfs-weights.txt` push part 0 to about 62% of the guest work. TestRealFacts is weighted 11.50 but measures 4.2 to 6.3 s. The TestFileExport\*/TestNFSReplay group is weighted about 11.7 but measures about 5 s. The heavy weights land in part 1, so the planner pushes TestModel and the ~1 s guard tests into part 0, next to TestCrashAtEveryMutationConverges and TestZFSDiffReplay.
     - Guest CPU-s per part: run 76 split 24.2/22.9, run 89 split 38.6/16.7, run 90 split 35.4/25.4.
     - Test phases: run 76 6.6/6.2 s, run 89 10.4/5.0 s, run 90 9.3/7.4 s.
     - Weights entered the overload: 85238de (16:12) raised RealFacts from 8.35 to 10.90, and cb3498b raised it to 11.50. The first run after that change, run 80, had an ep0-minus-ep1 test-step gap of +11 s.
  2. **Later guest-ready (about 2 to 3 s).** The guest started tests at uptime 10.1 and 11.6 s in run 76. In runs 89 and 90 it started at 11.0 to 13.8 s. Guest userspace boot went from 5.9-6.8 s to 7.1-8.5 s.
     - The host-side `go test -c` runs beside the boot on the same 4 vCPUs, and it got longer: "test binaries built" moved from +6.5-7.0 s to +8.5-8.8 s.
     - The likely cause is that internal/endpoint grew from 21.8k to 24.0k lines (+6 test files: cov-tests, scan-cut guards, snapshot size). Every run rebuilds the PR's packages, because the restored Go cache is the default branch's (e961f72 or 128008d).
- **Queue delay and restore speed are noise.**
  - Run 89 took 61 s, about 16 s over the median: ep0 started 14 s after run creation (the others started at 3 to 4 s), and its 566 MB guest restore ran at 50 MB/s, taking 12 s against a median of 6 s.
  - Run 89's own test step was 22 s, in line with the other runs since run 80.
  - Run 79 waited 8 s before GitHub even created its jobs (11 s queue on every job).
  - 4 of 15 runs had a job start 7 s or more late.
  - Restores of the same entry vary from 3 to 12 s (50 to 150 MB/s).
- **Most of the growth is cost per operation, not more operations.** Guest forks across both endpoint parts were 9170, 9507 and 9636 in runs 76, 89 and 90 (+5%). Total guest CPU-s rose from 47 to 55-61 (+17 to 29%). CPU-ms per fork in the overloaded part is 6.8 to 7.1, against 4.3 to 5.8 in the light one, so overloading one part costs extra on top of the imbalance itself. This is why rebalancing should give more back than a linear estimate suggests.
- **The `test` job does not cause the drift.**
  - Its simulator step is 16 to 23 s (run 76: 23 s; run 90: 22 s), and internal/endpoint under -race takes 11.8 to 12.3 s of it.
  - The new NFS-mode csi-sanity step costs 1 s.
  - The job ends 5 to 10 s before the VM jobs.
  - Run 78's 13 s step came from a fast runner, where internal/endpoint took 7.2 s. That shows about ±40% hardware variance.
- **The `static` job (12 to 22 s) and the csi VM job (test step 12 to 18 s) are not the cause either.** Csi was the critical job only when its restore was slow (runs 78 and 86).

## Per run (seconds from run creation; VM cells: queue/wall (restore, setup-go, test step))

| run | sha | total | queue first/last | crit | ep0 q/wall (restore,setupgo,test) | ep1 q/wall (r,g,t) | csi q/wall (r,g,t) | test wall (g, sim, nfs) | static wall (g) |
|---|---|---|---|---|---|---|---|---|---|
| 90 | 2f72064 | 43 | 3/3 | ep1 | 3/37 (5,4,21) | 3/39 (6,4,19) | 3/29 (5,3,13) | 32 (4, 22, 1) | 15 (4) |
| 89 | 17877d2 | 61 | 3/14 | ep0 | 14/46 (12,4,22) | 3/30 (5,2,15) | 4/34 (6,3,17) | 32 (6, 21, -) | 17 (5) |
| 88 | f7aa459 (failure) | 50 | 4/8 | ep0 | 5/44 (7,4,23) | 6/39 (8,3,19) | 7/38 (9,4,18) | 27 (3, 16, -) | 22 (7) |
| 87 | 2f21231 | 45 | 3/5 | ep1 | 3/30 (6,2,15) | 4/40 (8,3,20) | 5/35 (7,6,14) | 29 (3, 21, -) | 12 (2) |
| 86 | a07a765 | 45 | 4/4 | csi | 4/32 (5,3,17) | 4/32 (4,3,20) | 4/39 (8,5,17) | 30 (3, 22, -) | 16 (4) |
| 85 | 96c49ff | 40 | 3/4 | ep0 | 3/36 (4,3,22) | 3/35 (3,3,20) | 3/27 (4,2,13) | 27 (3, 20, -) | 18 (5) |
| 84 | 022b205 | 48 | 3/4 | ep0 | 4/43 (8,3,24) | 3/40 (5,3,22) | 3/32 (3,4,18) | 25 (3, 17, -) | 18 (6) |
| 83 | 48f431d | 45 | 4/4 | ep0 | 4/40 (7,3,21) | 4/38 (4,3,23) | 4/36 (8,4,16) | 30 (3, 21, -) | 18 (7) |
| 82 | 26975dd | 45 | 3/4 | ep1 | 4/38 (6,5,20) | 3/41 (7,4,22) | 3/32 (4,4,17) | 24 (3, 16, -) | 14 (3) |
| 81 | 286cf47 | 43 | 3/3 | ep0 | 3/39 (6,3,21) | 3/28 (3,3,16) | 3/28 (4,3,12) | 30 (3, 21, -) | 31 (19) |
| 80 | d232d63 | 47 | 3/4 | ep0 | 4/43 (6,3,25) | 3/32 (6,5,14) | 4/30 (3,4,16) | 31 (4, 21, -) | 17 (4) |
| 79 | 4a63496 | 51 | 11/11 | ep1 | 11/37 (5,3,22) | 11/40 (7,3,20) | 11/37 (10,3,14) | 31 (4, 22, -) | 19 (7) |
| 78 | 3a45760 | 43 | 4/4 | csi | 4/36 (4,4,21) | 4/28 (4,3,14) | 4/38 (8,4,16) | 21 (3, 13, -) | 14 (3) |
| 77 | 8a9784d | 46 | 3/7 | ep0 | 7/38 (8,3,19) | 3/30 (5,2,15) | 3/35 (7,4,16) | 33 (5, 23, -) | 14 (3) |
| 76 | 6629b7e | 38 | 3/4 | ep1 | 3/32 (5,3,15) | 4/33 (6,2,16) | 4/30 (4,3,15) | 32 (4, 23, -) | 13 (3) |

Spread across the 15 runs:

| Measure | min | median | max |
|---|---|---|---|
| total | 38 | 45 | 61 |
| queue to first job | 3 | 3 | 11 |
| queue to last job | 3 | 4 | 14 |
| restore | 3 | 5-6 | 12 |
| setup-go | 2 | 3 | 6 |
| ep0 test step | 15 | 21 | 25 |
| ep1 test step | 14 | 19 | 23 |
| csi test step | 12 | 16 | 18 |
| test sim step | 13 | 21 | 23 |

## Critical path, broken down

In the table below, pre covers set up, checkout, kvm and keys. Mid covers the qemu tree, the boot step and setup-go. Post covers the artifact upload and job teardown.

| run | sha | total | crit | queue | pre | restore | mid (qemu+boot step+setup-go) | test step | post | run tail | ep0-ep1 test step |
|---|---|---|---|---|---|---|---|---|---|---|---|
| 90 | 2f72064 | 43 | ep1 | 3 | 6 | 6 | 4 | 19 | 4 | 1 | +2 |
| 89 | 17877d2 | 61 | ep0 | 14 | 4 | 12 | 4 | 22 | 4 | 1 | +7 |
| 88 | f7aa459 | 50 | ep0 | 5 | 4 | 7 | 4 | 23 | 6 | 1 | +4 |
| 87 | 2f21231 | 45 | ep1 | 4 | 4 | 8 | 4 | 20 | 4 | 1 | -5 |
| 86 | a07a765 | 45 | csi | 4 | 5 | 8 | 5 | 17 | 4 | 2 | -3 |
| 85 | 96c49ff | 40 | ep0 | 3 | 4 | 4 | 3 | 22 | 3 | 1 | +2 |
| 84 | 022b205 | 48 | ep0 | 4 | 4 | 8 | 3 | 24 | 4 | 1 | +2 |
| 83 | 48f431d | 45 | ep0 | 4 | 5 | 7 | 4 | 21 | 3 | 1 | -2 |
| 82 | 26975dd | 45 | ep1 | 3 | 4 | 7 | 5 | 22 | 3 | 1 | -2 |
| 81 | 286cf47 | 43 | ep0 | 3 | 5 | 6 | 3 | 21 | 4 | 1 | +5 |
| 80 | d232d63 | 47 | ep0 | 4 | 4 | 6 | 4 | 25 | 4 | 0 | +11 |
| 79 | 4a63496 | 51 | ep1 | 11 | 4 | 7 | 4 | 20 | 5 | 0 | +2 |
| 78 | 3a45760 | 43 | csi | 4 | 5 | 8 | 4 | 16 | 5 | 1 | +7 |
| 77 | 8a9784d | 46 | ep0 | 7 | 3 | 8 | 4 | 19 | 4 | 1 | +4 |
| 76 | 6629b7e | 38 | ep1 | 4 | 4 | 6 | 3 | 16 | 4 | 1 | -1 |

| Segment | runs 76-79 | runs 80-90 | runs 80-90 without 89 |
|---|---|---|---|
| total | 44.5 | 46.5 | 45.1 |
| queue (critical job) | 6.5 | 4.6 | 3.7 |
| restore | 7.2 | 7.2 | 6.7 |
| test step | **17.8** | **21.5** | **21.4** |
| post | 4.5 | 3.9 | 3.9 |

Runs 76 to 79 carry about 3 s more queue noise (runs 77 and 79), which hides part of the test-step growth in their total.

## Guest timeline: endpoint parts (from the job logs)

Built and ready are in seconds after qemu start. Ready is the guest uptime when tests start.

| run / part | test binaries built | login prompt | tests start (uptime) | phase | busy | guest CPU-s (root) | forks | restore MB/s |
|---|---|---|---|---|---|---|---|---|
| 76 / 0 | +6.5 | +9.5 | 10.1 | 6.6 | 92% | 24.2 | 5040 | 131 |
| 76 / 1 | +7.0 | +10.6 | 11.6 | 6.2 | 93% | 22.9 | 4130 | 111 |
| 89 / 0 | +8.8 | +12.3 | 13.2 | **10.4** | 94% | **38.6** | 5542 | **50** |
| 89 / 1 | +6.0 | +10.4 | 11.0 | 5.0 | 85% | 16.7 | 3965 | 113 |
| 90 / 0 | +8.6 | +12.0 | 12.8 | **9.3** | 95% | **35.4** | 5214 | 147 |
| 90 / 1 | +8.5 | +12.7 | 13.8 | 7.4 | 86% | 25.4 | 4422 | 105 |

Within a part, the shards finish within 0.1 to 0.5 s of each other while the guest is CPU-saturated. In run 90 part 0 they ended at 22.17, 22.23 and 22.25 s. So the planned per-shard imbalance (16.61/11.62/11.62) costs nothing. **Only the balance between parts matters.**

## Weights against measurements

Measured values come from the shard logs, through `ci/test-times.awk` and the formula in `ci/real-zfs-weights.sh`, without timestamps. Both parts are summed per run. The part column shows where run 90 put the test.

| test | part (r90) | old weight | r90 | r89 | r76 |
|---|---|---|---|---|---|
| TestPropertyRoundTrip | 01 | 19.33 | 16.24 | 15.87 | 14.04 |
| TestCrashAtEveryMutationConverges | 0 | 13.39 | 12.46 | 13.55 | 8.99 |
| TestRealFacts | 1 | 11.5 | 6.29 | 4.19 | 4.43 |
| TestZFSDiffReplay | 0 | 5.85 | 5.73 | 6.24 | 3.97 |
| TestModel | 0 | 3.01 | 2.80 | 3.23 | 1.16 |
| TestNFSReplay | 1 | 2.29 | 1.20 | 0.93 | 0.90 |
| TestDestroyNeverHitsAReplacedDataset | 1 | 1.99 | 1.03 | 0.69 | 1.73 |
| TestReconcileAgainstDesiredSet | 0 | 0.94 | 1.35 | 1.38 | 0.76 |
| TestCloneSourcesMustBeOwned | 0 | 0.89 | 1.24 | 1.26 | 0.49 |
| TestDeleteVolumeScanIsSubtreeScoped | 0 | 0.69 | 1.24 | 1.03 | 0.51 |
| TestFileExportServedBeyondRequest | 1 | 1.19 | 0.57 | 0.56 | 0.53 |
| TestFileExportCreateRootOwner | 1 | 0.91 | 0.33 | 0.23 | 0.48 |
| TestFileExportAdminWorldEditIsForeign | 1 | 0.91 | 0.55 | 0.29 | 0.44 |
| TestFileExportUnpublishStillServed | 1 | 0.87 | 0.62 | 0.49 | 0.29 |
| TestCreateVolumeProperties | 0 | 0.56 | 0.83 | 0.94 | 0.39 |
| TestFileExportStaleShareOnlySharenfsHeals | 1 | 0.8 | 0.17 | 0.09 | - |
| TestFileExportLeftoverClientList | 1 | 0.79 | 0.54 | 0.33 | 0.44 |
| TestFileExportCreateServerElsewhere | 1 | 0.7 | 0.29 | 0.14 | 0.29 |
| TestReserveRestoreIsIdempotent | 1 | 0.68 | 0.38 | 0.27 | 0.61 |
| TestCloneLargerThanLimitIsOutOfRange | 0 | 0.53 | 0.65 | 0.24 | 0.54 |
| TestFileExportCreate | 1 | 0.61 | 0.40 | 0.14 | 0.38 |
| TestCreateVolumePinsMountpoint | 0 | 0.47 | 0.61 | 0.24 | 0.30 |
| TestFilesystemCloneNeverSmallerThanSource | 1 | 0.56 | 0.34 | 0.25 | 0.59 |
| TestReconcileCollectsLeakedCloneSource | 0 | 0.55 | 0.54 | 0.66 | 0.26 |
| TestSnapshotSizeIsItsSizeWhenTaken | 1 | 0.55 | 0.27 | - | - |
| TestReconcileNeedsADigestToCollectUndesired | 0 | 0.52 | 0.54 | 0.21 | 0.25 |
| TestSnapshotLifecycleWithClones | 1 | 0.53 | 0.37 | 0.71 | 0.45 |
| TestExpandThickFilesystemRetryAfterENOSPC | 1 | 0.51 | 0.38 | 0.77 | 0.45 |
| TestCloneFromVolumeReleasesInternalSnapshot | 0 | 0.39 | 0.51 | 0.20 | 0.34 |
| TestRealGetJSONMatchesSim | 1 | 0.47 | 0.33 | 0.67 | 0.23 |
| TestResumeConvergesThickToThin | 0 | 0.46 | 0.31 | 0.15 | 0.24 |
| TestPagination | 1 | 0.45 | 0.32 | 0.67 | 0.29 |
| TestFileExportLifecycle | 1 | 0.43 | 0.15 | 0.12 | 0.14 |
| TestFileExportPreconditions | 1 | 0.41 | 0.15 | 0.10 | 0.19 |
| TestFileExportCreateServerSurvivesRestart | 1 | 0.41 | 0.21 | 0.11 | 0.25 |
| TestReconcileDigestCoversTheGuid | 1 | (none: 0.1) | 0.32 | 0.70 | 0.33 |
| TestVolumeNameMustMatchID | 1 | 0.28 | 0.16 | 0.48 | 0.23 |

not in weights file (weigh 0.1): TestReconcileDigestCoversTheGuid 0.32, TestMinFreeBytesGuard 0.01, TestGetCapacityIsChildSpace 0.00, TestChildAvail 0.00, TestMinFreeBytesGuardPoolRootRefquota 0.00, TestPlanReserves 0.00, TestMinFreeBytesGuardLevelUpUnreadable 0.00, TestReconcileLockedReplanKeepsTheDigestedGuid 0.00, TestThickZvolReservationIgnoresCopies 0.00, TestCheckHeadroom 0.00, TestMinFreeBytesGuardParentSpace 0.00

The table shows these errors:
- TestRealFacts is weighted 11.50 against a measured 4.2 to 6.3.
- Every TestFileExport\* weight runs about 1.5 to 3 times its measurement. The group's total is about 11.7 against about 5, and the group stays in one shard.
- TestNFSReplay is weighted 2.29 against 0.9 to 1.2.
- TestDestroyNeverHitsAReplacedDataset is weighted 1.99 against 0.7 to 1.0.
- The hand estimates in the header of the file all ran about 2 times high. Their source run 40511369632 used a single part, so it was more contended.

Replaying `ci/real-zfs-plan.sh` read-only on run 90's list, with the measured part weights:

| weights | part 0 | part 1 | in part 0 |
|---|---|---|---|
| current file | 38.1 | 23.6 (62/38) | Crash, ZFSDiffReplay, Model, ReconcileAgainstDesiredSet, CloneSourcesMustBeOwned, DeleteVolumeScanIsSubtreeScoped |
| regenerated (mean of runs 89 and 90) | 30.9 | 30.9 | Crash, Model, CloneSourcesMustBeOwned; ZFSDiffReplay, RealFacts, NFS group and the ~1 s guards in part 1 |

## Recommendations, with the expected saving on push-to-green

1. **Regenerate `ci/real-zfs-weights.txt` from two-part runs and drop the hand estimates.** Expected: **-2 to -3 s** (median about 45 s to about 42 s).
   - How: `gh run download 40549402887 -p 'real-zfs-report-endpoint-*'`, plus the same for 40548715833, then `ci/real-zfs-weights.sh -s "runs 40549402887, 40548715833" .../test-times.tsv`.
   - The artifacts carry wall_s timestamps. A log-derived version, without wall_s, is in `new-weights.txt` here.
   - Effect: part 0's phase goes from 9.3-10.4 s back to about 7 s, close to run 76's balanced 6.2-6.6 s, plus some back from the lower CPU-ms per fork once part 0 is no longer overloaded.
   - Rerun once after the rebalance: the part-0 numbers (Crash 12.5-13.6, ZFSDiffReplay 5.7-6.2, Model 2.8-3.2) were measured overloaded, so they are about 1.3 to 1.5 times too high.
   - Going forward, set a new test's weight from its first two-part run rather than by hand, or leave it at 0.1 until measured: a 0.1 underestimate costs less than a 2x overestimate.
2. **Run the host test build at low priority beside the boot** (`nice -n 19` on the `go test -c` and `go build` subshell in `ci/real-zfs-vm.sh`). Expected: **-1 to -2 s** on guest-ready. This needs measuring under ADR 0005's keep rule.
   - The build has about 3 to 5 s of slack, finishing at +6 to 8.8 s while the guest is ready at +10 to 14 s.
   - The guest's userspace boot grew 1.2 to 2.6 s as the build grew.
3. **Take the artifact upload off the critical path.** Expected: **-1 to -1.5 s** on every VM job.
   - The upload step costs 1 to 2 s at the end of every VM job: `if: always()` uploads test-times, zfsdiff, plan, load and vmstat files.
   - Options: upload only on failure and in nightly or cache-warm, and regenerate weights from the job logs as done here. Or keep it only on the csi job, which has 5 to 10 s of slack.
4. **Keep the test job off the critical path. No action needed now.** It has 5 to 10 s of slack. Watch internal/endpoint under -race, at 11.8 to 12.3 s, if more simulator tests land there.
5. **Treat queue and restore variance as noise, and budget for it.** About 1 run in 4 has a straggler job (+7 to +10 s), and the restore varies from 3 to 12 s.
   - To stay at or under 60 s at roughly p95, the noise-free median needs to stay at or under about 42 s, which is ADR 0005's 45 s target minus one bad restore.
   - Items 1 to 3 bring the expected median to about 38 to 40 s.
   - Shrinking the 566 MB guest entry further is the only lever on restore. Each 100 MB cut saves about 0.7 to 2 s at the observed 50 to 150 MB/s.
6. **Lower priority: keep the PR's Go build cache fresh.** The restored entries are always from the default branch (e961f72 for the real-zfs namespaces, 128008d for test), so every run recompiles the whole PR delta.
   - A sha-keyed save when the match is only on the go.sum prefix would cut the "test binaries built" time by about 1 to 2 s. That time is only on the critical path through boot contention, so item 2 covers most of it.
   - A save at the end of an endpoint job would cost time on the critical path, so only the csi or test jobs should save, and they use other cache namespaces.

## Separating noise from our growth (summary)

| Run | Total | Over the 45 s median | Explained by |
|---|---|---|---|
| 89 (17877d2) | 61 | +16 | queue +10 (ep0 started at 14 s against 4 s); restore +6 (50 MB/s); test step 22 = normal |
| 79 (4a63496) | 51 | +6 | GitHub created the jobs 8 s late (all queues 11) |
| 88 (f7aa459, failed) | 50 | +5 | test step 23 + restore 7 + teardown with log upload 6 |
| 80 (d232d63) | 47 | +2 | first run after the RealFacts 10.90 weight: ep0 test step 25 against ep1 14 |
| 76 (6629b7e) | 38 | -7 | balanced parts (6.6/6.2 s phase), build at +6.5 s, guest ready at about 10 s |
