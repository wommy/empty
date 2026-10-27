# Wave 13: the VM job's critical path, measured (run 40494213342 vs 40471254688)

Every number is tagged **[m]** (measured: job log timestamps, the step API, guest `/proc/uptime`, `-test.v` durations) or **[e]** (estimated).
Sources:
- the new run 40494213342 (b03ef6d), VM job 122155423419, with a 3000-line log tail that starts at the keys step;
- the old run 40471254688 (c1205e9), VM job 122078634875, full log;
- both runs' step timings from `list_workflow_jobs`.

The artifact `zfsdiff-report` (test-times.tsv with wall stamps, endpoint.list) **could not be downloaded**. The proxy refuses `productionresultssa8.blob.core.windows.net` for both the MCP URL and `gh run download`. So I rebuilt the per-test own/cost columns by running `ci/test-times.awk` over the shard logs in the job log, for both runs. The `wall_s` column, which needs the stamps, is missing. Scratch files are in `wave13/cp/` (`shards/`, `old/shards/`, `tt.tsv`, `old/tt.tsv`).

## 1. Timeline of the VM job (new run; t=0 is run created, 11:11:04Z)

| t (s) | span (s) | what | note |
|---|---|---|---|
| 0 → 3 | 3.0 [m] | queue | job started_at 11:11:07 |
| 3 → 7.5 | 4.5 [m] | set up job, checkout, kvm, keys (SHA256SUMS fetched + gpgv: 0.7 [m]) | |
| 7.5 → 16.7 | **9.2 [m]** | restore guest: lookup 0.36, download **7.1** (876 MB, ~123 MB/s), unzstd 1.2, post 0.5 | the entry grew from 794 MB to 876 MB (+82 MB: the stored vmlinuz and initrd [e]) |
| 16.8 → 19.2 | **2.4 [m]** | restore qemu: lookup 0.37, download 1.8 (only 23 MB), extract 0.2 | per-restore overhead, not bytes |
| 19.2 → 19.45 | 0.25 [m] | gpgv, qemu-img overlay, qemu launched (11:11:23.45) | **direct kernel boot confirmed** (`direct kernel boot: 7.3.0-9-generic`) |
| 19.7 → 24.9 | 5.2 [m] | setup-go (Go cache 96 MB: 5.0 s), **off the path** | overlapped with the boot, as planned |
| 24.9 → 27.7 | 2.8 [m] | `go test -c`: binaries built at **+8.2 s** after the qemu launch | **finished 5.1 s before ssh**, so the build is off the path |
| 19.45 → 32.8 | **13.4 [m]** | the boot, from qemu launch to ssh up (+13.355 s) | guest kernel t0 ≈ +1.0 s after the launch [m, from uptime]; kernel 1.45 + initrd **4.03** + userspace 6.06 = 11.54 s to graphical.target [m]; ssh comes up 1.8 s after that [m], cause unknown |
| 32.8 → 33.9 | 1.1 [m] | nfs-server active, modprobe, `cloud-init status`, the version/nfsd checks, scp | |
| 33.9 → 65.1 | **31.2 [m]** | the test phase (guest uptime 13.46 → 44.62) | shards below |
| 65.1 → 71 | **~6 [m]** | the tail: scp logs, cat (0.25), **`systemd-analyze` + blame 1.7 [m]**, check-skips 0.5, artifact upload 1.4, post and job teardown ~2.3 | job completed_at 11:12:15; the run was updated at 11:12:16 (72 s) |

The shards (guest uptime, [m], from `shards.txt`):

| shard | new wall (s) | old wall (s) | new test-seconds (own+parallel subtests) | old test-seconds |
|---|---|---|---|---|
| endpoint.1 (TestPropertyRoundTrip + 23 others) | **30.6** | 13.8 | **109.2** (PropRT **83.6**) | 32.1 |
| csi-block | 22.5 | 18.6 | 22.4 | 18.5 |
| endpoint.0 (TestCrashAtEveryMutationConverges + others) | 21.9 | 14.4 | 78.6 (crash 61.5) | 55.4 (crash alone) |
| csi-mount | 21.9 | 18.1 | 21.8 | 18.1 |
| endpoint.2 (ZFSDiff 9.95, NFS 3.91, FileExport*) | 19.2 | **29.5** | 26.8 | 75.6 (PropRT 49.3) |
| csi-other | 3.6 | 2.6 | 12.0 | 7.5 |
| **phase** | **31.2** | **30.1** | 270.8 | 207.2 |

## 2. Old vs new on the path, and which estimate failed

Here is where the six seconds went, comparing old (77 s, created → VM completed) with new (71 s), all [m]:

| segment | old | new | Δ | estimate (cache-warm.md) |
|---|---|---|---|---|
| created → guest+qemu restored | 24.3 (setup-go 5.5 on the path) | 19.2 | **−5.1** | −3.9..−5 ✔ |
| qemu launch → tests start | 16.6 | 14.4 | **−2.2** | −2..−4 ✔ (low end) |
| test phase | 30.1 | 31.2 | **+1.1** | **−9.5** ✘ |
| tail | 5.2 | ~6.0 | +0.8 | 0, because systemd-analyze was not costed |
| **total** | **77** | **71** | **−6** | −17 on a J4-class runner |

**The rebalance failed, and only the rebalance.** It accounts for ~10.6 s [m] of the miss on a like-for-like runner: the writer projected 60–62 s for a J4-speed runner, and it took 71–72 s. The other mechanisms came in at or near their estimates:
- setup-go moved off the path (−5.1);
- the direct kernel boot happened;
- the build finished before ssh (by 5.1 s).

**Where the other ~18 s of the "28 s miss" comes from.** The 40–44 s figure was a *nominal-runner* projection, based on the 55 s baselines (logA). This run is on a slow runner, J4 class or worse:
- the guest restore took 9.2 s (J4 9.5, J3 4.8) [m];
- csi-sanity took 21.8–22.4 s (J4 18.1–18.5, logA 10.5) [m].

Comparing 40–44 with 72 mixes runner classes. The real error is ~10 s, and it is in the test phase.

### Why the rebalance did not shorten the test phase

1. **The weights did split the work as designed.** PropRT (12.78) went to ep.1, the crash sweep (14.15) to ep.0 and ZFSDiff (9.47) to ep.2, each predicted at ~18.2.
   - ep.2 did drop from 29.5 to 19.2 [m].
   - ep.1 went to 30.6 [m] instead of ~18.
2. **The cost of a test is not a property of the test alone. It depends on what runs beside it.**
   - The 196 PropRT subtests took a median of **0.21 s** in the old run and **0.42 s** in the new one. Their p90 went from 0.32 to 0.62 s, and their sum from 45.1 to 79.0 s [m].
   - A parallel subtest's reported time excludes its wait for a `-test.parallel` slot, so this is real per-operation slowdown, not queueing.
   - In the old run, PropRT shared ep.2 with ZFSDiff. ZFSDiff is sequential, and Go resumes the parallel top-level tests only after the sequential ones finish. So PropRT started ~9 s in, after most of the crash sweep, and ran beside little more than csi-sanity [e, from Go's scheduling rules and the shard ends].
   - In the new run it starts at t=0 beside the crash sweep, ZFSDiff and both csi-sanity legs.
   - Other tests also got slower under the denser co-load [m]: csi-sanity +21% (18.3 → 22.1), ZFSDiff +10% (9.0 → 9.95), the crash sweep +11% (55.4 → 61.5 test-s).
3. **The weights model** (cost / nproc for a parallel test, measured in other runs) assumes shards are independent. They are not. All six processes share one 4-vCPU guest (`-smp $(nproc)` on a 4-vCPU runner) and one kernel, with ZFS's global locks.
   - Packing the work into the first ~20 s raised every test's latency, so total throughput hardly changed: the phase was 30.1 s before and 31.2 s after [m], with 31% more test-seconds (207 → 271).
   - The test phase behaves as **throughput-bound on the guest, not balance-bound** [e: strongly suggested by the latencies; not proven, since there is no CPU or PSI data].
4. **ep.1 is also capped by its own slots.** Go's `-test.parallel` defaults to GOMAXPROCS (4 in the guest).
   - ep.1 had 109 parallel test-seconds over 4 slots, so it needs at least 27.3 s. It took 30.6 [m].
   - Each endpoint shard ran at 3.6–3.9 average concurrency [m: cost/wall], which means its slots were full.
   - Weights cannot split a single test, and PropRT alone is 83.6 test-s, so its shard needs at least 20.9 s at this load [m-derived].
   - From uptime ~36.5 to 44.3, ep.1 ran **alone**, with at most 4 concurrent tests on 4 vCPUs. That ~8 s tail is probably not CPU-bound [e; the wall stamps in test-times.tsv would confirm it].
5. **Smaller factor:** two new tests from b03ef6d had no weight and defaulted to 0.1 [m]: TestFilesystemCloneNeverSmallerThanSource (2.84 s) and TestResumeConvergesThickToThin (1.97 s). Both landed in ep.1, adding ~1.2 s of slot time [e].

### The boot

- The direct kernel boot removed the pre-kernel BIOS and GRUB time: qemu launch → guest kernel t0 went from ~5.5 s (J4) to ~1.0 s [m].
- But guest uptime at the start of the tests rose from **11.03 to 13.46 s** [m]. The net saving is only −2.2 s.
- The cause of that +2.4 s is not proven. Candidates:
  - the host was busy during the guest's initrd and userspace. The Go cache unzstd (1.4 s) and `go test -c` (2.8 s on 4 host CPUs) ran at guest uptime ~0–8, sharing the CPUs with qemu's 4 vCPUs [e];
  - the initrd itself takes 4.03 s [m], long for virtio + ext4. The blame shows `vda15.device` (the UEFI partition, /boot/efi) appearing at 4.77 s and `rfkill.device` at 5.08 s [m]. If `boot-efi.mount` sits in local-fs.target's chain, it holds userspace [e].
- graphical.target was reached at 11.54 s, but ssh was up only at ~13.3 s: a 1.8 s gap [m] whose cause is unknown. It may be an ssh probe hanging behind slirp's host-side accept until ConnectTimeout [e].

## 3. Levers to reach ≤45 s, ranked by saving per unit of risk (no check is cut)

| # | lever | saving | risk | evidence |
|---|---|---|---|---|
| 1 | **Take `systemd-analyze` off the path.** Start it in the background over the existing ssh master when the shards start (the boot finished at 11.5 s uptime, before the tests at 13.46). Print its output after the logs | **1.7 s [m]** | ~none. It is a diagnostic, and it is kept | the blame lines are stamped 1.7 s after the "Startup finished" line |
| 2 | **One cache restore instead of two.** Put the qemu tree in the guest's entry (key = guest key + runner image version), or restore qemu concurrently, for example by fetching the guest in a background step | **~2.0–2.2 s [e]** (the qemu step is 2.4 s [m], for 23 MB ≈ 0.2 s of bytes) | low. A runner image bump then also misses the guest unless qemu falls back to apt (13 s cold [m]). Concurrency avoids that | step timings in both runs |
| 3 | **Split the VM job across two runners:** endpoint (3 shards) on one, csi (other + mount + block) on the other. Each restores and boots its own guest in parallel. The check-skips inputs already split by package (`endpoint.log` vs `csi-mount.log` / `csi-block.log`) | **~10–13 s [e]** off the 31.2 s phase. The endpoint leg loses csi-sanity's co-load, and the csi leg runs ~3 processes on 4 vCPUs (csi-sanity took 18.1 s in J4 with less load, and 10.5 s in logA) | medium. Twice the restore and boot runner-minutes; two legs to keep in sync; both must report | the phase did not shrink when the work was rebalanced inside one guest (30.1 → 31.2 [m]); per-test latency rose with co-load (PropRT 2.0×, csi-sanity 1.21×, ZFSDiff 1.10× [m]). Doubling the vCPUs is the only lever that matches a throughput-bound phase |
| 4 | **Shard inside TestPropertyRoundTrip and lift the slot cap of the endpoint shards.** Use an env-selected subtest partition such as `PROPRT_SHARD=i/n`: every subtest runs exactly once, so there is no coverage change. That spreads its 83.6 test-s over 3 processes instead of one 4-slot process. Also pass `-test.parallel=8` to the endpoint shards, so a shard's solo tail is not capped at 4 | **3–8 s [e]**: ep.1's solo tail was ~8 s [m] and its slot floor 27.3 s [m] | low to medium. It is a test-harness change (the Go-test owner). Higher parallelism can surface races, but that adds coverage, it cuts none | §2 points 2 and 4. It pays most with lever 3, because inside one saturated guest it trades slot wait for latency |
| 5 | **Boot trims.** In this order: (a) measure `systemd-analyze critical-chain ssh.service nfs-server.service` (one line); (b) `noauto` or `nofail,x-systemd.device-timeout` for /boot/efi in the provisioned fstab; (c) a smaller initrd (`MODULES=dep`), or no initrd with `root=/dev/vda1` if the Ubuntu generic kernel has virtio_blk and ext4 built in; (d) start the build before qemu launches, or nice it, so it does not compete with the boot | **2–4 s [e]** of the 13.4 s boot. (c) also shrinks the restore (+82 MB cost ~0.7 s [m-derived at 123 MB/s]) | medium. Boot config is fragile, but the kernel-release assert and the zfs/nfsd checks still gate it | initrd 4.03 s, vda15 appearing at 4.77 s, uptime 11.03 → 13.46 with the build overlapping the boot [m] |

Not on the list:
- **A second guest-restore speedup by shrinking the disk** (docs, locales): ~0.8 s per 100 MB [m-derived]. The size gain is not measured.
- **Moving checkout and the keys step before the restore:** ≤1 s [e].
- **Raising `-test.parallel` alone, without lever 3 or 4:** in a saturated guest it mostly turns slot wait into latency [e].

### Projection (estimates)

- This run's runner (71 s): levers 1+2+3+4+5 give −19 to −29 s, landing at **~45–52 s**.
- A nominal (J3/logA-class) runner, whose restore is ~4.5 s shorter and whose test phase is shorter: **~38–45 s**. That meets the 45 s operating target nominally, but not on a slow runner.
- Without lever 3, the rest give −9 to −16 s: ~55–62 s on this runner class. **The 45 s target needs more guest CPU than the one 4-vCPU guest has**. That means a second runner (lever 3), or a larger runner (same effect as 3 with one guest, at a cost in money rather than risk; not measured).

### Before acting, measure (zero risk)

- Record the guest's CPU busy share and `/proc/pressure/cpu` over the test phase, from `/proc/stat` before and after. This turns "throughput-bound [e]" into a measurement and decides between levers 3 and 4.
- Let the artifact's `test-times.tsv` wall column be read: run `ci/real-zfs-weights.sh` where the blob host is reachable. It shows whether PropRT occupied ep.1's solo 8 s tail.
- Add the weights for the two unweighted tests.
