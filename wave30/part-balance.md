# A-part0-still-heavier: root cause (read-only analysis)

Data: the green endpoint 0/2 and 1/2 job logs from runs 40550837228 (r228), 40551637783 (r783), 40551837712 (r712)
(old weights, plan 39.85/39.84) and 40552465632 (r632, f389474, new weights, plan 30.83/30.83).
The shard logs of the old runs went through ci/test-times.awk. The plan was rebuilt locally from r632's test list and
matches the logged `part 0/2: 30.83 s: shard 0 15.68 ...` line exactly.
Scratch: wave30/logs/*, wave30/bin/{sum.py,work.py,pred.awk}, wave30/logs/est.weights.

## Per-part measurements (guest test phase; forks are zfs/mount/exportfs execs, which do not depend on contention)
| run  | part | phase s | root CPU-s | forks | CPU-ms/fork | host build s | login s |
|------|------|---------|-----------|-------|-------------|--------------|---------|
| r228 | 0 / 1 | 11.2 / 5.4 | 41.6 / 18.7 | 5582 / 4073 | 7.5 / 4.6 | 8.6 / 6.9 | 12.7 / 10.6 |
| r783 | 0 / 1 | 9.7 / 8.3  | 35.7 / 28.8 | 5774 / 4358 | 6.2 / 6.8 | 7.8 / 9.2 | 11.6 / 13.5 |
| r712 | 0 / 1 | 6.3 / 8.4  | 24.0 / 28.9 | 4713 / 4570 | 5.2 / 6.4 | 6.0 / 8.7 | 10.6 / 12.7 |
| r632 | 0 / 1 | 9.9 / 5.5  | 35.7 / 18.9 | 5185 / 4186 | 6.9 / 4.6 | 9.5 / 6.7 | 11.1 / 9.3  |
Phase ≈ forks × ms/fork / (4 vCPU × 0.9 busy). Check: 5185×6.9/3600 = 9.9 and 4186×4.6/3600 = 5.4.

## (1) Is it systematic? Yes for the work. The 9.9 vs 5.5 gap is mostly the runner.
- Part 0 makes more forks in 4 of 4 runs: 1.37, 1.32, 1.03 under the old weights and 1.24 under the new ones.
  The f389474 rebalance did not move this: the old-plan mean is also 1.24. The excess is about 55/45 of the work.
- CPU-ms per fork follows host speed. In all 4 runs the part with the slower host build also has the higher
  ms/fork (r632: build ratio 1.42, ms/fork ratio 1.45). So host speed is runner variance, not load.
- Splitting r632's 1.80x phase ratio: about 1.24x is work (37% of the log-ratio) and about 1.45x is host speed (63%).
  r712 shows the other direction: part 0 drew the fast host and finished first (6.3 vs 8.4 s).

## (2) Shard 0, the dominant tests, and whether weights are stale or the cost is in setup
- Shard 0 runs only TestCrashAtEveryMutationConverges plus 1/6 of the TestPropertyRoundTrip (PRT) rows.
  Crash is a sequential test with 57 parallel subtests: cost 60.75 test-s, wall 8.53 s.
- The "15.68 vs 7.58" split is only on paper. Shard walls were 9.82, 8.99 and 8.99 s. In a CPU-saturated guest
  (busy 91%, PSI cpu some 75%) the shards end together, so shard 0 is not the tail.
- The real problem is that the weights depend on where a test was measured. A test measured in the loaded part
  reads 2-4x higher than the same test measured in the light part:
  - PRT runs identical rows i mod 6 on every shard, yet cost/4 per shard was 1.87, 4.07, 4.20 in part 0
    against 1.21, 1.04, 1.13 in part 1.
  - TestZFSDiffReplay measured 6.44 / 6.32 / 3.77 s in part 0 (r228 / r783 / r712) and 1.74 s in part 1 (r632).
    The file says 5.99.
  - TestReconcileAgainstDesiredSet cost 6.2 / 5.0 / 3.5 in part 0 and 1.18 in part 1. The file says 1.37.
  - TestDeleteVolumeScanIsSubtreeScoped: 5.8 / 4.1 / 2.5 in part 0, 1.04 in part 1. The file says 1.13.
  f389474 moved these tests into part 1 at their part-0 (inflated) weights. As a result, r632 part 1 measured
  15.0 weight-units against 30.83 planned, and part 0 measured 36.9 against 30.83.
  The note in the weights file puts the inflation at 1.3-1.5x. The data says 2-4x.
- The cost is not in setup:
  - Pool create happens once per shard, 3 per part, so it is the same on both sides.
  - The cgroup CPU of tests, system, zed and udev is about 2x in part 0 across the board. That is load driven
    by more zvol and dataset events, not a fixed overhead.
- Latent bug in ci/real-zfs-weights.sh: a sequential fan-out test is weighed by wall when the input is a TSV,
  but by own+(cost−own)/nproc when the input is an older log. Crash's 13.00 came from the cost formula
  (old logs). The next regeneration from TSV would set it to its wall, 8.53, and part 0 would get heavier still.

## (3) Does the plan's shard model match the guest? No.
- The plan treats a shard as a serial lane and divides parallel tests by nproc.
- The guest actually runs 3 shards × -test.parallel=8, so up to 24 runnable tests share 4 vCPUs.
  - Loaded part 0: shard ends were within 0.8 s of each other even though the plan said 15.68 vs 7.58.
  - Light part 1: walls were 3.05, 5.36 and 4.80 s against 10.28 planned for each shard.
- So the part phase follows the part's total CPU work (as ADR 0005 says) and shard-level LPT barely matters.
  The part-level balance is the only lever, and it is fed weights that carry each part's contention.

## (4) Fix (minimal, with predictions)
To predict, I turned each run's per-test cost/nproc into "fork units": I scaled each part so that its tests sum to
that part's fork count, which removes the per-part contention. The estimates are stable across runs:
RealFacts 1182-1297, PRT 2152-2531, Crash 1368-1818, NFSReplay 235-256. The model reproduces r632's split
(53.8% predicted, 55.3% measured).
- Recommended: weights from per-part-normalized measurements.
  - Change real-zfs-weights.sh to scale each part's rows by forks / Σw (or CPU-s / Σw), using the
    `forks N` line in the log, before averaging runs.
  - Applied to the 4 runs above and scaled to the same 61.66 total, it gives:
    - PRT 15.15, Crash 10.75 (was 13.00), RealFacts 7.92 (5.24), ZFSDiffReplay 4.36 (5.99)
    - Model 2.36 (3.01), NFSReplay 1.55 (1.06), DestroyNever… 1.44 (0.86)
    - ReconcileAgainstDesiredSet 0.92 (1.37), CloneSourcesMustBeOwned 0.89 (1.25), DeleteVolumeScan… 0.77 (1.13)
    - Full file: wave30/logs/est.weights.
  - The plan then puts Crash and ZFSDiffReplay in part 0, and RealFacts, NFS and Model in part 1.
  - Predicted forks are 4688 / 4680, a 50.0% split. Both the r632-only and the 4-run models agree on this.
- Hand-editing only Crash is fragile. LPT is lumpy (TestModel flips parts), so the result jumps around:
  - 13.50: 49.4% (r632 model)
  - 14.00: 47.5%
  - 14.50: 45.1%
  - 15.00: 49.9%
  - 15.50: 52.1%
  - the model built from the 3 older runs predicts about 44% for all of these
  - Not recommended, except as a stopgap at 13.50.
- Predicted part timings for r632 with the balanced split (forks × ms/fork / 3.6):
  - On the hosts r632 drew: part 0 about 9.0 s (was 9.9), part 1 about 6.0 s (was 5.5).
  - On equal hosts (5.75 ms/fork): 7.5 / 7.5 s, against 8.3 / 6.7 s today.
  - The remaining part 0 vs part 1 gap after the fix is host variance and cannot be fixed by the plan.
- Also: when regenerating weights, measure fan-out sequential tests as cost/nproc, not wall, to keep Crash honest.
  The probation note's "1.3-1.5x" should be replaced by the per-part normalization.

## Critical path: which job finishes last
| run  | created → done | last job (completed) | next job | margin |
|------|----------------|----------------------|----------|--------|
| r632 | 17:38:16 → :57 (41 s) | endpoint 0/2 :57 | csi :52 | 5 s |
| r712 | 17:34:15 → 35:03 (48 s) | endpoint 0/2 35:03 | csi 34:57 | 6 s |
| r783 | 17:33:00 → :38 (38 s) | endpoint 0/2 and 1/2 :38 | csi :37 | 0-1 s |
| r228 | 17:27:55 → 28:39 (44 s) | endpoint 0/2 28:39 | csi / p1 28:33 | 6 s |
- Part 0 is last in 4 of 4 runs, but most of its lag is not the test phase.
  - r632: part 0 picked up 2 s later, its build and boot were 1.8 s slower (host), and its tests ran 4.4 s longer.
  - r712: part 0's tests were the shorter ones (6.3 s). It finished last because it was picked up 7 s late
    and its guest-cache restore took 12 s, against 4 s in part 1.
- Expected gain from balancing:
  - About 0.9 s on r632's hosts and about 0.8 s on equal hosts.
  - Part 0 would still finish about 4 s after csi on r632, so the run wall drops by only about 1 s.
  - The rest of the gap is runner draw: start delay, cache restore, host speed.
- Every run is under the 60 s cap, and 2 of 4 are under the 45 s target. The balance fix is worth taking because it
  removes a systematic bias and stops the weights from oscillating between parts. It is not the main wall-clock
  lever: job start and restore variance is.
