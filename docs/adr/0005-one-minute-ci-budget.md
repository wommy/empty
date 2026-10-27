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
    - Process shards come first. The guest runs six concurrent shards, each on its own pool: three endpoint shards, csi, csi-sanity on filesystems and csi-sanity on zvols. This needs no test-code change.
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
  - **Few jobs, sized to the critical path:** the static checks share one job, and the real-ZFS VM job sets the pace.

## Consequences

- Test code is held to the same data-oriented shape as the product: rows and plans as data, with exec only at the edges.
- A cached guest is one more artifact to keep honest. The version assert runs inside every boot, so a guest that has left 2.4.x fails loudly rather than testing the wrong ZFS. A guest that is stale but still on 2.4.x passes by design. That staleness is bounded by the ISO week in the cache key, and the job summary reports it (image serial, kernel and `zfs version`).
- Long fuzzing no longer gates a PR. Its findings arrive by the next morning as issues. Once a human commits them as corpus entries, they gate every PR.
- Tests that need wall-clock behaviour (real lock contention, nfsd start-up) should declare their time cost as data, next to `ci/expected-skips.txt`, so the budget is visible per test.
- Note (zfsdiff replay): the real-pool replay of the whole zfs(8) corpus (102 sequences, ~1,270 zfs invocations, estimated 15-30s) would at least double the slowest VM shard, so it runs nightly (`real-zfs-full`). The PR tier keeps it as measurement, not a skip: model vs simulator over the whole corpus with no pool, and a real-pool replay of `zfsdiff.PRSample` (21 sequences, 253 invocations, failing past 15s). On the real pool it measured 9.9s and 9.0s (runs 40469622947 and 40471254688) against an estimated 5s, and it is weighed at 9.5s now. A runner 1.5x slower brings it close to the 15s limit; if it nears that limit, the sample shrinks (it stays above zero sequences on the real pool) rather than another check being cut.
- If the budget cannot be met without cutting a check listed above, the check stays and this ADR is revisited. The budget does not outrank validity.
