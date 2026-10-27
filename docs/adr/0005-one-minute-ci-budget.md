# 5. Tests and CI finish in under one minute

Status: accepted (budget, 45s nominal margin); mechanisms adopted where marked, the rest proposed · Date: 2026-10-27 · Related: ADR 0004 (the 2.4 VM is the only real-ZFS gate).

## Context

- A green PR run takes about 2 minutes of wall clock (run 40456494050, head 8cc52ef):

  | Job | Wall | Where the time goes |
  |---|---|---|
  | real ZFS 2.4 VM | 1:51 | setup-go 10s, apt install qemu 13s, boot + tests + skip checks 81s |
  | proto | 1:15 | `buf lint` 53s, mostly compiling buf through `go run` |
  | unit | 1:06 | setup-go 13s, vet 10s, simulator tests 37s |
  | fuzz ×4 | ~0:55 each | a fixed 30s of fuzzing per target |

- A slow suite gets run less, gets sharded by guesswork, and invites `-short` flags and skips. A skip on the real leg means the code was not measured, and the skip budget (`ci/check-skips.sh`) exists to stop that.
- Much of the cost is not testing at all: rebuilding tools, installing packages, booting and provisioning a guest, and tests that really wait (sleeps, lock-wait, drain and backoff timers).
- The design is data-oriented: scan → immutable Table → pure planner → Plan → apply → re-read. Most behaviour can be asserted on the pure planner, rows in and plan out, with no exec and no clock. The simulator and the VM are needed only for what the planner cannot prove: zfs and nfs-utils facts, races and crash convergence.

## Decision

- **Budget:**
  - A PR's CI is green or red within **60 seconds** of wall clock, from push to the last required check.
  - `go test ./...` on a developer machine finishes in **a few seconds**.
  - A change that breaks the budget is a regression, like a failing test.
  - **Operating margin: 45 seconds nominal.** The same commit runs 1.5x slower on some hosted runners than on others: the VM job's test step took 32s, 46s and 49s across runs 40468413047, 40469622947 and 40471254688, and push-to-green ranged from 54s to 78s. So a typical run must finish in 45s to stay under 60s on a slow runner. Work that brings the nominal time above 45s needs a matching saving.
  - The budget is for **warm caches**. A cold provisioned guest costs about 115s, and a cold qemu tree about 13s. `cache-warm.yml` keeps the caches warm where PRs read them (see Mechanisms).
- **What may not be cut to meet it.** The budget is met by removing waiting and duplication, never by removing measurement. Every check that stands today still gates a PR:
  - gofmt, `go mod tidy -diff`, vet, the gen-drift check and buf lint;
  - the real OpenZFS 2.4 kmod and userland assert;
  - the skip budget, the test-list coverage and the Ginkgo spec floor;
  - every spec row (`docs/research`, wave8 catalogue) keeps at least one killing test.
- **Tiers:**
  - **PR (≤60s):**
    - the static checks;
    - pure planner tables;
    - simulator tests;
    - the seed-corpus run of each fuzz target as plain `go test`;
    - the real-ZFS VM suite.
  - **Nightly:**
    - exploratory fuzzing (long runs);
    - the model-based FSM explorer;
    - scale tests;
    - real-pool mutant samples;
    - the full real-pool crash sweep (`ZFS_CRASH_FULL=1`). The PR's real leg runs each crash case uninterrupted plus one crash point, rotated across cases; the simulator leg runs every point on every PR. The full sweep cost about 29s of the VM's two vCPUs (run 40468062545), and it slowed every other shard.

    A nightly failure files an issue (label `needs-triage`) with the reproducer attached. A human then commits the reproducer as a seed-corpus entry (`testdata/fuzz/<Target>/<hash>`), or as a test for the real-pool jobs, together with the fix, so the PR tier keeps it. The workflow never pushes. Both real-pool sweeps run in `nightly.yml`'s `real-zfs-full` job, which sets `ZFSDIFF_FULL=1` and `ZFS_CRASH_FULL=1`.
- **Mechanisms.** Measured in waves 10 and 13 and adopted where noted. The first CI run of each one confirms the estimates (see the wave 13 notes):
  - **No real waiting in tests:** clocks and durations are injected, and `testing/synctest` is used where no exec is involved.
  - **Assert at the lowest level that proves the row:**
    - a behaviour provable on the planner is tested there, as golden plans;
    - the simulator and VM keep only what needs zfs or races;
    - redundant tests become tables.
  - **Parallel on the VM (adopted):**
    - Process shards come first. Each guest runs three concurrent shards, each on its own pool: the endpoint job three endpoint shards, the csi job csi, csi-sanity on filesystems and csi-sanity on zvols (see the two VM jobs below). This needs no test-code change.
    - `t.Parallel` inside a shard is the follow-up. Each test owns its datasets (one child per test).
  - **Time cost as data (adopted):**
    - `ci/real-zfs-weights.txt` is the time cost of each real-pool test, next to `ci/expected-skips.txt`, and it drives the sharding.
    - The guest writes `test-times.tsv` on every run: each test's own time, its time with parallel subtests summed, and its wall. The file goes into the job summary and an artifact.
    - `ci/real-zfs-weights.sh` regenerates the weights from that file, so refreshing them is mechanical.
    - Weights only balance the shards. They never decide what runs.
  - **The VM starts warm (adopted):**
    - **The provisioned guest is cached, not prebaked.** It is Ubuntu's signed cloud image with OpenZFS 2.4 and nfs-utils installed once.
      - It is keyed by the image's sha256, the hash of the provisioning inputs (`ci/real-zfs-cloud-config.yaml`, not the script that runs the tests) and the ISO week. So editing the test runner does not reprovision, and the guest cannot lag zfsutils updates by more than a week.
      - It is stored only after a boot from the stored disk passes the checks.
      - The keys come from one action, `.github/actions/real-zfs-keys`.
    - **Warm boots load the stored kernel directly** (`-kernel`, `-initrd`), with no BIOS and no GRUB. The booted release must match the one stored with the disk.
    - **qemu is unpacked from the runner image's own apt view**, once per runner image version, with apt as the fallback. It is not a custom runner image.
    - **The guest boots while the job sets up Go** and builds the test binaries. The job restores the guest and qemu first, a boot step starts qemu and returns, and the test step waits for the guest.
    - **`cache-warm.yml`** runs hourly and on dispatch on the default branch. It saves the guest, qemu and Go caches where every PR can read them, because a PR's own saves are scoped to that PR. The Go entries are keyed by go.sum and commit and refreshed once per default-branch commit. A PR saves only when nothing for its go.sum was restored.
  - **Tools are fetched, not compiled:**
    - buf is a prebuilt, checksum-pinned binary.
    - Go is the sha256-pinned release tarball. It is downloaded, verified and extracted in the background during the cache restore.
    - The Go build cache stays warm.
  - **No real waiting in the race build:** `GORACE=atexit_sleep_ms=0` drops the race runtime's 1s exit sleep in each test binary. A race still fails the binary.
  - **Few jobs, sized to the critical path:** the static checks share one job, and the real-ZFS VM jobs set the pace.
  - **Two real-ZFS VM jobs, one per suite (adopted, wave 15):** see below.

### Two VM jobs: endpoint and csi on separate runners

- **Data.** With all six shards in one guest on one runner (4 vCPUs), the guest was CPU-bound in kernel work. Run 40502060857 (7f5637f, job 122181926805, the first warm run with the wave 14 levers), over the 31.6s test phase:
  - CPU busy 92%: user 22%, sys+irq 69%, steal 1%, iowait 4%;
  - pressure: cpu some 78%, io some 62%, io full 3%, memory 0%;
  - vmstat: run queue 10–41 and idle 1–2% while five or six shards ran; still about 88% busy with only one or two endpoint shards left.
  - shard walls: endpoint 24.3 / 27.0 / 31.3s, csi-sanity mount 23.1s and block 23.2s, csi other 4.7s. csi-sanity took 13.5–13.9s on the 54s run 40497090324, so it is slowed by the other shards, not by itself.
  - Steal at 1% rules out a contended host, so a bigger guest on the same runner would not help. In-guest tuning was tried: the wave 14 levers were estimated at 45–49s and measured 70s push-to-green.
- **Decision.** The VM job is a matrix over the suites in `ci/real-zfs-suites.txt`: `endpoint` (the three endpoint shards, TestPropertyRoundTrip's rows split across them by `PROPRT_SHARD`) and `csi` (csi tests, csi-sanity on filesystems and on zvols). Each job restores the same cache entry (`zfsvm-v4`, guest and qemu), boots its own VM and builds only its own test binaries (`ZFSVM_SUITE`). Its checks run in that job: `ci/check-skips.sh` for its binaries, and in the endpoint job `ci/check-split.sh`. `fail-fast` is off, so one suite's failure does not cancel the other's measurement. `nightly.yml`'s `real-zfs-full` runs the same matrix. `cache-warm.yml` provisions the one guest both jobs restore, and warms one Go namespace per suite (`real-zfs-endpoint`, `real-zfs-csi`).
- **Completeness is proved, not assumed:**
  - `ci/check-suites.sh` (static job) fails unless every package whose tests read `ZFS_TEST_POOL` is in exactly one suite, and `ci.yml`'s and `nightly.yml`'s `suite` matrices and `cache-warm.yml`'s namespaces name every suite exactly once.
  - In each job, `ci/check-skips.sh` fails unless every test its binaries list passed or skipped.
  - In the endpoint job, `ci/check-split.sh` fails unless every top-level test reported in exactly one shard log (TestPropertyRoundTrip in each) and every TestPropertyRoundTrip row ran in exactly one shard.
  - Together: every listed test runs in exactly one job, and once within it.
- **Prediction (from run 40502060857, a 70s-class runner).** The guest's busy CPU over the phase was 31.6s × 4 × 0.92 = 116 CPU-seconds. Sharing each second's busy CPU equally among the shards running then (0.97 busy with three or more, 0.88 with one or two, as vmstat shows) attributes 81.6 CPU-s to the endpoint shards (19.0 / 23.7 / 39.0) and 37.9 to csi (17.3 / 17.5 / 3.1). The sum, 119.5, is within 3% of the measured 116.
  - **endpoint job:** the same model with only the three endpoint shards gives a 21.7s phase. Everything outside the phase took 69 − 31.7 = 37.3s (queue, restore 9.9s, boot to ssh 13.7s, checks, upload) and does not change. Predicted job: 37.3 + 21.7 = **59s**, from 69s.
  - **csi job:** CPU alone allows a 10.5s phase; the measured contended wall, 23.1s, is the upper bound. Predicted job: **48–60s**.
  - Push-to-green is the slower job plus about 1s: about **60s on a 70s-class runner**. Scaled by the phase ratio to a 54s-class runner (40497090324: 21.8s phase), the endpoint job is about 47s. The split alone does not reach the 45s operating margin on a slow runner; the fixed 37s is now the larger share.
- **Cost.** Two VMs boot instead of one. Runner time per push goes from 66s (one job, start to end, without the queue) to about 2 × 34.3s fixed (66 − 31.7) + 21.7s + 10.5–23.1s = 101–113s, so **+35 to +47 runner-seconds (+53% to +72%)**. Billed per job rounded up to the minute, that is 2 minutes before and 2–4 after. Each job also downloads the 899 MB guest entry, so cache egress doubles. A cold guest (no entry for the current key) is provisioned by both jobs, about 115s each; the first save wins and the other only warns. `nightly.yml` gains one VM job a night.
- **Next lever: the boot.** The guest's boot is on the critical path: in run 40502060857 the test binaries were built at +8.9s after qemu started and ssh came up at +13.7s, so the job waited 4.8s for the guest. `systemd-analyze` in the same log: `Startup finished in 1.672s (kernel) + 5.521s (initrd) + 5.198s (userspace) = 12.391s`. The directly booted initrd is 67,176 KiB. The initrd stage, 5.5s, is the largest single part of the boot.
- **The first runs must confirm:** both jobs pass their checks, with `check-split: … top-level tests over 3 shard logs, each in exactly one`; the endpoint job's `guest load` line shows the phase near 22s; the csi job's phase is under the endpoint job's.

## Consequences

- Test code is held to the same data-oriented shape as the product: rows and plans as data, with exec only at the edges.
- A cached guest is one more artifact to keep honest. The version assert runs inside every boot, so a guest that has left 2.4.x fails loudly rather than testing the wrong ZFS. A guest that is stale but still on 2.4.x passes by design. That staleness is bounded by the ISO week in the cache key, and the job summary reports it (image serial, kernel and `zfs version`).
- Long fuzzing no longer gates a PR. Its findings arrive by the next morning as issues. Once a human commits them as corpus entries, they gate every PR.
- Tests that need wall-clock behaviour (real lock contention, nfsd start-up) should declare their time cost as data, next to `ci/expected-skips.txt`, so the budget is visible per test.
- Note (zfsdiff replay): the real-pool replay of the whole zfs(8) corpus (102 sequences, ~1,270 zfs invocations, estimated 15-30s) would at least double the slowest VM shard, so it runs nightly (`real-zfs-full`). The PR tier keeps it as measurement, not a skip: model vs simulator over the whole corpus with no pool, and a real-pool replay of `zfsdiff.PRSample` (21 sequences, 253 invocations, failing past 15s). On the real pool it measured 9.9s and 9.0s (runs 40469622947 and 40471254688) against an estimated 5s, and it is weighed at 9.5s now. A runner 1.5x slower brings it close to the 15s limit; if it nears that limit, the sample shrinks (it stays above zero sequences on the real pool) rather than another check being cut.
- If the budget cannot be met without cutting a check listed above, the check stays and this ADR is revisited. The budget does not outrank validity.
