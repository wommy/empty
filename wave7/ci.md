# CI pipeline as data: critical path, reliability, validity, and a faster design

Scope: `.github/workflows/ci.yml`, `ci/real-zfs-vm.sh`, `ci/check-skips.sh`, `ci/expected-skips.txt`
on wommy/empty, PR #1 (branch `claude/gallant-johnson-1qaxp5`), runs #3 to #22 (2026-10-27 03:55 to 05:02 UTC).

Data files (all in this directory):

- `timings.csv`: 557 rows, one per step: run, job, queue time, job wall, step start, step seconds, offset in job. Built by `scripts/collect.sh` (gh api) and `scripts/tocsv.py` from `raw/`.
- `vm_phases.csv`: sub-step phases of the VM test step, taken from log timestamps for runs 19, 21 and 22, plus the apt download rates.
- `logs/`: the job logs I parsed (runs 14, 19, 21, 22).

The workflow changed shape during the window. Runs 3 to 7 had `unit` plus a host `real-zfs` (noble). Runs 8 to 17 had `unit` (including proto), `real-zfs` (the VM) and `real-zfs-noble`. Runs 18 to 22 match the current file: `unit`, `proto`, and `real ZFS 2.4 (Ubuntu 26.10 VM)`. Only runs 10 onward boot the VM all the way.

## 0. Headline numbers

| | value |
|---|---|
| Push-to-green wall, successful runs 10 to 22 | 113, 118, 128, 130, 139, 142, 325 s. **Median 130 s**, worst 325 s |
| All completed runs 10 to 22 | median 136 s; tail 325 s (run 19) and 719 s (run 14) |
| What causes the tail | the azure apt mirror inside `install qemu`: 212 s (19.0 MB at 96 kB/s) and 605 s (46.7 MB at 79 kB/s). Normal is 12 to 26 s |
| Critical path | **two jobs of almost the same length.** `unit` is 111 to 131 s and the VM job is 102 to 139 s. Speeding up only the VM job buys at most about 10 s |
| Go build cache | **broken.** It never restores cleanly (see 2.7) |
| Failures in the window | 10 failed or failure-carrying runs: **8 CI bugs, 1 real product bug, 0 flakes** (plus 2 slow-mirror incidents that stayed green) |
| Proposed | **about 60 to 70 s median, about 80 s p90** (section 5) |

## 1. Critical-path timeline (from the data)

### 1.1 Real-ZFS VM job (run 22, green, 139 s; run 19 for the slow-mirror case)

| t (s) | step / phase | s | kind | notes |
|---:|---|---:|---|---|
| 0 | queue | 3 | | 2 to 4 s in every run (21 s once, in run 21) |
| 3 | set up job + checkout | 2 | net | |
| 5 | setup-go | 20 | net | downloads Go 1.27.0, then GOTOOLCHAIN=auto fetches go1.27.3, then tries to restore a 488 MB cache, and tar fails (2.7). 8 to 21 s over 20 runs |
| 25 | enable kvm | 0 | det | |
| 25 | install qemu (apt) | 13 | **net** | median 21 s; 212 s in run 19 and 605 s in run 14 |
| 38 | SHA256SUMS + gpgv | 0.7 | net | |
| 39 | image download (877 MB) + sha256 | 5.5 | net | 5.5 to 7.6 s, about 160 MB/s |
| 44 | qemu-img resize + seed | 1.8 | det | 1.8 to 5.1 s |
| 46 | **boot + cloud-init apt install of zfsutils-linux and nfs-kernel-server** | **61** | **net (guest)** | 49 to 61 s. Guest uptime when cloud-init finishes is 36 to 48 s. The rest is BIOS, kernel and the 5 s ssh poll step. The two `go test -c` builds run here and are hidden |
| 107 | zfs version + NFS prechecks | 2 | det | |
| 109 | scp + zpool create + endpoint tests | 11.5 | det | endpoint tests about 10 s |
| 121 | csi-sanity mount (44 specs) | 6.6 | det | |
| 127 | csi-sanity block (44 specs) | 6.0 | det | |
| 133 | check-skips | 0.9 | det | |
| 134 | post setup-go (cache save attempt) | 5 | net | always fails with "another job may be creating this cache" |
| 139 | end | | | |

Where the minutes go in a normal run: setup-go 20 s (14 %), apt 13 to 26 s (10 to 18 %), **guest boot and in-guest apt 49 to 61 s (40 to 45 %)**, the tests themselves 23 s (17 %), image 6 s (4 %). In the slow-mirror runs, apt alone is 65 % (run 19) and 85 % (run 14) of the job.

### 1.2 unit (run 22, 131 s; medians over runs 18 to 22)

| step | run 22 s | range 18 to 22 | notes |
|---|---:|---|---|
| setup-go | 20 | 12 to 21 | same double toolchain and failed restore |
| gofmt | 0 | 0 | |
| go mod tidy -diff | 3 | 2 to 3 | also downloads all modules (cold module cache) |
| vet | 13 | 10 to 22 | cold build cache |
| test -race | 39 | 30 to 39 | cold compile under -race, then about 1 s per package |
| fuzz smoke | 44 | 40 to 44 | 20 s + 10 s fuzztime, plus two instrumented builds |
| post setup-go | 6 | 2 to 6 | failed save |

### 1.3 proto (run 22, 30 s)

setup-go 20 s, buf lint 1 s, generate 0 s. These steps are warm **only by accident**: proto in run 18 was the first job to save key `setup-go-Linux-x64-ubuntu24-go-1.27.3-ccadfd6a…` (488 MB of buf's dependencies), so every job since restores buf's module tree. That cold run 18 took buf lint 58 s and generate 8 s, which is what proto costs whenever go.sum changes.

## 2. Speed design: each lever measured or estimated

Notation: **CP** is the saving on the critical path for a median run; **E[tail]** is the expected saving per run from removing tail events.

### 2.1 Cache the verified cloud image (actions/cache keyed on the SHA256 from SHA256SUMS)

- Measured: the download is 5.5 to 7.6 s for 877 MB. A cache restore of 877 MB runs at a similar 100 to 200 MB/s, so it takes about 5 to 8 s.
- **CP: about 0 to 2 s.** Its worth is reliability, not speed: it takes cloud-images.ubuntu.com off the PR path except for the 1 kB SHA256SUMS and its gpg signature, which still have to be fetched to know the key. That keeps validity: the image is still verified against a signed list.
- Correctness detail: `current/` is a moving symlink. If SHA256SUMS and the .img are fetched across a publish, the sha256 check fails. Resolve the serial (`stonking/current` points to `stonking/20261025/`) and download from the serial directory.
- Verdict: do it, but only as part of 2.2. On its own it saves no time.

### 2.2 Pre-baked guest image (zfsutils-linux and nfs-kernel-server preinstalled)

- Measured: boot plus cloud-init is 49 to 61 s, of which in-guest apt is about 25 to 35 s (cloud-init's packages module, then module load and nfs start). A cloud image without packages to install reaches ssh in about 10 to 15 s under KVM, and the existing 5 s poll adds up to 5 s more. **Estimated CP: about 40 s.**
- Approach: the first-boot snapshot, not virt-customize. virt-customize needs `libguestfs-tools` on the host, an apt install of about 100 MB from the same unreliable mirror, which is the slowest thing we have. Instead, on a cache miss: boot the verified base image with the current cloud-init, wait for boot-finished, run `sudo cloud-init clean --logs --machine-id && sudo apt-get clean`, power off, `qemu-img convert -O qcow2 -c`, then save (about 1 GB). On a hit: restore, create a qcow2 overlay (`qemu-img create -f qcow2 -b baked.qcow2 -F qcow2 run.qcow2`) and boot with a seed that only sets the ssh key.
- **Validity: does baking lag the archive?** Only if the key ignores the archive. Ubuntu ships the ZFS kmod prebuilt in `linux-modules-<kver>` inside the base image, so the kmod version is already pinned by the base image's sha256. What baking could freeze is the userland (`zfsutils-linux`, `libzfs*`) and `nfs-kernel-server`. **Key the bake on what the archive offers now**: `bake-<base sha256>-<sha256 of the Version lines for source packages zfs-linux and nfs-utils in stonking{,-updates,-security}/main Packages.xz>`. Measured: `dists/stonking/main/binary-amd64/Packages.xz` is 0.99 MB (about 1 s), and today it gives zfsutils-linux 2.4.4-1ubuntu4 and nfs-kernel-server 1:2.9.1-1ubuntu1, the same as the guest installed. A new upload changes the key, and the next run rebakes once (about +60 s). The lag is therefore zero, not up to a day.
- If Packages.xz cannot be fetched: use the newest bake with that base sha (restore-keys), add a `::warning::archive freshness unverified`, and record it in the evidence row. The nightly job fails hard on the same condition.
- **Nightly rebake anyway**, from scratch with no cache (2.10 and 4.2). It catches drift the key cannot see, such as a dependency outside the two source packages or cloud-init behaviour, and keeps the cold path from rotting.
- Alternative key, "serial plus package versions after first boot": equivalent, but it can only be computed after an unbaked boot, which defeats the point. Use the archive-index key.

### 2.3 Cache the qemu .debs on the host

- Measured: the download is 19.0 MB in 3 s normally. The whole step is 12 to 26 s (apt-get update 5 s, about 2 s to fetch the debs, unpack and triggers 6 s). The tail is 212 s and 605 s, in 2 of 15 VM runs.
- Cache `/var/cache/apt/archives/*.deb` for the 18 packages, keyed on `ImageOS-ImageVersion` (the runner image, e.g. `ubuntu24/20261012.334`) plus the hash of the package list. On a hit, skip `apt-get update` and run `sudo dpkg -i *.deb` (a closed set, verified once by apt when the cache was made). About 4 to 6 s.
- **CP: about 10 s median; E[tail] about (212 + 605 - 2 × 20) / 15 ≈ 52 s per run; p90 drops by 3 to 10 min.** This is the best expected value per unit of risk in the whole design.
- Risk: a new runner image moves the base libraries, so the key includes the image version and the first run after an image rollout refetches. Validity: qemu is the harness, not the thing under test.

### 2.4 Boot the guest in parallel with the Go compile (partly done)

- Today the boot already overlaps `go test -c`, but **setup-go (20 s) and apt (13 s) run before the boot**, and the image is only fetched inside the script.
- Measured, cold compile of the two static test binaries: 146 s and 138 s in this sandbox, which runs about 13x slower than the runner (`go vet ./...` cold: 189 s here against a median of 13 s on the runner). That puts the cold build on the runner at **20 to 30 s, and warm at under 1 s** (1.2 s here). With an unbaked guest (about 55 s), the build is hidden. **With a baked guest (about 15 s), a cold build becomes the critical path**, so 2.7 is a prerequisite of 2.2.
- Restructure: split the script into `boot` and `test` sub-commands as separate workflow steps. The order becomes debs, image restore, boot (backgrounded), setup-go, build, test. That also turns each phase into its own timed step in the API, so this CSV gets them without log parsing. **CP: about 10 s** (setup-go is hidden behind the boot).

### 2.5 Run the endpoint and csi binaries in parallel in the guest, on separate pools

- Measured, serial: endpoint 10 to 11.5 s, csi mount 5.0 to 6.6 s, csi block 5.3 to 6.0 s, about 23 s in all. In parallel: about the longest leg plus contention, **about 11 to 12 s. CP: about 11 s.**
- Requirements: one pool per leg (`zfsep-ep`, `zfsep-csim`, `zfsep-csib`, each on its own 4G sparse vdev), `-smp 4 -m 6144` (the runner has 4 vCPU and 16 GB; today's guest has 2 vCPU), and per-leg logs (already per-leg).
- Risk, medium: the kernel NFS export table and `/etc/exports.d` are global. TestFileExport* verifies its own entries in etab, and csi-sanity does not export, so the overlap is benign today, but a future csi export test would race. Mitigation: put the export tests in the endpoint leg only and keep a serial fallback (`PARALLEL=0`).

### 2.6 Split csi-sanity mount and block into parallel ssh sessions

- A subset of 2.5: it only overlaps 6.0 s of block with mount. **CP: about 6 s.** Do it as part of 2.5, not on its own.

### 2.7 Go build cache: is setup-go caching on, and with which key?

It is on (`cache: true` by default) with key `setup-go-Linux-x64-ubuntu24-go-1.27.3-<sha256(go.sum)>`, the same for all three jobs. In practice it is **broken in three ways**:

1. **The restore fails every time.** setup-go reads `go 1.27.0` from go.mod and installs 1.27.0 (run 18: about 9 s including "Adding to the cache"). Its `go env GOMODCACHE` call then triggers GOTOOLCHAIN=auto, which downloads go1.27.3 into `~/go/pkg/mod/golang.org/toolchain@…`. The cache tarball contains the same toolchain, so tar hits about 1,800 to 4,900 `Cannot open: File exists` errors, exits with 2, and setup-go logs `Failed to restore … Cache is not found` (runs 14, 19, 22, in every job). Most files are still extracted, which is why proto's buf run is warm.
2. **One key for three jobs.** The first job to finish wins the save (proto in run 18 saved 488 MB of buf dependencies), and every other job's save fails with "another job may be creating this cache". unit and real-zfs never get their own modules or build cache: run 22's VM job still logs `go: downloading … csi-test`.
3. **The key only moves with go.sum**, so a build cache, once saved, is frozen until a dependency changes.

Fix (low risk): pin the toolchain from the `toolchain` line, set `GOTOOLCHAIN: local`, turn off setup-go's cache, and use actions/cache with a per-job key: `go-<job>-<os>-<sha256(go.sum)>-<github.sha>`, restore-keys `go-<job>-<os>-<sha256(go.sum)>-`. Save on push to main only; PRs restore main's cache. Add a guard step that fails if `go env GOVERSION` is not the `toolchain` line, which is the same toolchain-drift class as the gen/ bug.

Estimated savings: setup-go 20 s down to about 8 s in every job (no 1.27.0 install, no failing 488 MB restore, no failing save); unit vet 13 s down to about 3 s; race-test compile about 15 to 20 s down to about 3 s; real-zfs build 20 to 30 s down to about 1 s, which only matters once 2.2 lands. **CP on unit: about 35 s.**

### 2.8 Skip the real-zfs job on docs-only changes with paths filters: is it safe?

- **Saves nothing on this PR.** For `pull_request`, GitHub evaluates `paths` against the whole PR diff (base...head), not the pushed commit. PR #1's diff always contains Go code, so even the README-only pushes (run 15 `16792b4`, run 17 `ccbd7ed`, run 6 `afbe52e`) would have run everything. It only helps future small docs PRs, where it saves the whole job.
- Safe only if:
  1. It is an allow-list of non-code paths (`**/*.md`, `docs/**`, `LICENSE`, `AGENTS.md`), and everything else counts as code, including `ci/**`, `.github/**`, `api/**`, `gen/**`, `buf*.yaml`, `go.*` and `ci/expected-skips.txt`.
  2. It is implemented as a `changes` job plus job-level `if:`, **not** workflow-level `paths-ignore`. A required check whose workflow never starts stays "Expected", which blocks the merge. A job skipped by `if:` reports success.
  3. Every push to main still runs everything.
- I checked that no test reads `docs/` (the only `docs/` references in `*.go` are comments), and the JSON under `docs/research` is not loaded by any code.

### 2.9 Drop -race from one place

- There is only one place: `-race` appears once (unit, `go test -race ./...`). The VM binaries are built with `CGO_ENABLED=0`, so they cannot be race-built, and fuzz is not race-built. Nothing is duplicated.
- Dropping it would save about 15 s of the 30 to 39 s step (a race build is about 2x a normal one, and race execution is 2 to 10x slower), but that is the only race coverage for `internal/lock`, the reconcile locks and the concurrent-create serialisation. **Keep it.** Get the time back from the warm cache (2.7) instead.

### 2.10 The fuzz smoke duration

- Measured: 40 to 44 s, which is 30 s of fuzztime plus about 12 s of instrumented builds. FuzzCreateConverges reached 256k execs in 9 s with new coverage still arriving. That makes it a smoke test, not a search.
- Gap: `FuzzReconcileSafe` (`internal/endpoint/reconcile_fuzz_test.go`) and `FuzzClassify` (`internal/zfs/fuzz_test.go`) are **never fuzzed in CI**. Only their seed corpora run, as ordinary tests.
- Proposal: move the fuzz smoke out of `unit` into a parallel `fuzz` job: 4 targets × 8 s, with the corpus cached per target (`$GOCACHE/fuzz`). That job takes about 50 s off the critical path, since it is parallel. Nightly: 10 min per target, with the corpus saved, and new crashers uploaded and turned into failures.
- **CP: about 44 s off unit**, and unit is half of today's critical path.

### 2.11 Target: wall-clock from push to green

| | current (median) | proposed (median) |
|---|---:|---:|
| unit | 111 to 131 s | about 50 s (setup-go 8, tidy 2, vet 3, race test about 25, overhead 10) |
| fuzz (new, parallel) | inside unit | about 50 s (4 × 8 s fuzztime, warm builds) |
| proto | 27 to 33 s (94 s cold) | about 20 s (30 s with a cold buf cache) |
| real-zfs | 102 to 139 s (p90 325+) | about 55 to 60 s: queue 3, checkout 2, debs 5, baked image restore 7, archive index 1, max(boot to ssh 15, setup-go 8 + build 1), prechecks 2, scp and pools 2, parallel tests 12, checks and evidence 2, post 3 |
| **run wall** | **130 s median, 136 s for all completed, tail 325 to 719 s** | **about 60 to 70 s median; about 80 s p90; +60 s once per archive or base-image change (rebake)** |

## 3. Reliability

### 3.1 Classification of each step

| step | job | class | network dependency |
|---|---|---|---|
| checkout | all | network | github.com |
| setup-go | all | network | go-versions release, proxy.golang.org (toolchain), Actions cache |
| gofmt | unit | deterministic | |
| go mod tidy -diff | unit | network (cold module cache) | proxy.golang.org, sum.golang.org |
| vet / test -race | unit | deterministic once modules are present | |
| fuzz smoke | unit | non-deterministic by design (random inputs), offline | |
| buf lint / generate | proto | **network** (`go run buf@v1.47.2` fetches about 100 modules) | proxy.golang.org, sum.golang.org |
| enable kvm | VM | deterministic | |
| install qemu | VM | **network** (azure mirror; the tail source) | azure.archive.ubuntu.com |
| SHA256SUMS + gpgv, image | VM | **network** | cloud-images.ubuntu.com |
| go test -c (in script) | VM | network on a cold module cache | proxy.golang.org |
| cloud-init packages | VM guest | **network** | archive.ubuntu.com (stonking) through slirp NAT |
| prechecks, tests, check-skips | VM | deterministic (a real kernel, but a fresh pool every run) | |
| cache save | all | network | Actions cache |

### 3.2 Retry policy per network step

| step | policy |
|---|---|
| apt (host) | 1) .deb cache hit: no network at all. 2) On a miss: `apt-get -o Acquire::Retries=3 -o Acquire::http::Timeout=20`, wrapped in `timeout 150`. If that times out, **switch the mirror** (`sed -i 's/azure\.archive/archive/' /etc/apt/apt-mirrors.txt`) and retry once with `timeout 150`. The failures we saw were slow, not erroring (79 to 96 kB/s), so retries alone do nothing. Only a timeout plus a mirror switch bounds them |
| SHA256SUMS / .gpg | `curl -fsSL --retry 4 --retry-all-errors --retry-delay 5 --connect-timeout 10 --max-time 60`. A gpg failure is **not** retried, because it is integrity, not transport |
| cloud image | Cache first, keyed on sha256. On a miss, download from the serial directory with `curl … --retry 3 --retry-all-errors --max-time 300` (877 MB, normally 6 s). On a sha256 mismatch: re-fetch SHA256SUMS once (a publish race), then fail |
| `go run buf@v1.47.2` | Split the fetch from the run: `for d in 0 10 30; do sleep $d; GOFLAGS=-mod=mod go install github.com/bufbuild/buf/cmd/buf@v1.47.2 && break; done`, then run `$(go env GOPATH)/bin/buf`, cached under a key with the buf version. The version is still pinned and sum.golang.org-verified |
| go module download (unit, VM build) | `go mod download` in a 3-try backoff loop (0, 10, 30 s) as its own step, so that every later step is offline |
| guest apt (cloud-init, unbaked path only) | user-data `apt: {conf: 'Acquire::Retries "5"; Acquire::http::Timeout "20";'}`. Check `cloud-init status --format json` `.errors` and fail **as "guest apt"**, not later as "zfs version mismatch" |
| Actions cache | no retry; a miss degrades to the slow path, never to a failure |

### 3.3 Timeouts sized from the data

| scope | now | data (normal / max seen) | proposed |
|---|---|---|---|
| unit job | 15 min | 111 to 163 s | 6 min |
| fuzz job (new) | n/a | n/a | 4 min (PR), 60 min (nightly) |
| proto job | 10 min | 27 to 94 s | 5 min |
| VM job | 30 min | 102 to 139 s / 715 s | 12 min |
| apt step | none | 12 to 26 s / 605 s | `timeout-minutes: 6` (2 × 150 s + dpkg) |
| ssh-up wait | 90 × (5 s + 5 s connect) ≈ 15 min | about 15 s baked, about 50 s unbaked | deadline of 120 s |
| cloud-init wait | 120 × 5 s+ ≈ 10+ min | 36 to 48 s of guest uptime | deadline of 90 s baked, 300 s unbaked |
| per test binary | `-test.timeout=8m` | 6 to 12 s | `-test.timeout=3m` (still 15x headroom); nightly scale test separate |

### 3.4 Every failure names its cause

Replace "the trap prints a console tail" with a stage-tracking classifier. The current diag also prints three SSH host-key randomart blocks twice, about 80 % of a failing log. Sketch:

```bash
stage=init; cause_of() { case $1 in
  fetch-*|apt-*)   echo "network: $1";;
  boot|ssh-up)     echo "guest boot: $1";;
  cloud-init)      echo "guest apt/cloud-init";;
  precheck-*)      echo "CI precondition: $1";;
  test-*)          echo "product test failure: $1";;
  budget-*)        echo "coverage budget: $1";; esac; }
trap 'rc=$?; if [ $rc -ne 0 ]; then
  c=$(cause_of "$stage"); echo "::error title=real-zfs failed::$c (exit $rc)"
  echo "| FAILED | stage \`$stage\` | $c | exit $rc |" >>"$summary"; diag; fi; …' EXIT
stage=fetch-sums; curl …; stage=fetch-image; …; stage=ssh-up; …
```

Each `ci/check-skips.sh` error already names its cause. Add the leg name to the summary title so the run page shows "coverage budget: vm/endpoint" without opening logs.

### 3.5 Failure ledger (runs 3 to 22)

| run | sha | job / step | cause | class | fixed by |
|---|---|---|---|---|---|
| 3 | 65f1bc5 | real-zfs (host noble) / test | thick zvols filled the 2G pool. The endpoint mapped zfs_setprop_error ENOSPC to Internal instead of ResourceExhausted | **real bug** (and the CI pool was undersized) | 74f9092 (run 4) |
| 8 | ab8e9bf | VM / test | `curl: (22) … 404`: the releases/ tree names images `ubuntu-<ver>-…` | CI bug | d1ae316 (run 9) |
| 9 | d1ae316 | VM / test | `qemu-system-x86_64: -nographic cannot be used with -daemonize` | CI bug | 48296aa (run 10) |
| 13 | b540a2c | unit / proto gen | `git diff gen/`: doc-comment layout from go/format 1.24 vs 1.27 (plugins built by different toolchains) | CI bug (toolchain drift) | bdf89d0 (run 16) |
| 14 | 1357361 | unit / proto gen | same gen/ drift. The VM job was green but took 715 s (apt 605 s at 79 kB/s) **and the export tests SKIPPED** ("needs a running NFS server") while the job passed | CI bug, plus a slow-mirror incident, plus a **validity hole** (green without NFS coverage) | bdf89d0; skip budget c9c73ad |
| 15 | 16792b4 | unit / proto gen | same gen/ drift (a README-only commit that inherited the PR's state) | CI bug | bdf89d0 |
| 18 | 9b6e1b6 | VM / test | nfsd thread count read as the `ci` user; `/proc/fs/nfsd/threads` is 0600, so the check reported 0 | CI bug | 0c884df (run 19). Inferred from the fix commit and the failure time; the tail only shows the diag |
| 19 | 0c884df | (green) | apt 212 s at 96 kB/s | slow-mirror incident (not a failure) | proposed 2.3 |
| 20 | 45c54b2 | VM / test | NFS precheck required `/etc/exports.d` and `/var/lib/nfs/etab`, which are created lazily on first share or export. Run marked cancelled because run 21 superseded it after the VM job had already failed | CI bug | 57ffbf6 (run 22) |
| 21 | 2246735 | VM / test | `NFS prerequisites missing in the VM (exportfs, /etc/exports.d, etab)`: same precheck | CI bug | 57ffbf6 (run 22) |

Totals: 9 red runs plus 1 cancelled-but-failing. **8 CI bugs (80 %), 1 real bug, 0 flakes**, and 2 latency incidents. Every CI bug was a first-run defect in new CI code, and none recurred after its fix. The gate's false-red rate on a stable pipeline is about 0. Its real risk is **latency (apt) and false greens** (run 14). Runs 1 and 2 are outside the window.

## 4. Validity

### 4.1 Record what a green run proved: one evidence row per run

The summary today prints serial, sha256, kernel and zfs version, plus a skip table. Make it **one machine-readable row**, appended to the step summary and uploaded as `evidence.json`, so runs can be compared:

| run | sha | image serial | image sha256 | baked key | kernel | zfs (user / kmod) | nfs-kernel-server | nfsd threads | endpoint tests run / pass / skip (allowed) | csi tests run / pass | sanity specs mount / block (floor 44) | archive fresh? |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 22 | 57ffbf6 | 20261025 | (sum) | n/a (unbaked) | 7.3.0-9-generic | 2.4.4-1ubuntu4 / 2.4.4-1ubuntu4 | 1:2.9.1-1ubuntu1 | 16 | 64 top-level PASS lines across all legs; 11 top-level skips + 4 subtest skips (of TestCloneFailureAfterCloneConvergesToSameState) = 12 allowed entries met | n/a | 44 / 44 of 103 | yes |

Sources: `dpkg-query -W -f '${Version}' nfs-kernel-server zfsutils-linux` in the guest; counts from `-test.list` plus the PASS and SKIP lines that check-skips already parses. Fix one misleading line along the way: `skip budget vm/endpoint: 15 skipped, 12 allowed` (run 22) counts subtests against top-level entries. It is correct but reads as an overrun. Print top-level and subtest counts separately.

### 4.2 Nightly job (schedule plus workflow_dispatch)

| leg | what it catches | cost (estimate) |
|---|---|---|
| **fresh image, no caches** (no image, bake, deb or Go cache) | archive drift the bake key cannot see, the cold path rotting, cloud-init changes, new base-image serials | about 3 min (today's job) |
| **long fuzz**: 4 targets × 10 min, corpus cached, crashers uploaded | parser and convergence bugs. FuzzReconcileSafe and FuzzClassify are never fuzzed today | about 42 min of one runner |
| **scale test at 10k datasets** on the real VM: create 10k (zfs create about 15 to 30 ms each on a file vdev, so 3 to 5 min), then time a full reconcile scan, `zfs get -j` parse, ListVolumes pagination and DeleteVolume with 10k siblings; assert O(n) budgets (e.g. a scan under 10 s) | quadratic scans, pagination-token bugs, the 8 m test timeout at scale | about 10 to 12 min |
| **all suites with `-count=3 -shuffle=on`** on the VM | order dependence and leaked pool state | about 2 min |

Nightly failures should open or annotate an issue (labelled `needs-triage` per docs/agents/triage-labels.md), not block PRs.

### 4.3 Would a second guest add real coverage? Only if it ships 2.4

- **Debian 13 + backports**: I could not verify from here (deb.debian.org returns 403 through this sandbox's proxy). It is only worth adding if `trixie-backports` carries zfs-linux **2.4.x**. Even then, the *userland* behaviour our contract tests probe would be the same OpenZFS 2.4. The real difference is that Debian builds the kmod with **DKMS** (Ubuntu ships it prebuilt in linux-modules), which tests packaging, not our code. DKMS adds 3 to 5 min per boot, so at most nightly. **Not per-PR.**
- **TrueNAS 27 nightly**: this is the production target, and it runs TrueNAS's own `zfs` fork (truenas/zfs 2.4 branch, with its own patches) and middleware that also manages shares, so it is the one second guest with real coverage value. But it is an installer ISO, not a cloud image (no cloud-init/NoCloud), and an unattended install plus API bootstrap under KVM is a project of its own. Recommendation: **nightly-only, after a one-off spike** proves an unattended install. Never on the PR path.
- Ubuntu 26.10 stays the single PR gate, matching ADR 0004 (OpenZFS 2.4 only).

## 5. Proposal: top 5 changes, ranked by minutes saved per unit of risk

| rank | change | CP saved (median) | tail saved | risk (1 to 5) | min/risk |
|---:|---|---:|---:|---:|---:|
| 1 | qemu .deb cache plus apt timeout and mirror switch | 10 s | p90 −3 to −10 min (E ≈ 52 s/run) | 1 | 1.0 |
| 2 | Fix the Go cache: pin the toolchain, GOTOOLCHAIN=local, per-job actions/cache keys | about 35 s on unit, 12 s on every job's setup | | 1 | 0.6 |
| 3 | Move the fuzz smoke to a parallel job (4 targets × 8 s) plus nightly long fuzz | about 44 s on unit | | 1 | 0.7 |
| 4 | Pre-baked guest (first-boot snapshot) keyed on base sha256 plus archive Packages versions, base and bake in actions/cache, nightly fresh rebake | about 40 s on the VM | removes guest-apt network dependency | 3 | 0.22 |
| 5 | Split the VM script into boot/test steps (boot before setup-go) plus parallel in-guest legs on separate pools | about 10 s plus 11 s | | 3 | 0.12 |

The order matters for the wall clock because unit and the VM job are co-critical. Doing 1+2+3 brings unit to about 50 s, but the VM job stays at about 115 s, so the run is about 115 s. Adding 4+5 brings the VM job to about 55 to 60 s, so the run is **about 60 to 70 s**.

### 5.1 Diff sketch: ci.yml

```diff
@@ env (top level, new)
+env:
+  GOTOOLCHAIN: local          # never auto-switch: one toolchain, from go.mod's toolchain line
@@ jobs:
+  changes:                    # docs-only detection for future small PRs (job-level skip, not paths-ignore)
+    runs-on: ubuntu-24.04
+    timeout-minutes: 2
+    outputs:
+      code: ${{ steps.f.outputs.code }}
+    steps:
+      - uses: actions/checkout@<sha> # v4
+        with: { persist-credentials: false, fetch-depth: 0 }
+      - id: f
+        run: |
+          if [ "${{ github.event_name }}" != pull_request ]; then echo code=true >>"$GITHUB_OUTPUT"; exit 0; fi
+          git diff --name-only "origin/${{ github.base_ref }}...HEAD" \
+            | grep -Ev '(^docs/|\.md$|^LICENSE$)' -q && echo code=true >>"$GITHUB_OUTPUT" || echo code=false >>"$GITHUB_OUTPUT"
+
   unit:
     runs-on: ubuntu-24.04
-    timeout-minutes: 15
+    timeout-minutes: 6
     steps:
       - uses: actions/checkout@…
       - uses: actions/setup-go@…
         with:
-          go-version-file: go.mod
+          go-version: '1.27.3'   # = go.mod toolchain; checked below
+          cache: false           # broken: toolchain collision + one key for 3 jobs
+      - name: toolchain matches go.mod
+        run: test "$(go env GOVERSION)" = "$(sed -n 's/^toolchain //p' go.mod)"
+      - uses: actions/cache@<sha> # v4
+        with:
+          path: |
+            ~/go/pkg/mod
+            ~/.cache/go-build
+          key: go-unit-${{ runner.os }}-${{ hashFiles('go.sum') }}-${{ github.sha }}
+          restore-keys: go-unit-${{ runner.os }}-${{ hashFiles('go.sum') }}-
+          # save only from main: PRs restore main's cache instead of thrashing the 10 GB budget
+          lookup-only: false
+      - name: modules (network, retried)
+        run: for d in 0 10 30; do sleep $d; go mod download && exit 0; done; exit 1
       - name: gofmt
       …
       - name: test (simulator)
         run: go test -race -count=1 ./...
-      - name: fuzz smoke
-        run: |
-          go test -run '^$' -fuzz FuzzCreateConverges -fuzztime 20s ./internal/endpoint
-          go test -run '^$' -fuzz FuzzParseColumns -fuzztime 10s ./internal/zfs
+
+  fuzz:
+    runs-on: ubuntu-24.04
+    timeout-minutes: 4
+    strategy:
+      matrix:
+        target: [endpoint/FuzzCreateConverges, endpoint/FuzzReconcileSafe, zfs/FuzzParseColumns, zfs/FuzzClassify]
+    steps:
+      - (checkout, setup-go 1.27.3 cache:false, actions/cache key go-fuzz-…)
+      - run: go test -run '^$' -fuzz "^${T#*/}\$" -fuzztime 8s ./internal/${T%%/*}
+        env: { T: '${{ matrix.target }}' }

   proto:
-    timeout-minutes: 10
+    timeout-minutes: 5
     env:
-      BUF: go run github.com/bufbuild/buf/cmd/buf@v1.47.2
+      BUF_VERSION: v1.47.2
     steps:
+      - uses: actions/cache@<sha>
+        with: { path: ~/go/bin/buf, key: buf-${{ env.BUF_VERSION }}-go1.27.3 }
+      - name: fetch buf (network, retried)
+        run: |
+          test -x ~/go/bin/buf && exit 0
+          for d in 0 10 30; do sleep $d; go install github.com/bufbuild/buf/cmd/buf@$BUF_VERSION && exit 0; done; exit 1
       - name: buf lint
-        run: $BUF lint
+        run: ~/go/bin/buf lint

   real-zfs:
+    needs: changes
+    if: needs.changes.outputs.code == 'true'
     name: real ZFS 2.4 (Ubuntu 26.10 VM)
-    timeout-minutes: 30
+    timeout-minutes: 12
     steps:
       - uses: actions/checkout@…
-      - uses: actions/setup-go@…
       - name: enable kvm
         …
+      - name: qemu debs cache
+        id: debs
+        uses: actions/cache@<sha>
+        with:
+          path: ~/qemu-debs
+          key: qemu-debs-${{ env.ImageOS }}-${{ env.ImageVersion }}-v1
       - name: install qemu
-        run: |
-          sudo apt-get update
-          sudo apt-get install -y --no-install-recommends qemu-system-x86 qemu-utils cloud-image-utils
+        timeout-minutes: 6
+        run: ci/install-qemu.sh ~/qemu-debs   # dpkg -i on hit; apt with timeout+mirror switch on miss
+      - name: guest image (verify, restore or bake)
+        id: img
+        run: ci/real-zfs-vm.sh image stonking current 2.4   # prints base sha, bake key to $GITHUB_OUTPUT
+      - uses: actions/cache/restore@<sha>
+        id: bake
+        with:
+          path: ${{ runner.temp }}/zfsvm/baked.qcow2
+          key: ${{ steps.img.outputs.bake_key }}
+          restore-keys: ${{ steps.img.outputs.bake_prefix }}
+      - name: boot guest (background)
+        run: ci/real-zfs-vm.sh boot ${{ steps.bake.outputs.cache-hit == 'true' && 'baked' || 'bake' }}
+      - uses: actions/setup-go@…        # now overlaps the guest boot
+        with: { go-version: '1.27.3', cache: false }
+      - uses: actions/cache@<sha>       # go-vm-… per-job key (CGO_ENABLED=0 objects)
+      - name: build test binaries
+        run: ci/real-zfs-vm.sh build
       - name: test (Ubuntu 26.10, real zfs 2.4)
-        run: ci/real-zfs-vm.sh stonking current 2.4
+        run: ci/real-zfs-vm.sh test 2.4
+      - name: save baked image (first boot of a new key, main or PR)
+        if: steps.bake.outputs.cache-hit != 'true' && success()
+        uses: actions/cache/save@<sha>
+        with: { path: '${{ runner.temp }}/zfsvm/baked.qcow2', key: '${{ steps.img.outputs.bake_key }}' }
```

### 5.2 Diff sketch: ci/install-qemu.sh (new)

```bash
#!/usr/bin/env bash
set -euo pipefail
debs=${1:?deb cache dir}; pkgs=(qemu-system-x86 qemu-utils cloud-image-utils)
if compgen -G "$debs/*.deb" >/dev/null; then
  sudo dpkg -i "$debs"/*.deb && exit 0    # closed set from a previous apt resolve on the same image
  echo "::warning::cached debs failed to install, falling back to apt"
fi
apt_try() { sudo timeout 150 apt-get -o Acquire::Retries=3 -o Acquire::http::Timeout=20 "$@"; }
get() { apt_try update && apt_try install -y --no-install-recommends --download-only "${pkgs[@]}"; }
get || { echo "::warning::apt mirror slow/failing; switching to archive.ubuntu.com"
         sudo sed -i 's/azure\.archive\.ubuntu\.com/archive.ubuntu.com/' /etc/apt/apt-mirrors.txt; get; }
mkdir -p "$debs"; cp /var/cache/apt/archives/*.deb "$debs"/
sudo apt-get install -y --no-install-recommends "${pkgs[@]}"
test -s /usr/share/keyrings/ubuntu-cloudimage-keyring.gpg
```

### 5.3 Diff sketch: ci/real-zfs-vm.sh (sub-commands, bake key, parallel legs, cause naming)

```diff
-# usage: ci/real-zfs-vm.sh <ubuntu-series> [image-build] [openzfs-minor]
+# usage: ci/real-zfs-vm.sh image <series> [build] [minor] | boot baked|bake | build | test [minor]
+# Phases are separate workflow steps so each is timed by the Actions API and the
+# guest boots while setup-go and the compile run. State lives in $work.
+stage=init; trap 'on_exit' EXIT     # on_exit: ::error with cause_of "$stage", summary row, diag
@@ image
-base="https://cloud-images.ubuntu.com/$series/$build"
+stage=fetch-sums
+curl … "$base/SHA256SUMS" "$base/SHA256SUMS.gpg"; gpgv …           # integrity: never retried
+serial=$(curl -fsSL --retry 4 --retry-all-errors "$base/" | sed -n 's/.*\.\([0-9]\{8\}\)\.image_changelog.*/\1/p' | tail -1)
+base="https://cloud-images.ubuntu.com/$series/$serial"                # pin: no mid-publish race
+# bake key = what we would install today: base image + archive versions of zfs-linux and nfs-utils
+stage=fetch-index
+idx=$(for p in "$series" "$series-updates" "$series-security"; do
+  curl -fsSL --retry 3 --max-time 30 "http://archive.ubuntu.com/ubuntu/dists/$p/main/binary-amd64/Packages.xz" | xz -dc |
+  awk '$1=="Package:"{p=$2} $1=="Source:"{s=$2} $1=="Version:"{v=$2} /^$/{if(s~/^(zfs-linux|nfs-utils)/||p~/^(zfsutils-linux|nfs-kernel-server)$/)print p,v; s=""}'
+done | sort -u) || { echo "::warning::archive freshness unverified"; idx=unknown; }
+echo "bake_prefix=bake-$sum-" >>"$GITHUB_OUTPUT"
+echo "bake_key=bake-$sum-$(sha256sum <<<"$idx" | cut -c1-16)" >>"$GITHUB_OUTPUT"
@@ boot
-qemu-system-x86_64 -enable-kvm -cpu host -m 4096 -smp 2 -display none \
-  -drive file="$img",if=virtio …
+if [ "$mode" = baked ]; then
+  qemu-img create -q -f qcow2 -b "$work/baked.qcow2" -F qcow2 "$work/run.qcow2" 20G   # overlay: cache stays pristine
+  write_user_data --no-packages            # ssh key only
+else
+  cp "$work/$series.img" "$work/run.qcow2"; qemu-img resize "$work/run.qcow2" 20G
+  write_user_data --packages               # zfsutils-linux nfs-kernel-server + apt retries
+fi
+qemu-system-x86_64 -enable-kvm -cpu host -m 6144 -smp 4 -display none \
+  -drive file="$work/run.qcow2",if=virtio …
@@ test
-for _ in $(seq 1 90); do vm true … sleep 5; done
+stage=ssh-up;     wait_for 120 'true'
+stage=cloud-init; wait_for "$([ "$mode" = baked ] && echo 90 || echo 300)" 'test -e /var/lib/cloud/instance/boot-finished'
+[ "$mode" = bake ] && bake_snapshot   # cloud-init clean --logs --machine-id; apt-get clean; poweroff;
+                                      # qemu-img convert -c -O qcow2 run.qcow2 baked.qcow2; reboot from overlay
@@ run the legs in parallel, one pool each
-  truncate -s 10G vdev0
-  sudo zpool create -f -O compression=lz4 zfsep-test "$PWD/vdev0"
-  sudo "${env[@]}" ./endpoint.test …  | tee endpoint.log || rc=$?
-  sudo … CSI_SANITY_ACCESS_TYPE=mount ./csi.test … | tee csi-mount.log || rc=$?
-  sudo … CSI_SANITY_ACCESS_TYPE=block ./csi.test … -test.run "^TestSanity\$" | tee csi-block.log || rc=$?
+  leg() { truncate -s 4G "vdev-$1"; sudo zpool create -f -O compression=lz4 "zfsep-$1" "$PWD/vdev-$1"
+          shift_env=(ZFS_TEST_POOL="zfsep-$1" ZFS_REQUIRE_REAL=1); shift
+          sudo "${shift_env[@]}" "$@" >"$log" 2>&1; }
+  leg ep   ./endpoint.test "${t[@]}"                                  & p1=$!   # all NFS-export tests live here
+  leg csim env CSI_SANITY_ACCESS_TYPE=mount ./csi.test "${t[@]}"      & p2=$!
+  leg csib env CSI_SANITY_ACCESS_TYPE=block ./csi.test "${t[@]}" -test.run "^TestSanity\$" & p3=$!
+  for p in $p1 $p2 $p3; do wait $p || rc=1; done
+  for p in ep csim csib; do sudo zpool destroy -f "zfsep-$p"; done
+stage=budget; ci/check-skips.sh …
+stage=evidence; evidence_row >>"$summary"; evidence_json >"$work/evidence.json"
```

### 5.4 Diff sketch: nightly workflow (new `.github/workflows/nightly.yml`)

```yaml
on:
  schedule: [{ cron: '17 3 * * *' }]
  workflow_dispatch:
jobs:
  fresh-vm:        # no caches at all: catches archive and cloud-init drift, and rebakes
    steps: [ … ci/install-qemu.sh /nonexistent; ci/real-zfs-vm.sh image/boot bake/build/test; fail on "freshness unverified" ]
  fuzz-long:
    strategy: { matrix: { target: [4 targets] } }
    timeout-minutes: 15
    steps: [ restore corpus; go test -fuzz … -fuzztime 10m; save corpus; upload testdata/fuzz on failure ]
  scale-10k:
    timeout-minutes: 25
    steps: [ boot baked; ZFS_SCALE=10000 ./endpoint.test -test.run '^TestScale' -test.timeout=20m ]  # new test
  shuffle:
    steps: [ boot baked; each leg with -test.count=3 -test.shuffle=on ]
```

## 6. Other findings worth acting on

- `go-version-file: go.mod` installs **1.27.0** (the `go` line) and then auto-switches to **1.27.3** (the `toolchain` line). Two toolchains are downloaded per job, and this is the root cause of the cache collision. The gen/ formatting drift (runs 13 to 15) is the same class of bug, toolchain mismatch.
- The failure diag prints SSH host-key fingerprints and randomart twice (console tail plus cloud-init output). Filter the console tail to `cloud-init|error|zfs|nfsd|Call Trace`.
- Run 20 shows "cancelled" at run level although its VM job had already failed (concurrency cancel-in-progress). When reading history, use job conclusions, as `timings.csv` does.
- The current cloud-init wait loops allow up to about 25 min of polling before giving up. Only the 30 min job timeout bounds them. Use deadlines (3.3).
