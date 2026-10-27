# CI 49s -> 76s (fd3ba30 -> 1d4d623), and 806727d

Endpoint job (critical path in every run). Logs parsed from get_job_logs (full logs; per-test TSVs via ci/test-times.awk in scratchpad wave15/tt/).

| run | commit | p2g | restore step | MB/s | ssh up | build | test phase | shard spans (s) | endpoint test-s |
|---|---|---|---|---|---|---|---|---|---|
| 40506940034 | db15ae9 | 87 | 8s | 135 | +19.5 | +18.6 (cold go) | 18.5s | 14.2/14.9/18.2 | 127 |
| 40510207805 | fd3ba30 | 49 | 8s | 129 | +9.6 | +4.9 | 17.8s | 13.5/14.7/17.5 | 123 |
| 40511042404 | 1d4d623 | 76 | 17s | 60 (stalls) | +13.2 | +7.6 | 31.7s | 31.0/21.1/31.0 | 245 |
| 40511369632 | 806727d | 57 | 11s | 97 | +9.3 | +6.1 | 23.4s | 22.5/16.5/23.2 | 172 |

(40502060857 was the pre-split single job: 9s restore at 119 MB/s, test phase 31.6s for both suites.)

Guest CPU busy 91-93%, CPU PSI some 72-76% in every endpoint run: the guest is CPU-bound, so wall tracks total work.

## 1. Restore: download variance
- Cache entry 942,776,534 B (~899 MB), same entry size every run; exact key hits all.
- 40511042404: 60 MB/s with repeated zero-progress seconds (13:19:49-50, 52-53, 54-55, 56-57); download 14s + 1.3s unzstd. The csi job in the same run got 82 MB/s; fd3ba30 got 129 (endpoint) / 234 (csi). Azure blob throughput, not code: +9s.
- The disk is already a zstd-compressed qcow2 (`qemu-img convert -c -o compression_type=zstd`), so a higher actions/cache zstd level gains ~nothing. Bytes are what matter: 1s per 130 MB at nominal, 1s per 60 MB on a bad day. Levers: shrink guest contents (packages, docs), trim the qemu tree, keep the 66 MB initrd out of the warm path (fallback only). Splitting qemu from the guest only helps if the two entries download concurrently (~1-2s).

## 2. Test growth
- New work: TestDestroyNeverHitsAReplacedDataset (5 cases x every destroy index, 19 subtests, 18.8-25.0 test-s, all on shard 0; not in real-zfs-weights.txt, so weighed at 0.1). TestDeleteVolumeScanIsSubtreeScoped 2.8. TestRealFacts 6.0 -> 8.4-9.4 (sequential, new rows).
- TestCrashAtEveryMutationConverges: same 57 subtests; 41.7 -> 53.6 (806727d) / 74.9 (1d4d623). Subtests with no destroy (create-fs 0.92->1.47, publish 0.94->1.53) slowed as much as the delete ones, so this is contention, not the guid get. The guid get is not measurable (at most ~1s).
- Unchanged tests slowed 1.6x (median) in 40511042404, but only ~1.0-1.4x in 40511369632, for example TestPropertyRoundTrip 15.4/37.7/21.8. That run's host was also slow: build 7.6s vs 4.9s, initrd 5.0s vs 3.3s.
- Split: of +13.9s test phase, ~5.6s is new work (17.8 -> 23.4 nominal); ~8s is host noise.
- Weights are stale: shard 1 ends ~6s before shards 0/2 in 806727d (16.5 vs 22.5/23.2).

## 3. 806727d (run 40511369632, 57s)
- No-initrd boot worked on both VM jobs: "direct kernel boot ... initrd 67176 KiB, not loaded (root=/dev/vda1)". No fallback warning. Startup has no initrd phase (endpoint 1.24 + 6.75s; csi 1.64 + 9.04s).
- ssh up: endpoint +9.3s; csi +15.6s. The build finished first (+6.1 / +8.4), so the claim "ssh up before the build ends" is wrong: ssh is still the gate, by 3.2s.
- systemd-udev-settle is still active (1.78s) even with zfs-import-scan masked: something else pulls it in.
- Expected push-to-green: 54s with a nominal 8s restore; 52-62s overall, more on a slow-cache plus slow-host day. Under 60 nominally, not 45.

## Levers (critical-path seconds)
1. Endpoint test phase, 4-6s: run TestDestroyNeverHitsAReplacedDataset's every-index sweep on the sim and keep one index per case on real ZFS, then regenerate real-zfs-weights.txt (adds the new test, crash test 14.15 -> ~18). A third VM job for shard 0 would be ~8s but costs a runner.
2. Cache bytes, 3-4s nominal, ~7s on slow days: halve the 899 MB entry (initrd out of the warm path, trimmed qemu tree, slimmer guest). Concurrent split restores ~1-2s.
3. Boot, ~2-3s: find what still pulls in systemd-udev-settle (1.78s); ssh gates the build by 3.2s in the endpoint job.
