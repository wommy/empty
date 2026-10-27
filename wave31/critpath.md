# Push-to-green critical path: last 6 green ci.yml runs (read-only)

Runs (short ids): r887 = 40549402887, r228 = 40550837228, r783 = 40551637783, r712 = 40551837712,
r632 = 40552465632, r012 = 40554288012. The wall times (created to updated) are 43, 46, 39, 49, 42 and 48 s, median 44.5 s.
r632 and r012 have nice 19 on the build. Run 96 (40555805200) was still in progress, so there is no third green run yet.
Data are the full logs of all 18 VM jobs (wave31/logs), parsed by wave31/bin/parse.py into rows.json.
All times are in seconds after the run was created. The VM-relative columns (built, login, ssh) are "+s after qemu -daemonize".

## 1. Per-job timeline
| run | job | pickup | 1st log | pre-restore | restore s (MB/s) | qemu up | built | login | ssh | tests start | test wall | post | teardown | done |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| r887 | p0 | 3 | 4.5 | 3.2 | 4.8 (150) | 12.9 | +8.6 | +12.0 | +13.0 | 27.1 | 9.5 | 1.6 | 1.9 | 40 |
| r887 | p1 | 3 | 4.8 | 4.2 | 6.4 (106) | 15.7 | +8.5 | +12.7 | +13.6 | 30.4 | 7.5 | 2.2 | 1.9 | 42 |
| r887 | csi | 3 | 4.7 | 2.6 | 5.2 (126) | 12.7 | +6.6 | +10.9 | +11.4 | 24.9 | 3.6 | 1.4 | 2.0 | 32 |
| r228 | p0 | 3 | 5.9 | 2.8 | 6.6 (103) | 15.6 | +8.6 | +12.7 | +13.6 | 30.2 | 10.5 | 1.9 | 2.4 | 45 |
| r228 | p1 | 3 | 5.6 | 3.4 | 7.7 (84) | 17.1 | +6.8 | +10.6 | +11.2 | 29.1 | 5.4 | 2.0 | 2.5 | 39 |
| r228 | csi | 3 | 5.5 | 3.3 | 6.3 (107) | 15.3 | +8.3 | +12.3 | +13.3 | 30.1 | 5.9 | 1.5 | 1.4 | 39 |
| r783 | p0 | 4 | 4.9 | 2.6 | 4.0 (172) | 11.7 | +7.8 | +11.6 | +12.4 | 25.2 | 9.8 | 1.4 | 1.6 | 38 |
| r783 | p1 | 4 | 5.1 | 3.0 | 3.2 (256) | 11.6 | +9.2 | +13.5 | +14.4 | 27.3 | 8.4 | 1.9 | 0.5 | 38 |
| r783 | csi | 6 | 6.9 | 3.4 | 7.0 (91) | 17.8 | +6.4 | +10.4 | +11.0 | 29.7 | 3.8 | 1.5 | 2.1 | 37 |
| r712 | p0 | 10 | 11.9 | 3.0 | 11.1 (55) | 26.4 | +6.0 | +10.6 | +11.2 | 38.3 | 6.4 | 1.8 | 2.5 | 49 |
| r712 | p1 | 3 | 5.3 | 3.3 | 3.3 (246) | 12.8 | +8.7 | +12.7 | +13.6 | 27.4 | 8.5 | 1.8 | 1.4 | 39 |
| r712 | csi | 3 | 5.7 | 2.9 | 8.1 (81) | 17.3 | +9.6 | +13.5 | +14.4 | 33.1 | 5.9 | 1.8 | 2.2 | 43 |
| r632 | p0 | 3 | 5.1 | 5.2 | 5.2 (136) | 16.0 | +9.5 | +11.1 | +12.1 | 29.1 | 9.5 | 1.0 | 1.4 | 41 |
| r632 | p1 | 2 | 4.7 | 3.3 | 5.9 (110) | 14.3 | +6.7 | +9.3 | +9.9 | 24.9 | 5.5 | 0.4 | 1.2 | 32 |
| r632 | csi | 2 | 4.7 | 5.0 | 7.6 (84) | 17.7 | +7.8 | +10.0 | +10.6 | 29.4 | 4.0 | 1.4 | 1.2 | 36 |
| r012 | p0 | 3 | 5.7 | 2.9 | 11.7 (53) | 20.9 | +9.3 | +11.2 | +12.1 | 34.0 | 9.5 | 0.8 | 2.6 | 47 |
| r012 | p1 | 3 | 5.9 | 4.1 | 11.1 (57) | 21.5 | +9.1 | +10.2 | +11.1 | 33.6 | 9.6 | 0.6 | 3.1 | 47 |
| r012 | csi | 3 | 5.1 | 3.5 | 5.8 (114) | 15.1 | +6.4 | +8.7 | +9.3 | 25.1 | 3.4 | 1.5 | 2.0 | 32 |

Columns: pickup = job created→started (API); 1st log = run created→runner's first line; pre-restore = set-up+checkout+kvm+keys;
restore = lookup+download+untar; tests start = guest `start at uptime`; post = phase end→"Post job"; teardown = "Post job"→completed (API).
static and test always finish 9-25 s before the VM jobs and are never critical.

**Critical job and its segments, in s:**

| run | last job | first log | pre | restore | qemu | launch to tests start | tests | post | teardown | wall |
|---|---|---|---|---|---|---|---|---|---|---|
| r887 | p1 (p0 2 s behind) | 4.8 | 4.2 | 6.4 | 0.3 | 14.7 | 7.5 | 2.2 | 1.9 | 43 |
| r228 | p0 | 5.9 | 2.8 | 6.6 | 0.3 | 14.6 | 10.5 | 1.9 | 2.4 | 46 |
| r783 | p0 = p1 | 4.9 | 2.6 | 4.0 | 0.2 | 13.5 | 9.8 | 1.4 | 1.6 | 39 |
| r712 | p0 | **11.9** | 3.0 | **11.1** | 0.4 | 11.9 | 6.4 | 1.8 | 2.5 | 49 |
| r632 | p0 | 5.1 | 5.2 | 5.2 | 0.5 | 13.1 | 9.5 | 1.0 | 1.4 | 42 |
| r012 | p0 = p1 | 5.7 | 2.9 | **11.7 / 11.1** | 0.5 | 13.2 | 9.5 | 0.8 | 2.6 | 48 |

Path: created → runner up (median 5.2) → checkout/keys (3.3) → restore (6.4, 3.2-11.7) → qemu (0.4) → boot to tests start (13.3)
→ tests (9.0) → post (1.5) → teardown (1.9) → run updated (+1). setup-go and the host build are never critical ("built" before
"login" in 18/18, by 1.0-4.4 s). csi was never last (margin 1-15 s; its phase is 3.4-5.9 s against 5.4-10.5 s for the endpoint parts).

**Variance against systematic cost.** done-time variance across the 12 endpoint jobs is 21.6 s². Covariance shares: restore 48%,
pickup/runner start 26%, test phase 13%, teardown 12%, boot about 0%, pre-restore and post 0%.

So the two runner-draw terms explain about 3/4 of the spread; the boot is large but steady (13.3 s median, sd 1.4).
- **Restore is a bandwidth lottery (noise).** The entry is always 566.1 MB (`zfsvm-v5`). Lookup 0.2-0.4 s, untar 0.5-0.9 s,
  download at 53-256 MB/s (median 107). Slow draws cluster by run (r012 p0/p1 53/57, r712 p0 55): the two slowest runs, 48 and 49 s.
- **Pickup (noise):** 2-4 s normally; outliers r712 p0 10 s, r783 csi 6 s. First log line comes 1.5-2.7 s after "started".
- **Systematic, every run:** boot→tests start 13.3 s; pre-restore 3.3 s (checkout 1.0-2.3, keys 0.5-1.2); teardown 1.9 s;
  post 1.5 s (0.4-1.0 s since the endpoint upload became failure-only in r632/r012, against 1.4-2.2 s before).
- **Systematic gate cost inside the boot.** "login:" appears 1.38-1.76 s (mean 1.56, all 18 jobs) after systemd's "Startup finished"
  (graphical.target, so after systemd-user-sessions), and 2.6-4.0 s (mean 3.4) after ssh.service started. Kernel clock starts
  about 1.0-2.1 s after qemu launch (`Accepted publickey [t]` against "ssh up"). After the gate: ssh handshake 0.6-1.0 s, then
  `modprobe zfs` 0.49-0.89 s (backgrounded at ssh up, still the long pole), then nfs + start 0.3 s.

## 2. nice -n 19 probation (rule: median endpoint `login prompt` ≤ 11.15 s over 3 green runs, `built` before it)
| run | p0 login / built | p1 login / built | run median |
|---|---|---|---|
| 40552465632 | 11.100 / 9.541 | 9.298 / 6.698 | 10.20 |
| **40554288012** | **11.247 / 9.277** | **10.190 / 9.145** | **10.72** |

- 4 jobs pooled: median 10.65 s. `built` came before `login` in all of them, by 1.0-2.6 s.
- No third green run yet: 40555805200 was in progress.
- Verdict so far: on track to keep. With per-run medians the rule is already met whatever run 3 shows (median of {10.20, 10.72, x}
  ≤ 10.72). Pooling all 6 jobs, it fails only if both of run 3's endpoint logins exceed about 11.2 s (median then 11.175).
- Supporting evidence, 8 pre-nice endpoint jobs (r887, r228, r783, r712) against nice: login median 12.35 vs 10.65 (Mann-Whitney
  U=4 of 32, one-sided p≈0.03); guest "Startup finished" mean 8.91 vs 7.31 s; build mean 8.03 vs 8.65 s (slower, as expected);
  csi logins 10.4-13.5 vs 8.7/10.0. Consistent mechanism, but n is small and host speed confounds it.

## 3. Levers ranked by expected saving on the critical path (median run), then risk
Estimates replay each lever on the 6 runs and recompute the max over 3 VM jobs. ADR = within existing ADR 0005 mechanisms.
1. **Gate ssh on systemd-user-sessions finishing, not on agetty's "login:"** (ADR, ssh poll section). **−1.3 to −1.5 s**
   (replay median 1.47, mean 1.35). Low risk, and no cache key bump.
   - The gate exists to avoid PerSourcePenalties while /run/nologin exists; user-sessions has finished by graphical.target and
     "login:" lands about 1.56 s later in all 18 jobs. Break on the console's "Finished systemd-user-sessions.service" line (no `quiet`
     on the cmdline, so status lines reach ttyS0), keeping "login:" and the 60 s gate as fallback. Safest first step: mark it without moving the gate.
   - Cap: the host build then becomes critical in some jobs (r012 p1 saves only 0.74 s: built +9.15 against a gate at about +8.7). Re-judge nice after.
2. **Mask more boot units on the kernel cmdline** (ADR, same `systemd.mask=` mechanism as the zfs-import/udev-settle masks).
   **−0.5 to −1.5 s**. Low-medium risk, no key bump.
   - blame (r012 p0): proc-fs-nfsd 1.53, networkd-wait-online 1.39, networkd 1.38, resolved 1.09, rsyslog 0.82, chrony 0.81, binfmt 0.76,
     networkd-dispatcher 0.74, udisks2 0.73, sysstat 0.70, apparmor 0.59, netplan-configure 0.50. Userspace swings 5.1-9.1 s with host
     contention, so fewer units on 4 contended vCPUs shortens graphical.target. Candidates: chrony, rsyslog, binfmt, networkd-dispatcher,
     udisks2 (also 0.44 CPU-s in the test phase), sysstat; not apparmor (behaviour differs from production).
   - Check first whether user-sessions sits behind network-online (wait-online 1.39 s would then be on the gate): add
     `systemd-analyze critical-chain systemd-user-sessions.service serial-getty@ttyS0.service` to the analyze block.
3. **Shrink the guest entry by about 100 MB** (ADR, cache bytes section). **−1.0 s median, −1.8 s on slow-bandwidth runs.** Medium risk.
   - Replay per 100 MB: 0.39-1.83 s, median 0.96; −69 MB gives 0.66. Sources: the disk's /boot initrd (69 MB, kept for the panic
     fallback that never fired in 18 jobs; dropping it reverses an ADR decision), and a kernel-module allowlist (lsmod at provisioning
     + zfs, nfsd and deps) instead of the denylist (size unknown: log `du /lib/modules` on the next cold run). Cost: key bump, one ~115 s cold run per job.
4. **Load zfs during the boot, overlapping the gate:** **−0.5 to −0.8 s** (modprobe is 0.49-0.89 s, on the path at ssh up).
   - A guest unit means a key bump (`systemd.run=` + `run_success_action=none` on the cmdline would avoid it, unverified). Medium risk: +0.8 CPU-s on a contended boot.
5. **Cheaper pre-restore** (not in the ADR):
   - Replace actions/checkout (1.0-2.3 s) with a shallow `git fetch` run step; background `enable kvm`. **−0.5 to −1.0 s**, low risk.
     Starting the VM before checkout is impossible: the key hashes repo files and needs SHA256SUMS.
6. **Endpoint part balance** (prior wave30 result, ADR): **−0.8 to −1 s** on runs where p0 draws the slow host. Low risk.
7. **Kill qemu at the end of the run phase** (not in the ADR): teardown 0.5-3.1 s (median 1.9) includes the runner's orphan cleanup
   of a 6 GB guest. **0 to −0.5 s** (below the API's 1 s resolution), low risk.
8. **Trim post-phase work** (check-split, test-times print): 0.4-1.0 s left. **About −0.3 s.**
9. **Direct-boot pre-kernel** (kernel clock starts 1.0-2.1 s after launch): PVH entry (pvh.bin is kept) or a lighter machine type.
   **Unknown, about −0.3 to −1 s**; instrument first (mark the first console byte).
10. **Parallel multi-entry restore** via a custom @actions/cache JS step (not in the ADR, which rejected a sequential split, ~2 s overhead).
    Helps only if the 53-57 MB/s draws are per-blob throttling, not runner NIC (inside r012 p0 the rate is flat at 50-60 MB/s).
    Potentially −3 to −6 s on slow runs, ~0 on median ones. High complexity, uncertain.
11. **No gain:** zstd level (clusters already zstd, untar 0.5-0.9 s); restoring fewer paths (qemu tree 8 MB ≈ 0.08 s); job ordering
    (no matrix-order effect on pickup, p0/p1/csi medians all 3 s). A fourth VM job would add one more draw from both lotteries.
**Summary.** Levers 1, 2 and 5 are low-risk and need no cache key bump. Together they should take about 2.5-3.5 s off the
median, from about 44.5 to about 41-42 s. Lever 3 adds about 1 s and cuts the slow-bandwidth tail (48-49 s runs) by about 2 s.
The remaining spread, 39-49 s, is runner draw: pickup up to 12 s and restore bandwidth 53-256 MB/s. Every run stays under 60 s.
