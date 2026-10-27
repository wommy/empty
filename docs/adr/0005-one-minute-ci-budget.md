# 5. Tests and CI finish in under one minute

Status: accepted (budget); proposed (mechanisms, pending measurement) · Date: 2026-10-27 · Related: ADR 0004 (the 2.4 VM is the only real-ZFS gate).

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
    - real-pool mutant samples.

    A nightly failure files an issue, and the reproducer goes into the seed corpus so the PR tier keeps it.
- **Mechanisms.** These are proposed, measured in wave 10 and adopted when they show their savings:
  - **No real waiting in tests:** clocks and durations are injected, and `testing/synctest` is used where no exec is involved.
  - **Assert at the lowest level that proves the row:**
    - a behaviour provable on the planner is tested there, as golden plans;
    - the simulator and VM keep only what needs zfs or races;
    - redundant tests become tables.
  - **`t.Parallel` by default:** each test owns its datasets (one child per test), so the VM tests run in parallel too.
  - **The VM starts warm:**
    - a prebaked guest image with OpenZFS 2.4 and nfs-utils, keyed by a hash of its build script and rebuilt when that hash changes;
    - cached qemu;
    - the Go build overlaps with the boot.
  - **Tools are fetched, not compiled:** prebuilt, checksum-pinned buf, and a warm Go build cache.
  - **Few jobs, sized to the critical path:** the static checks share one job, and the real-ZFS VM job sets the pace.

## Consequences

- Test code is held to the same data-oriented shape as the product: rows and plans as data, with exec only at the edges.
- A prebaked image is one more artifact to keep honest. The version assert runs inside every boot, so a stale image fails loudly rather than testing the wrong ZFS.
- Long fuzzing no longer gates a PR. Its findings arrive by the next morning and then gate every PR as corpus entries.
- Tests that need wall-clock behaviour (real lock contention, nfsd start-up) should declare their time cost as data, next to `ci/expected-skips.txt`, so the budget is visible per test.
- Note (zfsdiff replay): the real-pool replay of the whole zfs(8) corpus (102 sequences, ~1,270 zfs invocations, estimated 15-30s) would at least double the slowest VM shard, so it runs nightly (`real-zfs-full`). The PR tier keeps it as measurement, not a skip: model vs simulator over the whole corpus with no pool, and a real-pool replay of `zfsdiff.PRSample` (21 sequences, 253 invocations, weighed at 5s, failing past 15s).
- If the budget cannot be met without cutting a check listed above, the check stays and this ADR is revisited. The budget does not outrank validity.
