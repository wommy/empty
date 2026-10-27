# CI check: real ZFS 2.4 endpoint parts (branch claude/gallant-johnson-1qaxp5)

Read-only. Runs: 40558567587, 40557164093, 40555805200, 40554288012, plus 40559709255 (newest completed ci.yml run on the branch, newer than the four above). All six runs concluded `success`.

Column definitions:
- **login prompt (s)**: the `timeline: login prompt at +NNNNms` line, converted to seconds.
- **test phase (s)**: the first `guest load over the test phase: X.Xs on 4 vCPUs` line in the job log. The `part N/2: 30.76 s: shard ...` lines are the planned split (printed before the test phase, identical in both jobs of a run) and were not used.
- **forks**: the first `forks N (...)` line in the job log.

| run id | part (job id) | login prompt (s) | test phase (s) | forks |
|---|---|---|---|---|
| 40559709255 | endpoint 0/2 (122385126612) | 11.400 | 10.6 | 5723 |
| 40559709255 | endpoint 1/2 (122385126035) | 8.588 | 5.1 | 3904 |
| 40558567587 | endpoint 0/2 (122381112591) | 9.261 | 5.4 | 4579 |
| 40558567587 | endpoint 1/2 (122381113242) | 9.319 | 5.0 | 3889 |
| 40557164093 | endpoint 0/2 (122376190094) | 10.749 | 10.2 | 5368 |
| 40557164093 | endpoint 1/2 (122376190669) | 11.297 | 10.2 | 5374 |
| 40555805200 | endpoint 0/2 (122371434284) | 11.059 | 10.1 | 5267 |
| 40555805200 | endpoint 1/2 (122371434207) | 9.318 | 4.8 | 3828 |
| 40554288012 | endpoint 0/2 (122366056177) | 11.247 | 9.9 | 5087 |
| 40554288012 | endpoint 1/2 (122366056374) | 10.190 | 9.6 | 4931 |

Cross-check of test phase against forks: forks / forks-per-second in the same line gives the same scale, e.g. 40558567587 ep0 4579 / 840 = 5.45 s (logged 5.4 s); 40559709255 ep0 5723 / 541 = 10.58 s (logged 10.6 s).

## Quoted log lines

Run 40559709255, endpoint 0/2 (job 122385126612):
```
2026-10-27T18:25:00.8485859Z timeline: login prompt at +11400ms
2026-10-27T18:25:13.4180177Z guest load over the test phase: 10.6s on 4 vCPUs
2026-10-27T18:25:13.4182897Z forks 5723 (541/s, 7.0 CPU-ms each), context switches 327503 (30955/s)
```
Run 40559709255, endpoint 1/2 (job 122385126035):
```
2026-10-27T18:24:58.5433657Z timeline: login prompt at +8588ms
2026-10-27T18:25:04.5149556Z guest load over the test phase: 5.1s on 4 vCPUs
2026-10-27T18:25:04.5150719Z forks 3904 (772/s, 4.4 CPU-ms each), context switches 270051 (53370/s)
```
Run 40558567587, endpoint 0/2 (job 122381112591):
```
2026-10-27T18:17:43.9316305Z timeline: login prompt at +9261ms
2026-10-27T18:17:50.5944326Z guest load over the test phase: 5.4s on 4 vCPUs
2026-10-27T18:17:50.5945330Z forks 4579 (840/s, 4.5 CPU-ms each), context switches 304403 (55854/s)
```
Run 40558567587, endpoint 1/2 (job 122381113242):
```
2026-10-27T18:17:47.0296243Z timeline: login prompt at +9319ms
2026-10-27T18:17:53.3443862Z guest load over the test phase: 5.0s on 4 vCPUs
2026-10-27T18:17:53.3444956Z forks 3889 (778/s, 4.4 CPU-ms each), context switches 269442 (53888/s)
```
Run 40557164093, endpoint 0/2 (job 122376190094):
```
2026-10-27T18:08:54.4765924Z timeline: login prompt at +10749ms
2026-10-27T18:09:06.7925283Z guest load over the test phase: 10.2s on 4 vCPUs
2026-10-27T18:09:06.7927933Z forks 5368 (526/s, 7.0 CPU-ms each), context switches 294799 (28874/s)
```
Run 40557164093, endpoint 1/2 (job 122376190669):
```
2026-10-27T18:09:01.3580999Z timeline: login prompt at +11297ms
2026-10-27T18:09:13.5742945Z guest load over the test phase: 10.2s on 4 vCPUs
2026-10-27T18:09:13.5765232Z forks 5374 (526/s, 6.7 CPU-ms each), context switches 343008 (33562/s)
```
Run 40555805200, endpoint 0/2 (job 122371434284):
```
2026-10-27T18:00:24.3951115Z timeline: login prompt at +11059ms
2026-10-27T18:00:35.9786654Z guest load over the test phase: 10.1s on 4 vCPUs
2026-10-27T18:00:35.9797353Z forks 5267 (519/s, 7.1 CPU-ms each), context switches 296145 (29177/s)
```
Run 40555805200, endpoint 1/2 (job 122371434207):
```
2026-10-27T18:00:21.6585808Z timeline: login prompt at +9318ms
2026-10-27T18:00:27.7228844Z guest load over the test phase: 4.8s on 4 vCPUs
2026-10-27T18:00:27.7230106Z forks 3828 (794/s, 4.4 CPU-ms each), context switches 297534 (61729/s)
```
Run 40554288012, endpoint 0/2 (job 122366056177):
```
2026-10-27T17:50:36.1011596Z timeline: login prompt at +11247ms
2026-10-27T17:50:47.5618148Z guest load over the test phase: 9.9s on 4 vCPUs
2026-10-27T17:50:47.5622891Z forks 5087 (513/s, 7.1 CPU-ms each), context switches 287838 (29045/s)
```
Run 40554288012, endpoint 1/2 (job 122366056374):
```
2026-10-27T17:50:35.6549209Z timeline: login prompt at +10190ms
2026-10-27T17:50:47.2846980Z guest load over the test phase: 9.6s on 4 vCPUs
2026-10-27T17:50:47.2909787Z forks 4931 (515/s, 6.9 CPU-ms each), context switches 325076 (33933/s)
```

Plan lines (not used as the test-phase column), for reference. They print identically in both jobs of a run and are the same in all runs except where the weights changed:
```
2026-10-27T18:17:40.6293617Z part 0/2: 30.76 s: shard 0 13.10 shard 1 8.83 shard 2 8.82
2026-10-27T18:17:40.6293985Z part 1/2: 30.76 s: shard 3 10.30 shard 4 10.23 shard 5 10.22
```

## A. Are the two endpoint parts' test phases within 20% of each other?

Measure: |ep0 - ep1| / max(ep0, ep1) on the test phase column.

- **40558567587** (first run with normalized weights): 5.4 s vs 5.0 s, difference 7.4%. **Within 20%.**
- **40559709255** (later run): 10.6 s vs 5.1 s, difference 52%. **Not within 20%.**
- Earlier runs for comparison: 40557164093 10.2 vs 10.2 (0%, within); 40555805200 10.1 vs 4.8 (52%, not within); 40554288012 9.9 vs 9.6 (3%, within).

So the normalized-weights run is balanced, and the later run is not. The later run's imbalance also tracks the runner: the slow runner (about 7.0 CPU-ms per fork, the long phase of about 10 s and the long login prompt above 11 s) went to ep0, and the fast runner (4.4 CPU-ms per fork, about 5 s) went to ep1. The same split appears in 40555805200 (ep0 slow, ep1 fast). This is a hardware-class effect, not clearly a weights effect. Note the reference numbers from the task (9.9 s vs 5.5 s on 40552465632) were not re-measured here; 40554288012 in this set gives 9.9 / 9.6.

## B. Login prompt rule (keep the nice setting if the median over 3 green runs is at most 11.15 s)

Per-run median = mean of the run's two endpoint login prompts (two values, so the median is their mean).

| run | ep0 (s) | ep1 (s) | run median (s) |
|---|---|---|---|
| 40552465632 (given) | 11.100 | 9.298 | 10.199 |
| 40554288012 | 11.247 | 10.190 | 10.719 |
| 40555805200 | 11.059 | 9.318 | 10.189 |
| 40557164093 | 10.749 | 11.297 | 11.023 |
| 40558567587 | 9.261 | 9.319 | 9.290 |
| 40559709255 | 11.400 | 8.588 | 9.994 |

- Median over all six run medians: sorted 9.290, 9.994, 10.189, 10.199, 10.719, 11.023; median = (10.189 + 10.199) / 2 = **10.194 s**.
- Median over the three most recent measured runs (40559709255, 40558567587, 40557164093): 9.994, 9.290, 11.023; median = **9.994 s**.
- The highest possible median of any three of the six run medians is 11.023 s, which is below 11.15 s, so the verdict does not depend on which three runs are chosen.

**Answer B: KEEP** the `nice` setting. The median is 10.19 s (all six runs) or 9.99 s (three most recent), both at or below 11.15 s.

## Answers

**A.** Run 40558567587 is within 20% (5.4 s vs 5.0 s). The later run 40559709255 is not (10.6 s vs 5.1 s). Earlier runs are mixed: 40557164093 and 40554288012 are within 20%, 40555805200 is not (10.1 s vs 4.8 s).

The within-20% result follows the runner pairing, not the normalized weights. Before the weights changed, 40557164093 and 40554288012 were balanced with both parts on slow runners (about 7 CPU-ms per fork). After the change, 40558567587 is balanced with both parts on fast runners (about 4.4 CPU-ms per fork). 40559709255 and 40555805200 are unbalanced because one part got a slow runner and the other a fast one (about 2.1x gap). The reference 9.9 s vs 5.5 s on 40552465632 has the same roughly 1.8x slow/fast gap. The logs therefore cannot show that the weights fixed the imbalance.

**B.** Keep. Median of the run medians is 10.194 s (six runs), which is at or below 11.15 s.
