# Where the time goes: tests and CI (budget: ADR 0005, 60 s per PR)

Measured on 8cc52ef (the commit of run 40456494050), Go 1.27.3. Nothing was changed, committed or pushed.
All files are in `/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave10/`.

## Read this first: what is trustworthy

- **CI numbers are ground truth.** They come from the job logs and step timestamps of run 40456494050.
- **Local wall-clock numbers are inflated.** The box has 4 CPUs and a load average of 67 to 84 for the whole session (sibling agents). Local wall times for CPU-bound work are noisy, 2x to 5x too high, and not comparable to the CI runner. I therefore report (a) CPU seconds (user+sys), which load does not change, (b) the minimum and median over several runs, and (c) wall time only for sleeps, which load does not change.
- **The VM boot window is opaque.** The script prints nothing between "Image resized" (05:39:28) and the first guest output (05:40:15). Boot, guest apt, the host-side test compile and the 5 s poll quantization cannot be separated from the log. The split below is bounded, not measured.

## 0. Critical path today (run 40456494050)

| Job | Wall | vs 60 s |
|---|---|---|
| real ZFS VM | 1:51 (05:38:50 to 05:40:41) | +51 s |
| proto | 1:15 | +15 s |
| unit | 1:06 | +6 s |
| fuzz x4 | 0:54 to 1:04 | -6 to +4 s |

Every job is at or over the budget. The VM job sets the pace, but proto, unit and fuzz each need cutting too, so cutting only the VM job is not enough.

## 1. Per-test wall time, locally

Files:
- `plain-per-test.csv`, `plain-per-package.csv`: `go test -json -count=1 ./...`, min/median/max over 4 warm runs.
- `race-per-test.csv`, `race-per-package.csv`: the same with `-race`, 3 warm runs.
- Raw: `plain-*.json`, `race-*.json`, `*-tests.csv`, `*-packages.csv`, `*.analysis.txt`.

Whole-suite cost:

| Run | Wall | CPU (user+sys) |
|---|---|---|
| cold cache, no race (166 s) | 166.3 s | not recorded |
| cold cache, `-race` (338 s) | 338.4 s | not recorded |
| warm, no race | 8.8 s once, then 22 to 54 s under load | 9.5 s |
| warm, `-race` | 38.3 s once, 33 to 107 s under load | 20.4 s |
| warm, `-race`, `GORACE=atexit_sleep_ms=0` | 32.1 s (vs 37.2 s same session) | 19.7 s |

The CPU split with this module's packages changed and dependencies cached (`cpu.txt`, CPU seconds):

| Phase | no race | `-race` |
|---|---|---|
| compile of this module's changed packages | 7.6 | 10.0 |
| link of the 9 test binaries (`go test` relinks on every run) | 6.6 | 8.9 |
| running the tests | 2.7 | 11.5 |
| `go vet ./...` (plain cache, own packages changed) | 32.9 | |

Compile and link are about 70% of a warm plain run and about 60% of a warm race run. The tests themselves are small.

Per-package top-level test time, minimum over runs. The plain column is dominated by sleeps. The race column is dominated by CPU, so it is inflated by load:

| Package | Top-level tests | plain, min sum (s) | race, min sum (s) | CI `-race` package time (s) |
|---|---|---|---|---|
| internal/server | 9 | 3.63 | 16.58 | 6.04 |
| internal/endpoint | 111 | 1.64 | 6.79 | 2.77 |
| cmd/zfs-endpoint | 6 | 1.03 | 1.08 | 2.05 |
| internal/zfs | 16 | 0.40 | 0.41 | 1.37 |
| internal/csi | 6 | 0.28 | 1.03 | 1.45 |
| internal/lock | 5 | 0.04 | 0.07 | 1.05 |
| internal/unixsock | 6 | 0.01 | 0.00 | 1.01 |
| cmd/zfs-csi | 1 | 0.00 | 0.00 | 1.02 |

160 top-level tests and 124 subtests.

Slowest tests (plain min / CI-relevant note):

| Test | Plain (s) | Note |
|---|---|---|
| internal/server TestShutdownWaitsForCancelledHandlers | 2.36 | Sleep, 2.36 to 2.40 s in all 7 runs |
| internal/endpoint TestSimulateDestroyAllEquivalent | 1.37 | CPU: 20000 seeds |
| internal/server TestMessageSizes | 1.06 | CPU: ~2005 simulator creates and a 100k-id request |
| cmd/zfs-endpoint TestStartupWaitsForTheParent | 1.00 | Sleep: 1 s retry backoff |
| internal/zfs TestKilledCommandIsInterrupted | 0.25 | 50 ms timeout plus 200 ms WaitDelay |
| internal/csi TestSanity | 0.25 | Ginkgo on the simulator |
| every other test | at most 0.15 | |

Under `-race` on this loaded box, TestMessageSizes is 13.7 s and TestSimulateDestroyAllEquivalent 5.1 s. Those are CPU seconds multiplied by load, not CI numbers. On CI the whole `internal/server` package is 6.04 s including the 2.4 s sleep and about 1 s of race exit sleep, so TestMessageSizes is about 2 s there (inferred).

### `t.Parallel`

- **Used: 0 of 160 top-level tests.** There are no `pause` events in any `-json` run, and `grep -r 't.Parallel()'` finds nothing.
- **Possible:**
  - 159 of 160 tests have no `t.Setenv`, `Chdir` or `Setenv`. Only TestServeHealthNotifyAndStop uses `t.Setenv` (`main_test.go:143`), which forbids `t.Parallel`.
  - In simulator mode each test builds its own `zfstest.New("tank")`, so they are parallel-safe by construction.
  - In real-pool mode (`ZFS_TEST_POOL`, `endpoint_test.go:66`, `harness_test.go:30`) all tests share one pool. Parallelism there needs one child dataset per test (ADR 0005). I did not verify that each test already stays inside its own dataset.
- **Effect:**
  - Locally the suite is sleep- and CPU-bound, so `t.Parallel` helps little here: the two sleepers are 3.4 s and run in different packages that already run in parallel.
  - On the VM it is the main lever, because the 111 endpoint tests run one after another (sum of top-level times 9.22 s equals the leg's wall time).

## 2. Per-test time on the real VM

File: `vm-per-test.csv` (208 rows: leg, test, level, status, seconds, log timestamp). Source: `vm-job.log` (job log, `-test.v` text, not test2json).

| Leg | Top-level tests | Sum of top-level time | Skips | Note |
|---|---|---|---|---|
| endpoint.test | 111 | 9.22 s (all sequential) | 16 (1.39 s) | 20 skips counting subtests, 17 allowed |
| csi.test, mount | 6 | 5.44 s | 0 | TestSanity 4.56 s |
| csi.test, block | 1 (TestSanity) | 4.56 s | 0 | |

Slowest on the VM: TestSanity 4.56 s (twice), TestCreateVolumeProperties 1.27, TestReconcileAgainstDesiredSet 0.68, TestReserveRestoreIsIdempotent 0.57, TestCloneSourcesMustBeOwned 0.46, TestSnapshotLifecycleWithClones 0.43, TestCloneLargerThanLimitIsOutOfRange 0.39, TestReconcileCollectsLeakedCloneSource 0.36, TestExpandNodeExpansionRequired 0.33. 46 endpoint rows are at or above 0.05 s. The other 73 top-level endpoint tests sum to 0.19 s.

The simulator-only tests (17 allowed skips) cost 0.04 to 0.25 s each even though they skip, because setup runs first. TestConcurrentCreatesSerialize 0.25 s, TestBusyIsMappedByOperation 0.24 s.

## 3. VM breakdown

File: `timing-vm-timeline.csv`. Everything from `ci/real-zfs-vm.sh` and the log timestamps.

| Phase | Seconds | Note |
|---|---|---|
| job set-up and checkout | 3 | |
| setup-go | 10 | toolchain download 0.9, extract 1.7, copy to tool cache 5.3, build-cache restore (91 MB, key HIT) 1.7 |
| enable kvm | 0.1 | |
| install qemu | 12.1 | `apt-get update` 5.7 plus install 6.4 |
| SHA256SUMS download and gpgv | 0.8 | |
| cloud image download and sha256 | 6.2 | downloaded every run, no cache |
| `qemu-img resize 20G` | 5.1 | |
| **boot window** | **47.2** | see below |
| guest checks, scp of 37 MB of binaries, `zpool create`, test lists | 2.4 | |
| endpoint.test | 9.2 | |
| csi.test mount | 5.5 | |
| csi.test block | 4.6 | |
| `zpool destroy`, log scp, 3x `check-skips.sh` | 0.8 | skip checks are about 0.2 s each |
| shutdown (trap kills qemu) | under 0.1 | |
| post steps | 3 | |

- **Image:** downloaded and verified every run (6.2 s), then resized (5.1 s). No cache.
- **zfs and nfs are not prebaked.** `user-data` has `package_update: true` and `packages: [zfsutils-linux, nfs-kernel-server]`, so the guest runs apt on every boot (`real-zfs-vm.sh:49-50`).
- **Test compile overlaps with boot.** The two `CGO_ENABLED=0 go test -c` run after `qemu` starts and before the ssh poll (`:81-83`), serially. Locally with dependencies cached and this module changed: endpoint 7.0 s, csi 6.2 s (13.2 s, load-inflated wall). Binaries are 8.4 MB and 29 MB. This cost is hidden behind boot today and becomes critical once the guest is prebaked.
- **The 47.2 s window** contains, in order: keygen, `cloud-localds`, qemu start, 2x `go test -c` (concurrent with the guest boot), then the ssh poll (5 s steps), then the `boot-finished` poll (5 s steps), and the guest's own apt install. The two `sleep 5` loops (`:86`, `:93`) alone cost 2.5 s each on average. Bound: guest-ready is at most 47 s after resize and at least the compile time (6 to 13 s).
- **Not measured:** the split inside the 47 s. Cheapest way to get it next time: timestamp markers in the script, and keep `console.log` on success. It is only uploaded on failure today.
- **Cache:** the build cache for this job restored (key `go.sum` plus `real-zfs-vm.sh`, 91 MB HIT) and is not re-saved on a hit.

## 4. Every source of real waiting

File: `timing-waits.csv` (file:line, kind, configured, measured cost, where hit).

Waits that cost time in the PR path:

| Where | Cost (s) | Note |
|---|---|---|
| `internal/server/daemon_test.go:48` (set at `:155`, first half `:142`) | 2.2 to 2.4 | TestShutdownWaitsForCancelledHandlers. `Shutdown` itself returns after 200 ms. The test's `t.Cleanup` (`d.GRPC.Stop`) waits for the stuck handler's 2 s sleep. |
| `cmd/zfs-endpoint/startup.go:114`, sleep at `:137` | 1.00 | TestStartupWaitsForTheParent. The parent appears at 300 ms (`main_test.go:79`), but the first retry is at 1 s. |
| race runtime `atexit_sleep_ms` (default 1000) | about 1.0 per binary x 9 | CI shows zfs-csi 1.02 s, lock 1.05 s, unixsock 1.01 s with under 0.1 s of tests. Local `GORACE=atexit_sleep_ms=0`: whole race run 37.2 s to 32.1 s wall. |
| `ci/real-zfs-vm.sh:86`, `:93` | 2.5 expected each | `sleep 5` poll loops |
| `ci/real-zfs-vm.sh:131-137` | 9.2 + 5.5 + 4.6 | three legs sequential |
| `ci.yml:69` | 31.2 + 12.4 build | fuzz, fixed by flag, 4 jobs |
| `internal/zfs/zfs_test.go:145-153` | 0.25 to 0.29 | 50 ms timeout plus 200 ms `WaitDelay` |
| `internal/server/daemon_test.go:91`, `:115` | 0.10, 0.05 | fixed sleeps |
| `internal/endpoint/adv4_test.go:99` | 0.05 | fixed sleep |
| `internal/lock/lock_test.go:88` | 0.01 | |

Defaults that tests do not hit (cost 0):
- drain 30 s (`main.go:65`, zfs-csi `main.go:39`), stop-wait 10 s (`main.go:66`).
- `Shutdown` idle path 1 s (`daemon.go:128`).
- `LockWait` default 30 s (`endpoint.go:173`). Tests set 200 ms (`adv4_test.go:42`, `reconcile_contract_test.go:207`) or 1 s (`endpoint_test.go:84` and others), and no endpoint test is over 0.15 s locally, so no test sits out a lock timeout. TestAdv4OneBusyLockAbortsWholeRun asserts it does not wait.
- `lockMargin` 1 s (`endpoint.go:289`), `zfs/runner.go:86` `WaitDelay` 5 s, the 5 s and 10 s failure bounds in `lock_test.go` and `main_test.go`.

Polling and backoff:
- 1 ms poll loops (`lock_test.go:56`, `daemon_test.go:111/146/159/174`) and a 10 ms socket poll (`main_test.go:98`).
- The only real backoff is `startup.go:114` above.

## 5. Build and compile cost

- **go test compile versus run.** CPU table in section 1. `-json` Action timings only cover run time. The build is not reported separately. Locally: run time is 2.7 CPU-s (plain) and 11.5 CPU-s (race) against 14.2 and 18.9 CPU-s of compile plus link.
- **Race build cost.**
  - Local cold: 338 s versus 166 s for plain, so about 2x.
  - Local warm: 20.4 CPU-s versus 9.5.
  - CI unit job: the first package result arrives 24.3 s after the test step starts, the last 36.3 s after. So about 24 s is build and link of 9 race binaries and 12 s is running.
- **Cache hit or miss in CI.**

| Job | Cache | Result |
|---|---|---|
| unit | `go.sum` key, 465 MB | HIT, restore 5.7 s (download 1.8, untar 3.6). Not saved after a hit, so the cache is never refreshed when only code changes. This module's packages are rebuilt on every run. |
| real-zfs | `go.sum` + `real-zfs-vm.sh` key, 91 MB | HIT, restore 1.7 s, not saved |
| proto | `cache: false` | no GOCACHE, no module cache: buf's ~100 modules downloaded and compiled every run |
| fuzz x4 | `cache: false` | 12.4 s build before the baseline coverage line |
| Go toolchain, all jobs | runner tool cache | MISS: 1.27.3 is downloaded, extracted and copied in every job, 7.4 s (5.1 to 5.3 s is the copy) |

- **buf lint.** `go run github.com/bufbuild/buf/cmd/buf@v1.47.2 lint` took 53 s in CI.
  - Log timestamps: module downloads 6.3 s (05:39:02.5 to 05:39:08.8), then about 46 s of compile and link with no output, then lint.
  - Local measurements (`buf.txt`, the cold wall of 831 s is load, use CPU): cold `go run` is 242 s user plus 43 s sys. A warm second `go run` is 1.8 CPU-s and 6.5 s wall. A prebuilt binary `buf lint` is 1.3 s wall and 0.5 CPU-s. Linking alone is 5.1 CPU-s.
  - **About 98% of the 53 s is fetching and compiling buf. Linting is about 1.3 s.**
  - `generate` (7 s) runs buf again from the Go cache (no recompile) and builds the two protoc plugins.

## 6. Top 15 time sinks (vs the 60 s budget)

File: `timing-sinks.csv`. All savings are estimates in seconds of PR wall clock, and none removes a check that ADR 0005 lists as mandatory.

| # | Sink | Cost (s) | Est. saved (s) | Job | Confidence |
|---|---|---|---|---|---|
| 1 | buf built from source on every run (`ci.yml:81`, `:91`) | 53 | about 50 | proto | high |
| 2 | Guest installs zfs and nfs at boot on a stock image (`real-zfs-vm.sh:49-50`) | 47.2 window | about 30 to 35 with a prebaked image | real-zfs | medium |
| 3 | 30 s fuzz x4 in the PR tier (`ci.yml:69`) | 54 to 64 per job | about 42 to 50 per job (seed corpus stays in unit) | fuzz | high |
| 4 | VM test legs sequential, 0/160 `t.Parallel` | 19.3 | about 9 to 10, up to 14 with parallel tests | real-zfs | medium |
| 5 | Go toolchain download and copy in every job | 7.4 per job | about 7 per job | all | high |
| 6 | qemu `apt-get update` plus install | 12.1 | about 8 to 10 | real-zfs | medium |
| 7 | Image download, verify and resize every run | 12.1 | about 7 to 12 | real-zfs | medium |
| 8 | vet and race test serial; race build+link of 9 binaries | 10.3 + 36.3 | about 10 (vet in parallel) | unit | high |
| 9 | `sleep 5` poll loops (`real-zfs-vm.sh:86`, `:93`) | 5 expected | about 4.5 | real-zfs | high |
| 10 | Race exit sleep in 9 binaries | 1.0 each | 1 to 5 | unit | high |
| 11 | TestShutdownWaitsForCancelledHandlers (`daemon_test.go:48`, `:155`) | 2.4 | about 2.2 | unit | high |
| 12 | TestMessageSizes (`daemon_test.go:255`) | about 2 on CI (inferred) | about 1.5 to 2 | unit | medium |
| 13 | TestSimulateDestroyAllEquivalent, 20000 seeds (`simulate_test.go:79`) | about 1 to 1.5 on CI (inferred) | about 1 to 1.3 | unit | medium |
| 14 | Startup retry backoff 1 s (`startup.go:114`) | 1.0 | about 0.7 to 1.0 | unit | high |
| 15 | gofmt and `go mod tidy -diff` serial before vet | 2.2 | about 2 | unit | high |

Where the budget lands if items 1 to 11 are done (estimate, not measured):
- VM job: 111 s minus about 70 s is roughly 40 to 50 s. After a prebaked guest, the host-side `go test -c` of both binaries (13 s locally) comes onto the critical path and must be cached or started earlier.
- Unit: 66 s minus about 25 s is roughly 40 s.
- Proto: about 20 s.
- Fuzz: out of the PR tier.

So the budget is reachable without cutting a check. The single largest item is the VM boot window, and it is the least well measured.

## Other files

`vm-job.log` (full real-zfs job log), `vm-job-raw.txt`, `vmparse.py`, `analyze.py`, `agg.py`, `unjson.py` (parsers), `run1.sh`, `run2.sh`, `seq.sh`, `seq2.sh`, `cisim.sh`, `cpu.sh`, `buf.sh`, `atexit.sh` (measurement scripts), `walls.txt`, `walls2.txt`, `cisim.txt`, `cpu.txt`, `buf.txt`, `atexit.txt` (raw results), `timing-ci-steps.csv`, `timing-vm-timeline.csv`, `timing-waits.csv`, `timing-sinks.csv`.
`cisim.txt` has vet and compile numbers taken against the wrong cache state (a race-only cache), so its vet figure (253 s) is not a CI-like number. Use `cpu.txt` for those.
