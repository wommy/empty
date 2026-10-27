# Wave 14: CI levers on `ci-levers` (from 7eb878a, not pushed)

Branch `ci-levers`, worktree `/home/user/empty/.claude/worktrees/agent-af94e460282111105`. One commit per lever:

| # | commit | lever |
|---|---|---|
| 1 | bd874bf | systemd-analyze runs beside the tests, not after them |
| 2 | b0addff | the guest and the qemu tree restored as one cache entry |
| 3 | e9c5bd8 | TestPropertyRoundTrip's rows split across the 3 endpoint shards; endpoint shards get `-test.parallel=8` |
| 4 | 550e9d2 | /boot and /boot/efi set to noauto; critical-chain added to the boot report |
| 5 | 0f332d1 | guest CPU and PSI snapshot plus vmstat over the test phase |
| 6 | 1f72f7f | TestModel (and TestRealFacts) weighted from a passing run; test-times.awk double count fixed |

Tags: **[m]** measured, **[e]** estimated.

## Anomalies (read first)

1. **The first CI run of this branch will be cold, so it cannot confirm the timings.**
   - Lever 2 moves the key prefix from `zfsvm-v3` to `zfsvm-v4`, and lever 4 edits the cloud-config. Either one alone forces reprovisioning (about 115 s or more).
   - That first run saves a PR-scoped entry, so the **second** push on the PR is the first warm, measurable run.
   - `main` stays cold until cache-warm.yml runs after the merge. Run `workflow_dispatch` once right after merging.
2. **ci/test-times.awk double-counted nested parallel subtests (fixed in lever 6).**
   - TestModel's 8 parallel workers sit under the sequential subtest `seqs`. `t.Run("seqs")` waits for them, so TestModel's own 3.05 s already includes them.
   - The awk added their 6.05 s again, giving cost_s 9.10 [m, run 40497090324].
   - The fix counts a parallel descendant only when every test between it and the top-level test is parallel. Every other row of both runs I checked comes out the same.
3. **ci/real-zfs-weights.sh would have under-weighted a split test (fixed in lever 3).** With TestPropertyRoundTrip in 3 shards, one run has 3 rows for it. The script averaged them (giving about 1/3 of the total) where it should sum them within a run. It now sums within a file and averages across files.
4. **I also changed TestRealFacts, which you did not ask for.** Its weight was 1.50, kept from a file where it was never measured. It reports 6.41 s sequential [m, run 40497090324]. I set it from the same passing run as TestModel, in the same commit. Revert that line if you want to keep strictly to the brief.
5. **Two tests still have no weight** and default to 0.1:
   - TestFilesystemCloneNeverSmallerThanSource: 2.32 s parallel, about 0.58 weight [m];
   - TestResumeConvergesThickToThin.

   Left alone, since rerunning `ci/real-zfs-weights.sh` on the new artifact picks them up.
6. **I did not trim the initrd.** On 26.10, initramfs-tools `MODULES=dep` may not apply, because the default may be dracut. Booting with no initrd needs virtio_blk and ext4 built in, plus a `root=` the kernel can resolve without an initrd, and I cannot boot-test either here.
   - A broken initrd would fail cold provisioning in every scope. The new guest key means there is no old entry to fall back on.
   - The boot line now logs the stored initrd's size, and the background report adds `critical-chain`. Those decide the next step.
7. **The job logs are reachable, but the artifacts are not.** `gh run view --log` (the run zip) gets 403, while the GitHub MCP `get_job_logs` returns the full job log. That is how lever 6 got real shard logs. The artifact blob host is still refused.
8. **The old boot report piped `blame` into `| head -n 15`** inside the guest. That shell had no pipefail, so it was harmless. It is now `awk "NR <= 15"`, like every new pipe. A scan of the diff finds no `| head`, `| grep -q` or `| grep -m` added.
9. **Several things are unverified until a runner executes them:**
   - `-test.parallel=8` is a judgement call. In a CPU-saturated guest it converts slot wait into latency. Lever 5's data says which.
   - `cache-matched-key` being empty on a miss, and `restore-keys` with `lookup-only`, rely on actions/cache v4.2.3 behaviour. I did not run either.

## Per lever

### 1. systemd-analyze off the critical path (bd874bf)

- **What changed:** after the guest checks pass, `systemd-analyze` and the blame top 15 now run in the background over the ssh master. They first wait (`timeout 60 systemctl is-system-running --wait`) for the boot to settle, run while the tests run, and are printed in the same group after the shard logs. The step is best effort and never gates the job.
- **Saving:** 1.7 s **[m]** in run 40494213342, and 1.14 s **[m]** in run 40497090324 (group stamps 11:34:40.96 to 11:34:42.10).
- **The first warm run must confirm that:**
  - the `systemd-analyze` group holds the Startup line, the blame lines and the critical chain (added in lever 4);
  - the gap between the last shard-log group and the check-skips lines is under about 0.2 s.

### 2. One cache entry for the guest and the qemu tree (b0addff)

- **Keys:** `.github/actions/real-zfs-keys` now outputs
  - `vm` = `zfsvm-v4-<series>-<sum>-<ISO week>-<cloud-config hash>-qemu-<ImageOS>-<ImageVersion>-<qemu-tree.sh hash>`;
  - `vm-fallback` = the same up to `-qemu-`.

  ci.yml, nightly.yml and cache-warm.yml all restore the paths `zfsvm-cache` and `qemu`, in that order, with that key and restore-keys. There is no hashFiles in the action.
- **Runner image release:** the fallback restores the guest together with the old qemu tree.
  - In ci.yml, `qemu-tree.sh` verifies that tree or rebuilds it, as before.
  - In cache-warm.yml a fresh tree is always built for a non-exact hit. That run then saves the new key **without reprovisioning**. Before this change, a new image only cost the qemu entry.
  - If qemu falls back to apt, the qemu directory is left empty so the guest is still saved.
- **Saving:** about 1.3–2.2 s **[e]**.
  - The second restore step took 2.4 s [m] in run 40494213342 and 1.49 s [m] in run 40497090324, of which about 0.13 s is bytes (23 MB).
  - The merged entry adds those 23 MB to the first restore: about 0.15 s at 150–180 MB/s.
- **Stub:** `wave14/ci-levers/stub-keys.sh` checks that the key is stable for the same inputs, that a new runner image changes `vm` but not `vm-fallback`, and that the new key still prefix-matches the old guest.
- **The first CI runs must confirm:**
  - **Cold run:** the restore step logs a miss; cloud-image-utils installs; the save step saves `zfsvm-v4-…-qemu-…`, and its size is about 876 + 23 MB.
  - **Second (warm) run:** the job has one restore step, with `Cache restored from key: zfsvm-v4-…`. `set up qemu` passes verify. The restore takes about the old guest restore time plus about 0.2 s, and there is no separate qemu step.
  - **cache-warm after merge:** a dispatch provisions and saves once. A second dispatch is a lookup-only no-op.

### 3. TestPropertyRoundTrip split across the endpoint shards (e9c5bd8)

- **Test:** the test reads `PROPRT_SHARD=<i>/<n>` and runs the rows whose index is i mod n. Unset, it runs every row (`0/1`), so the simulator suite is unchanged. An invalid value fails the test. It logs `PROPRT_SHARD i/n: k of N rows`.
- **Guest:** `split='^TestPropertyRoundTrip$'` in `shard()` places the test in **every** endpoint shard with `PROPRT_SHARD=i/3`. Each shard gets 1/3 of its weight before the longest-first placement of everything else. The endpoint shards run with `-test.parallel=8`.
- **Coverage:**
  - `ci/check-skips.sh` still proves on the merged endpoint log that every listed test reported PASS or SKIP. TestPropertyRoundTrip passes 3 times, and the list is de-duplicated.
  - The new `ci/check-split.sh` fails unless:
    - there is exactly one partition line per shard 0..n-1;
    - n and N agree across shards and the k sum to N;
    - exactly N subtests reported a result, none in two shards.
- **Stub:** `wave14/ci-levers/stub-shards.sh` builds endpoint.test and runs the guest's own `shard()` (extracted verbatim) on the real list and weights. It checks that every listed test is in exactly one shard and TestPropertyRoundTrip is in 0, 1 and 2.
  - It runs the three shards on the simulator exactly as the guest does. check-split reports 196 subtests over 3 shards, each exactly once, and check-skips shows no listed test missing.
  - Six negative cases are all refused: a shard log missing, one shard twice, two shards claiming 1/3, a row run in two shards, a missing partition line, a row that never reported.
  - Predicted shard loads at HEAD: 22.13 / 22.13 / 22.13 s (the weights are from older, slower runs).
- **Go:** `go vet ./...` is clean and `go test -race -count=1 ./...` passes.
- **Saving:** 2–5 s **[e]** on a 54 s-class runner, 3–8 s **[e]** on a 72 s-class one (the wave 13 range).
  - In run 40497090324, endpoint.1, which held PropRT's 52.97 test-s, ended the phase at 21.5 s, while endpoint.0 took 17.9 s and the others 13.6–14.4 s [m].
  - Splitting removes PropRT's single-shard slot floor. If the guest is CPU-bound, much of the gain turns back into latency, and lever 5 shows how much.
- **The first warm run must confirm:**
  - check-split prints `TestPropertyRoundTrip: 196 subtests over 3 shards, each exactly once`;
  - the skip budget is unchanged at `vm/endpoint: 17 skipped, 17 allowed`;
  - `shards.txt` has the three endpoint walls within about 3 s of each other, and the phase (start to end uptime) is shorter than 21.8 s [m, run 40497090324] on a comparable runner;
  - in test-times.tsv, the per-shard PropRT rows (about 65 subtests each) have a median subtest time not far above the 0.21 s of the uncontended run.

### 4. Boot trims (550e9d2)

- **What changed:** the cloud-config's runcmd appends `noauto` to the fstab entries for `/boot` and `/boot/efi` (an idempotent awk that keeps comments and existing noauto entries).
  - The change takes effect from the next boot, so the provisioning boot still copies the kernel out of `/boot`.
  - Warm boots use the stored kernel and initrd, or GRUB, which reads the partitions itself.
  - In run 40497090324 the blame shows vda13 (label BOOT) at 4.16 s; in 40494213342, vda15 (UEFI) at 4.77 s [m].
- **Checks:** the version, module, nfsd and kernel-release asserts still run on every boot. A stored disk is cached only after a boot from it passes them.
- **Report additions:** the background report adds `systemd-analyze critical-chain ssh.service nfs-server.service` and `mounted under /boot: …`. The boot line logs the initrd size in KiB. The initrd itself is unchanged (anomaly 6).
- **Stubs:**
  - `stub-fstab.sh` runs the YAML's command on a sample cloud-image fstab, twice: both entries get `,noauto`, root does not, and existing noauto lines and comments survive.
  - `stub-analyze.sh` runs the remote command through sh with stubs, including a 2000-line blame: the output is cut to 15 lines with no closed pipe.
- **Saving:** 0.3–1.5 s **[e]**, against a measured boot of 9.12 s (1.12 kernel + 3.66 initrd + 4.34 userspace) and ssh up at +10.24 s [m, run 40497090324].
- **The first warm run must confirm:**
  - `mounted under /boot:` is empty;
  - the boot.mount and boot-efi.mount units are gone from blame and critical-chain;
  - the userspace time in the Startup line is below 4.34 s, and `timeline: ssh up` is below +10.2 s on a comparable runner;
  - the version assert line and `direct kernel boot: …, initrd N KiB` are present.

### 5. Guest CPU and pressure snapshot (0f332d1)

- **What changed:** the guest snapshots `/proc/stat` and the PSI totals of `/proc/pressure/{cpu,io,memory}` before and after the shards, and runs `vmstat -n -t 1` in the background across the phase. It writes `out/load.txt` with:
  - the phase wall and vCPU count;
  - busy, user, sys+irq, **steal**, idle and iowait shares;
  - the stalled share for cpu/io/memory, `some` and `full`.
- **Where it shows:** the host prints a log group titled with the CPU line (load.txt and vmstat) and adds load.txt to the job summary. The artifact uploads load.txt and vmstat.txt. These are diagnostics only and cannot fail the run.
- **Stub:** `stub-load.sh` runs the exact lines under mawk on this machine's /proc around a 3 s busy phase. Output: `cpu: busy 96% (user 86%, sys+irq 9%, steal 1%)…`, `pressure …: cpu some 49%, …`.
- **Cost:** under 0.1 s **[e]** (one vmstat process).
- **The first warm run must confirm:** the `guest load (test phase)` group exists, and its pressure line is not `PSI unavailable`.
- **Data needed for the two-runner split decision** (not done in this batch), from two or three warm runs:
  - **CPU busy share and cpu `some` over the phase.** Busy at or above about 85% with cpu some at or above about 40% means the phase is throughput-bound. Then only more vCPUs help, and the split (estimated −10 to −13 s in wave 13) pays.
  - **Steal share.** A high value (≥ 10%) means the host's 4 CPUs are contended: qemu plus host work. A second runner helps there too, and a bigger guest on the same host would not.
  - **io `some`/`full` and iowait.** If io dominates while CPU busy is below about 70%, the guest is waiting on ZFS or txg rather than compute. Then the split pays less, and `-test.parallel` or the pool layout is the lever.
  - **The vmstat `r` column during the endpoint.1 solo tail,** the last few seconds after the other shards end. An `r` of 4 or below with idle CPU means the tail is slot- or latency-bound, not CPU-bound.
  - **The phase wall next to the shard walls in `shards.txt`.**

### 6. TestModel's weight (1f72f7f)

- **Derived, not estimated.** Passing run 40497090324 (18cbaea, VM job 122165163972) reports `--- PASS: TestModel (3.05s)`.
  - On the real pool, TestModel runs 2 sequences (MODEL_BUDGET 50 / 25 steps). Only workers w0 (3.05 s) and w1 (3.00 s) do work, inside the sequential `seqs`, so own time = wall = 3.05 s [m].
  - Weight: **5.00 → 3.05**.
  - The 16–24 s of the earlier runs was failure plus shrinking, not a passing cost.
- **TestRealFacts:** 1.50 → 6.41 [m, same run] (anomaly 4).
- **Source:** the source line in the weights file names the run. Both tests moved into the measured, sorted section.
- **Stub:** `stub-testtimes.sh` runs the old and new awk under mawk on both runs' shard logs. Only TestModel's cost changes (9.10 → 3.05). A synthetic log checks that nested parallel-under-parallel still counts. `stub-weights.sh` checks the split-row summing: 21 and 10 average to 15.50.
- **Saving:** balance only, 0–1 s **[e]**.
- **The first warm run must confirm:** the TestModel row in test-times.tsv has a wall_s close to 3 s. That row comes from the stamped marks, which the stub logs lack.

## Projection (estimates)

| | 54 s-class (run 40497090324) | 72 s-class (run 40494213342) |
|---|---|---|
| L1 | −1.1 [m] | −1.7 [m] |
| L2 | −1.3 [e] | −2.2 [e] |
| L3 | −2 to −5 [e] | −3 to −8 [e] |
| L4 | −0.3 to −1.5 [e] | −0.3 to −1.5 [e] |
| L5, L6 | about 0 | about 0 to −1 |
| **landing** | **~45–49 s** | **~59–65 s** |

The ≤60 s budget looks reachable on a typical runner. The 45 s operating target is reachable at best on a fast one. On slow runners it most likely needs the two-runner split, which lever 5's data now decides.

## Files

- Repo: `ci/check-split.sh` (new); `ci/real-zfs-vm.sh`, `ci/real-zfs-guest.sh`, `ci/test-times.awk`, `ci/real-zfs-weights.sh`, `ci/real-zfs-weights.txt`, `ci/real-zfs-cloud-config.yaml`, `internal/endpoint/props_roundtrip_test.go`, `.github/actions/real-zfs-keys/action.yml`, `.github/workflows/{ci,nightly,cache-warm}.yml`.
- Stubs in `/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave14/ci-levers/`: `stub-keys.sh`, `stub-shards.sh`, `stub-weights.sh`, `stub-fstab.sh` (with `fstab.sample`), `stub-analyze.sh`, `stub-load.sh`, `stub-testtimes.sh` (with `test-times.old.awk`). Run each with `bash <stub> <repo>` from the repo root; `stub-shards.sh` takes no argument.
- Data: `/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave14/ci-levers/logs/vm-40497090324.log` (full VM job log). The split shard logs and `tt.tsv` are in `/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave14/ci-levers/logs/r54/`.
- Validation: actionlint is clean; `bash -n` is clean on every changed script; `go vet ./...` is clean; `go test -race -count=1 ./...` passes.
