# CI budget anomaly: +14s on the real ZFS 2.4 VM job (5b42809 -> 939fbac)

## Verdict
Noise (a uniform ~1.5x slower guest plus ~4s slower boot), not the merge. The merge's own cost is about 0.06s.
The latent problem is a mis-weighted shard, so the job has no headroom against noise.

## Step time split (from log timestamps)
| Phase | 5b42809 (A) | 939fbac (B) | delta |
|---|---|---|---|
| step start -> guest "start at uptime" (qemu boot + ssh) | 12.6s | 16.7s | +4.1 |
| guest tests (uptime 9.00->27.23 / 10.99->38.74) | 18.2s | 27.7s | +9.5 |
| step total | ~31s | ~45.6s | +14 |
Guest boot-to-start uptime was 9.0s vs 11.0s. Host prep before the step (checkout, caches) was not slower: 16.3s vs 11.8s.

## Per-shard wall (guest uptime, shards.txt)
| Shard | A | B | ratio |
|---|---|---|---|
| csi-other | 1.91s | 2.25 | 1.18 |
| endpoint.0 | 4.95 | 8.37 | 1.69 |
| endpoint.2 | 5.40 | 9.04 | 1.67 |
| csi-mount | 10.51 | 15.30 | 1.46 |
| csi-block | 10.56 | 15.71 | 1.49 |
| endpoint.1 | 17.97 | 27.30 | 1.52 |

## Per-test (top-level lines; parallel subtests summed in brackets)
| Test | A | B | ratio |
|---|---|---|---|
| csi-block TestSanity | 10.48 | 15.64 | 1.49 |
| csi-mount TestSanity | 10.46 | 15.25 | 1.46 |
| ep.2 TestReconcileAgainstDesiredSet | 3.00 | 4.77 | 1.59 |
| ep.1 TestPropertyRoundTrip (196 subtests: 30.4 / 45.8) | 2.82 | 4.01 | 1.42 |
| ep.0 TestNFSReplay | 2.58 | 3.89 | 1.51 |
| ep.2 TestCloneSourcesMustBeOwned | 1.88 | 3.43 | 1.82 |
| ep.1 TestCrashAtEveryMutationConverges (51 subtests: 45.6 / 70.0) | top line 0.00 | top line 0.00 | 1.53 |

Sum of top-level times per shard: ep.0 6.65->11.1, ep.2 19.8->32.1 (1.62x), csi-mount 10.5->15.3, csi-other 5.25->7.25.

The slowdown is uniform, including code the merge cannot touch: csi-sanity (+49%), the crash sweep (+53%) and PropertyRoundTrip (+50%). That is a CPU-starved guest (2 vCPU, 6 concurrent shards). It looks like runner variance; the logs carry no steal or load data.
Boot was also slower, which points at the host rather than the tests.

## What the merge actually added
- FuzzReconcileSafe: 7 -> 8 seeds, 0.02s -> 0.08s total (seed#7 = 0.03s). Fuzz targets run only as seed corpora, at 0.00-0.08s in both runs. No fuzz target is heavy and none runs against the real pool in any measurable way.
- BenchmarkScan does not run (no -bench). The proto and README changes are not part of the test run.
- The test lists are identical apart from the one new seed.
- Moving fuzz seeds to the simulator leg would save under 0.1s, so it is not worth doing.

## Real finding: shard imbalance sets the critical path
ci/real-zfs-weights.txt gives every shard 7.5 weight-units. In truth endpoint.1 takes 18s (A) / 27s (B) while endpoint.0 and endpoint.2 take 5-9s. Shard endpoint.1 holds:
- TestCrashAtEveryMutationConverges, weighted 6.0 but really about 14s wall;
- TestPropertyRoundTrip, absent from the weights file (default 0.1) but really 2.8-4.0s wall, 30-46s of subtest CPU.

endpoint.0, endpoint.2 and csi-other are done by 14.6s (A), while endpoint.1 runs on until 27s. The tail is about 7s (A) and 11s (B) with the cores mostly idle. The tail is the pure imbalance.

## Recommended fix (no check cut; coverage unchanged)
Correct the two weights so the crash sweep gets a shard to itself and PropertyRoundTrip lands with the light shard. I simulated the awk sharder against the actual test list:
- current loads: 7.5 / 7.5 / 7.5, with Crash and PropertyRoundTrip together on one shard;
- proposed: Crash alone (ep.0), TestNFSReplay on ep.1, PropertyRoundTrip with TestCreateVolumeProperties on ep.2.

Expected saving on the critical shard is about 3-4s (it sheds PropertyRoundTrip, 2.8-4.0s). That brings the A/B-style numbers to roughly 14s/23s. It does not cancel a 1.5x runner slowdown, but it recovers part of the headroom.

```diff
--- a/ci/real-zfs-weights.txt
+++ b/ci/real-zfs-weights.txt
-TestCrashAtEveryMutationConverges 6.00
+TestCrashAtEveryMutationConverges 14.00
@@
+TestPropertyRoundTrip 2.80
 TestCreateVolumeProperties 1.27
```
Update the comment above Crash to say "scaled from 27.7s guest wall; run 40468816308, ep.1 minus PropertyRoundTrip".

Optional follow-up if more headroom is needed:
- Make the crash sweep's 51 parallel subtests cheaper, since they are about 70% of the CPU. A rotated crash point per case is already the PR design.
- The remaining fixed cost is boot (12-17s) plus prep (12-16s).
- Treat 54s vs 64s as inside the run-to-run variance of this runner; judge the budget on several runs.
