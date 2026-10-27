# Wave 13: cache-warm and the 45s nominal target

Branch `cache-warm` (local, not pushed), rebased on `c1205e9`. Five commits:

| commit | what |
|---|---|
| `f7ef200` | setup-go: Go is downloaded, verified and extracted in the background. The Go cache is keyed by go.sum + commit, with a `save` output |
| `46e735e` | real-zfs job: the guest boots during setup-go; real-zfs-keys action (SHA256SUMS fetched once); direct kernel boot; boot timeline; per-test times (test-times.tsv) and the weights regenerator; nightly gets `ZFS_CRASH_FULL=1` |
| `011d858` | `cache-warm.yml`: runs hourly and on dispatch, on the default branch |
| `1d127d7` | `ci/real-zfs-weights.txt` regenerated from runs 40469622947 and 40471254688 |
| `ae9c86b` | ADR 0005: adopted mechanisms, 45s nominal margin, nightly issue wording, zfsdiff sample note |

Lint:
- actionlint is clean on all three workflows. It does not check the two composite actions, so neither of them has `hashFiles` in its outputs, and real-zfs-keys has no `hashFiles` at all (it uses `sha256sum`).
- `bash -n` is clean on every `ci/*.sh`.
- shellcheck is not installed here, so it was not run.
- No Go code changed.

## Baselines (measured, from the job logs)

| run | head | push-to-green | VM job | setup-go | guest restore | qemu restore | script: gpg/SUMS | qemu start → guest kernel clock | → tests start (guest uptime) | test phase | critical shard |
|---|---|---|---|---|---|---|---|---|---|---|---|
| 40468413047 (logA) | 5b42809 | ~55 | 49s | 3.8 | 7.2 | 1.3 | 0.7 | 3.5 | 9.0 | 18.0 | ep.1 (Crash + PropRT) |
| 40469622947 (J3) | 3ab03d5 | 69 | 62.7s | 3.9 | 4.8 | 0.65 (+0.9 setup) | 0.58 | 5.8 | 10.9 | 30.8 | ep.2 31.2s (ZFSDiff 9.94 + PropRT) |
| 40471254688 (J4) | c1205e9 | 78 | ~68s | 5 (3.5 restore + 1.8 install) | 9.5 | 2.0 | 0.92 | 5.5 | 11.0 | 30.1 | ep.2 29.5s (ZFSDiff 9.00 + PropRT) |

- In J3 the static job took 13s and the test job 22-24s. The VM job is the whole critical path.
- The zfsdiff PR replay: TestZFSDiffReplay took 9.94s (J3) and 9.00s (J4) as test time, and its reported replay wall was 9.738s and 8.87s, against a 5.00 weight.
- csi-sanity, mount and block: 19.5-19.6s (J3), 18.1-18.5s (J4), 10.5s (logA). That is the floor of the test phase on the same runner.

## Changes and expected savings (critical path of the VM job unless noted)

| # | change | saves | basis |
|---|---|---|---|
| 1 | **Weights regenerated from measured per-test data** (`1d127d7`). TestPropertyRoundTrip goes 2.80 → 12.78 (196 parallel subtests, 49-53 test-seconds) and TestZFSDiffReplay 5.00 → 9.47. The three endpoint shards balance at 18.2 each. Under the old weights, PropRT and ZFSDiff landed in one shard: 27.3 in the new units, measured 29.5s | **~9-10s** in J3/J4 (test phase 30 → ~19-20, the csi-sanity floor) | **Estimate from measured data.** The cost model (sequential: own + parallel subtests / nproc; parallel: test-seconds / nproc) reproduces J4's measured shards: ep.0 14.15 predicted vs 14.35 measured, ep.1 13.19 vs 13.79, ep.2 27.29 vs 29.45. The total guest load is unchanged, so csi may slow a little when the tail is filled. Say 8-10s |
| 2 | **The guest boots during setup-go** (S1). The order is now restore guest → restore qemu → boot step (qemu starts, step returns) → setup-go → test step (build, wait, test) | **3.9-5s** (setup-go, J3/J4) | setup-go time is measured. It is hidden only while boot ≥ setup-go + build. The boot window was 12-16s including the overlapped build, so it fits (estimate) |
| 3 | **Direct kernel boot** (S3). A cold run stores vmlinuz, initrd, cmdline and release with the disk. Warm boots pass `-kernel/-initrd/-append`. The booted `uname -r` must equal the stored release, and every other check is unchanged | **~2-4s** | The waste is measured: 3.5-5.8s from qemu start to the guest kernel's clock in 5 runs (logA 3.5, 4f092fd 4.6, f3706a7 5.2, J4 5.5, J3 5.8). The saving is an estimate: qemu start and kernel decompression remain, and the restore grows by the kernel and initrd (~+0.5s) |
| 4 | **SHA256SUMS fetched once.** The keys action fetches SUMS and .gpg in parallel and verifies them. The script copies and re-verifies them (`ZFSVM_SUMS`) | **~0.5-0.8s** | Measured: the script's own fetch and gpg took 0.58s (J3) and 0.92s (J4). The keys step adds the parallel .gpg fetch (~0.1, estimate) |
| 5 | **Go extracted in the background** during the cache restore (setup-go) | 1.4-1.9s per job: in static and test directly; in the VM job it is already hidden by #2 | Measured: the install step was 1.43/1.90/1.61/1.82/1.95s, and the download was always finished (sha OK within 0.09s of the step start). **Caching the Go tarball was not done**: the download was never on the path (measured), so it would save 0s. Background extraction removes the part that was on the path |
| 6 | **GORACE=atexit_sleep_ms=0** in the test job | ~1s of the test job's tail (est.); the job is off the critical path (22-24s) | Measured locally (wave10/atexit.txt): lock 1.43 → 0.21s, unixsock 1.11 → 0.16, zfs-csi 1.14 → 0.12. Races still exit 66 |
| 7 | **One `go test -c -o dir/` for both binaries**, linked in parallel | small, and off the path after #2 | It works locally with Go 1.27.3. Not timed |
| 8 | **The qemu save moved after the tests** | 0 warm. On a qemu miss, the upload no longer delays the boot | – |
| 9 | **cache-warm.yml** (hourly + dispatch, default branch): guest (provision-only, verified boot), qemu tree, and the Go namespaces static, test and real-zfs, keyed by go.sum + commit. Superseded entries on the branch are deleted | Avoids **~115s** (cold guest) and **~13s** (cold qemu) for a PR after any image serial, week roll or runner image change. It keeps Go builds incremental against the default branch's HEAD | Cold costs measured (wave10). The Go compile saving is not measured |
| 10 | **Instrumentation**: timeline marks (binaries built, ssh up, nfs-server active, in ms since qemu start); `systemd-analyze` + blame after the tests; test-times.tsv in the summary and artifact | 0 | So the next round is measured, not guessed |

Projection (estimates; one green run of this branch settles them):
- **J3-speed runner: 69 → ~51-53s.** Savings: −9.5 (#1), −3.9 (#2), −3 (#3), −0.6 (#4), +0.5 (bigger restore).
- **J4-speed runner (the slowest seen): 78 → ~60-62s.** Its guest restore alone was 9.5s, vs 4.8s in J3.
- **Nominal (bdfe867 at 56s, logA at ~55s): → ~40-44s.** That is inside the 45s target. The 1.5x slow tail still lands around 60s.

So the 45s nominal is reachable with this branch by estimate, but a slow runner still sits at the 60s line. More levers, in order of seconds per risk, none of which cuts a check:
1. **Guest boot userland** (~9-11s of guest uptime before the tests). Read `systemd-analyze blame` from the first run and mask whatever sits in front of nfs-server and sshd (likely `systemd-networkd-wait-online` and cloud-init leftovers). Estimate 2-5s.
2. **TestPropertyRoundTrip on the real pool**, at 49-53 test-seconds (the largest after the crash sweep's 55-58). Give it a rotated PR sample with the full matrix nightly, the same pattern ADR 0005 already accepts for crashSample. This is a test change for the Go-test owner. Estimate: −40 test-seconds of guest CPU, so ~3-5s off a saturated test phase.
3. **The zfsdiff PR sample.** It fits after the rebalance: its shard is 17 weight-s against 18.2, with the NFS replay. But at 9.0-9.9s it is close to its own 15s fail line on a 1.5x runner. If that line is approached, shrink `zfsdiff.PRSample` from 21 to about 10 sequences (it stays > 0 on the real pool; the full corpus runs nightly). Estimate ~4.5s off that shard and lower flake risk. This is not done here (Go code), but it is recorded in ADR 0005.
4. **Guest restore** (4.8-9.5s for 794MB): shrink the stored disk by purging docs, locales and man pages in the cloud-config. Unmeasured.

## The zfsdiff replay in the plan (coordinator's question)

- Measured real wall: **9.74s (J3) and 8.87s (J4)** for the 21-sequence PR sample. The test's own time was 9.94 and 9.00.
- It is sequential and mostly zfs(8) latency. With the measured weights it shares shard 2 with TestNFSReplay, at 17.0 weight-s, under the 18.2 balance point and below csi-sanity's ~18.6s. **It fits, so I propose no shrink now.**
- The trigger to shrink is the replay's own wall reaching ~12s (its stop-starting line), or shard 2 becoming the critical shard in test-times.tsv.

## Nightly (item 4)

- `real-zfs-full` sets `ZFS_CRASH_FULL=1` next to `ZFSDIFF_FULL=1`. The script passes both into the guest, and the guest exports them (crashSample reads `ZFS_CRASH_FULL`).
- The issue body lists the `--- FAIL`/`panic:` lines from the endpoint shards plus the zfsdiff tally.
- test-times.tsv from a nightly run is marked `mode=full`, and the weights script skips it.
- Time: the full sweep was ~29s of 2 vCPUs (ADR), and the shard timeout is 4m. Verify on the first nightly.

## Needs a first CI run to verify

1. **Run `cache-warm.yml` once right after pushing** (`gh workflow run cache-warm.yml --ref claude/gallant-johnson-1qaxp5`). This branch changes the guest key (v2 → v3), the qemu key (sha256sum instead of hashFiles) and the Go key format. Without that run, the first PR run is cold (~115s guest + ~13s qemu). Go falls back to the namespace prefix and saves once.
2. Direct kernel boot: the cold run's log shows `direct kernel boot: <release>, cmdline: ...`. The warm run's summary shows `(direct kernel boot)`. Read `timeline:` marks and compare qemu start → guest uptime 0 against 3.5-5.8s.
3. qemu survives from the boot step to the test step. The static job's background buf download relies on the same runner behaviour: orphans are reaped only at job end.
4. `timeline: test binaries built` vs `ssh up`. If the build lands after ssh, the build is the critical path, not the boot. Then move setup-go back before the restores and build in the background during the restore.
5. test-times.tsv now has a wall column (stamped lines). Rerun `ci/real-zfs-weights.sh` on the artifacts of 2-3 runs and compare with these model weights.
6. Composite actions are not linted. Check that `steps.go.outputs.save` and `steps.keys.outputs.{guest,qemu,sums}` resolve (a typo there shows only on the runner).
7. `gh cache list --ref` and `gh cache delete` with `actions: write` on the runner's gh.
8. Static and test job wall after the background extraction (expect about −1.5s each), and the test job with GORACE.
9. The nightly full crash sweep's duration against the 4m shard timeout.

## Files

- Changed:
  - `.github/actions/setup-go/action.yml`
  - `.github/workflows/ci.yml`
  - `.github/workflows/nightly.yml`
  - `ci/real-zfs-vm.sh`
  - `ci/real-zfs-guest.sh`
  - `ci/real-zfs-weights.txt`
  - `docs/adr/0005-one-minute-ci-budget.md`
- New:
  - `.github/actions/real-zfs-keys/action.yml`
  - `.github/workflows/cache-warm.yml`
  - `ci/test-times.awk`
  - `ci/real-zfs-weights.sh`
- Scratch, in `wave13/cw/`:
  - `jlog.sh`: a job log → shard logs → test-times.tsv;
  - `shardsim.sh`: replays the sharder;
  - `vmstub.sh`: the boot/run split, direct boot and the kernel mismatch check, run against stubbed qemu, ssh and gpgv;
  - `stamptest.sh`: the stamping pipeline on the simulator;
  - `gotest.sh`: the setup-go background step, with a good and a bad sha;
  - `lint.sh`;
  - `j3/` and `j4/`: per-shard logs and test-times.tsv.
