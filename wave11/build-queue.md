# Wave 11: the CI build and download queue, and its supply chain

Scope: PR branch `claude/gallant-johnson-1qaxp5` at c73e40d. Workflow `ci` runs 40456494050 (8cc52ef), 40456984582 (b5c4ec4), 40458729931 (00a2b8d), 40458837093 (95e003a) and 40459452633 (c73e40d). No newer run exists; run 33 is the latest. Step timings come from the Actions jobs API, and sub-step timings from the job logs, parsed with `phases.py`. Raw logs are in `wave11/data/`.

ADR 0005 sets a 60s budget. No run has met it yet. Run wall times: 1:55, 2:02, 0:11 (the action failed to load), 2:27 and 1:59. The VM job is the long pole in every run.

---

## 1. Anomalies (ranked)

| # | Anomaly | Data | Root cause | Status |
|---|---|---|---|---|
| A1 | **Nothing can restore these caches except PR #1 itself.** | The repo's default branch is `claude/gallant-johnson-1qaxp5` (the PR head); the PR base is `main`. `ci.yml` runs on `push` only for `main`, and nightly never saves. All four cache entries (Go ×3, guest) were saved by `pull_request` runs, so they are scoped to `refs/pull/1/merge`. | GitHub cache scoping: a run restores from its own ref, from the PR base branch and from the default branch. No run ever writes to `main` or to the default branch. **Every new PR, and every new `current/` image serial, starts fully cold (1:59–2:27).** Nightly restores keys that are never saved, so it is always cold. | verified (repo API: `default_branch`; PR base `main`; c73e40d restore hit only the keys 95e003a saved) |
| A2 | **The 81s→117s VM step difference is not only guest apt.** | 8cc52ef→95e003a: boot→cloud-init done 47.2→73.6s (+26.4). Image download+sha 6.2→11.9s (+5.7). VM test phase 20.2→26.5s (+6.3). `qemu-img resize` 5.1→1.8s (−3.3). Sum +35s vs the observed +36s. | About a quarter of the gap was the image mirror and slower in-guest tests, not guest apt. Westus runs (b5c4ec4, 95e003a) had slower image downloads (8.5–11.9s) than centralus runs (6.2–6.6s). n=4, so this is a correlation only. | verified (timestamps); the region cause is unverified |
| A3 | **The VM tests run about 40% faster after a reboot.** | Same test code in all four runs (`git diff 8cc52ef c73e40d -- internal cmd` is empty). Endpoint leg: 9.3 / 10.8 / 11.5 / **6.4s**. csi-sanity mount/block: 4.5/4.5, 5.1/5.8, 5.5/6.2, **3.3/3.6s**. Only c73e40d ran the tests on a freshly rebooted overlay guest; the others ran them on the provisioning boot. | Post-provisioning work keeps running on the provisioning boot (hypothesis: snap seeding, dpkg triggers) and competes for the 2 vCPUs. A warm boot should keep the ~15s test phase. | timings verified; cause unverified |
| A4 | **12s of silence after `Image resized.`** (95e003a) | 06:04:07.3 → 06:04:19.4 (the first `go: downloading`). In that window the script runs ssh-keygen, cloud-localds, `qemu -daemonize` and the start of `go test -c`. | Not root-caused. The step prints nothing in that window. Patch 03 adds phase marks and `systemd-analyze blame`. | unverified |
| A5 | **`qemu-img resize` of a qcow2 takes 1.8–5.1s** | 5.1s (centralus), 1.8s (westus), 1.8s (westus), 4.4s (centralus). A metadata-only resize should take milliseconds. | Hypothesis: qemu-img flushes the 877 MB file that curl just wrote, so the time tracks the runner's disk throughput. This fits the region split. Patch 06 creates an overlay instead (cold path only). | unverified |
| A6 | **A silent 3–6s gap inside `apt-get update`** in every run | The last `Get:` line comes, then `Fetched 12.6 MB in 1s` appears 4.0 / 3.1 / 3.3 / **6.3s** later. | apt processes indices nobody needs: Translation-en, plus packages.microsoft.com amd64/arm64/armhf. Patch 01 hides the whole apt step behind other work and drops translations. | gap verified; cause unverified |
| A7 | **`Terminate orphan process: pid (…) (sleep)` in every test job** (cold and warm) | Runner cleanup in both 95e003a and c73e40d. | **Product bug.** `TestKilledCommandIsInterrupted` runs `sh -c 'sleep 30'`. `LocalRunner` sends SIGTERM, then WaitDelay's SIGKILL, to the **leader pid only**, although `childAttr` gives the child its own process group. The grandchild `sleep` outlives `Run`. A real `zfs` that forks a helper would leak the same way. Reproduced locally: **5/5 orphans before patch 05, 0/5 after**, and the zfs and endpoint tests pass. | verified |
| A8 | **Cold guest provisioning ranges from 40s to 74s** | Boot→done: 47.2 / 47.7 / 73.6s with a 5s poll. c73e40d took ≤40.3s, which includes `apt-get clean` and poweroff, with a 1s poll. | Guest apt goes to the image's default mirror. The console log is uploaded only on failure, so the mirror and per-package times cannot be seen. | unverified |
| A9 | **A warm guest boot has not happened yet** | c73e40d missed the cache (`Cache not found`), provisioned, saved 838 MB in 7.6s and then booted an overlay of the stored disk. That overlay boot is the warm code path: ssh plus `is-system-running` took 13.6s, `zfs-kmod-2.4.4`, nfsd threads 16. | The zfs module and nfsd do come up on the overlay boot. `modprobe zfs` runs explicitly; nfs-server persists from `systemctl enable` in runcmd. A restore-from-cache boot is still unobserved: file modes after `tar`, the timer behaviour on an older disk, and the actual restore time. | partly verified |
| A10 | Run 40458729931 (00a2b8d) failed in 11s | setup-go failed to load: the composite outputs called `hashFiles`. | Fixed in 95e003a (`cache-primary-key`). | verified |

---

## 2. Critical path per run (VM job: the long pole in every run)

```
c73e40d  (run 40459452633, VM job centralus, guest COLD, Go caches WARM)   run wall 1:59
t+0   setup+checkout      ██ 2s
t+2   setup-go            ████ 4s     [go.tgz 71.6MB ∥ Go cache 91MB restore 2.0s] → tar xzf 1.6s
t+6   apt update+install  █████████████ 13s   (update 7.4s, of which 6.3s silent; install 4.6s; needrestart 0.8s)
t+19  resolve image key   ▌ 0.5s      SHA256SUMS fetch #1 (unverified, key only)
t+20  restore guest       ▏ 0.2s miss
t+20  real-zfs-vm.sh      86s
        0.6  SHA256SUMS fetch #2 + .gpg + gpgv
        6.6  cloud image 877MB + sha256
        4.4  qemu-img resize                                  (A5)
       40.3  boot ∥ go test -c (warm)  → ssh → cloud-init/guest apt → apt clean → poweroff
        3.9  qemu-img convert 2.3G → cache dir
       13.6  overlay boot → ssh → is-system-running --wait   (= the warm boot)
        1.6  modprobe, zfs version 2.4.4/kmod, exportfs, nfsd 16
       14.8  scp + endpoint 6.4 + csi mount 3.3 + csi block 3.6 + skip checks
t+106 save guest          ███████ 7.6s  (838MB up at ~250MB/s, ON the critical path)
t+113 post/complete       ~2s
static 15s and test 19s finish at about t+15 and t+19: off the critical path.
```

```
95e003a  (run 40458837093, VM westus, guest n/a, Go caches COLD)          run wall 2:27
t+0 setup+checkout 2 | setup-go 3 (miss) | apt 14 | script 117:
      gpg 1.0 | image 11.9 | resize 1.8 | [12s silent, A4] | boot→done 73.6 (5s poll)
      | checks 2 | tests 26.5
    | save Go cache 4s (95.8MB) | post 3
static: setup-go 3 | tidy 3 (module dl 2.2) | vet 30 | gen 1.6 | seeds 1.8 | save 5 → 50s
test:   setup-go 3 | go test -race 49 (≈40 compile) | save 4 → 60s
```

```
8cc52ef  (run 40456494050, VM centralus, old layout, setup-go cache hit)   run wall 1:55
setup-go 10 | apt 13 | script 81: image 6.2 | resize 5.1 | boot→done 47.2 | tests 20.2
```

**Projected warm VM job (guest hit)** at c73e40d plus a hit: 2 + 4 + 13 (apt) + 0.5 + restore of 838MB/2.3G (estimated 6–14s: Go restores ran at 64–90 MB/s, saves at 250 MB/s) + script ≈ 31s (0.6 + 13.6 + 1.6 + 14.8) ≈ **57–65s job, ~60–68s run**. That is still at or over budget. With patch 01, apt overlaps the setup-go and guest restore steps, giving **≈45–52s job, ≈48–55s run**.

### What waits on what, what could overlap
- **apt (13s) has no dependency on setup-go, the image key or the guest restore**, yet it runs before them. The script's SHA256SUMS fetch, image download and `go test -c` don't need qemu either. → Patch 01.
- The **Go tarball download already overlaps** the Go cache restore. Only `tar xzf` (1.6–2.3s) is serial, in all three jobs.
- **`go test -c` (CGO_ENABLED=0, 2 packages, sequential)** already overlaps the guest boot. On a cold Go cache its module downloads ran inside the boot window (95e003a), so it never sat on the critical path.
- The **guest save (7.6s)** and **Go cache saves (4–5s)** run at job end, on the critical path, but only on cold runs. Patch 04 moves cold provisioning onto the scheduled default-branch run, so PRs only restore.
- **The second boot on a cold run (13.6s)** is the price of storing a clean disk. It also bought A3's faster tests.

### Fetched twice (or more)
| Item | Times per run | Bytes |
|---|---|---|
| Go SDK tarball (actions/go-versions) | 3 (one per job), never cached | 3 × 71.6 MB |
| Go modules (cold) | 3 (each job downloads into its own namespace and saves its own copy) | 3 × modcache inside the 91–119 MB entries |
| stonking SHA256SUMS | 2 (workflow key step, unverified; script, gpg-verified) | small, but there is a race if `current/` moves between them → patch 02 |
| apt indices | 1, but 12.6 MB including MS repo lists for 3 arches and Translation-en | 12.6 MB |
| nightly | 5 jobs × Go tarball, cold module download and cold compile, every night | 5 × 71.6 MB + modules |

---

## 3. Every byte, per job (cold vs warm)

| Job | Fetch / build | Cold (95e003a) | Warm (c73e40d) | Integrity check |
|---|---|---|---|---|
| all | actions: checkout@11d5960, cache@5a3ec84, upload-artifact@ea165f8 | tarballs | same | SHA-pinned (Node 20 actions forced onto Node 24: a future-break warning in every job) |
| all | git fetch, depth 1, of `refs/pull/1/merge` | <1s | <1s | TLS, token read-only |
| all ×3 | Go 1.27.3 tarball, 71,607,465 B | overlapped; install 1.7–2.3s | same | **sha256 pinned** (`GO_SHA256`) |
| static | buf v1.47.2, 24,719,887 B | background; ready by `install buf` | same | **sha256 pinned** |
| static | Go modules (tidy pulls the whole graph incl. test-only deps: pprof, gonum, testify…) | 2.2s | 0 (from cache) | go.sum plus sum.golang.org for new ones. On a restore the extracted modcache is trusted, not re-hashed (`go mod verify` would re-hash it) |
| static | GOCACHE + modcache `go1.27.3-static-<go.sum>` | miss → vet 30s, save 124.6 MB in 5s | **hit** 124.6 MB in 3.5s; vet 0.2s | Actions cache scope only |
| static | `go tool protoc-gen-go(-grpc)` built by `buf generate` | 1.6s | 0.7s | go.sum |
| test | GOCACHE `-race` `go1.27.3-test-…` | miss → `go test -race` 49s, save 102.1 MB in 4s | **hit** 102.1 MB in 3.0s; test 9.4s (6.6s is internal/server runtime) | cache scope |
| real-zfs | GOCACHE CGO_ENABLED=0 `go1.27.3-real-zfs-…` | miss, save 95.8 MB in 4s | **hit** 95.8 MB in 2.0s | cache scope |
| real-zfs | apt update 12.6 MB + 18 debs 19.0 MB (qemu 8.2.2, cloud-image-utils) | 14s | 13s | apt InRelease signatures (Ubuntu and MS archive keys) |
| real-zfs | stonking SHA256SUMS ×2 + .gpg | 0.5 + 0.6s | same | **gpgv** with `ubuntu-cloudimage-keyring.gpg` from the runner's ubuntu-keyring; the workflow's first fetch is unverified (key only) |
| real-zfs | cloud image 876,878,336 B | 6.2–11.9s + sha256 | skipped on a guest hit | **sha256 from the gpg-verified list** |
| real-zfs | guest apt (zfsutils-linux, nfs-kernel-server) | 40–74s | skipped on a guest hit | apt InRelease in the guest |
| real-zfs | guest cache `zfsvm-stonking-<img sha>-<script hash>` | miss; save 837,868,951 B (2.3G raw) in 7.6s | never yet restored (A9) | **none beyond cache scope**; the in-guest version/kmod/nfsd checks are validity checks, not integrity checks |
| nightly ×5 | Go tarball, modules, compile | cold every night; `nightly-*` keys are never saved | n/a | as above |

**Cache footprint in the PR scope:** 124.6 + 102.1 + 95.8 + 837.9 MB ≈ 1.16 GB. A new go.sum adds about 320 MB. A new image serial, or (today) any edit to `ci/real-zfs-vm.sh`, adds 838 MB. The repo limit is 10 GB, with LRU eviction and 7-day idle expiry.

**Are the go.sum-keyed caches hit on the next run?** Yes. 95e003a missed and saved all three. c73e40d hit all three exactly (`Cache hit for: …`) and skipped every Go save. On a hit nothing re-saves, so the entry stays frozen at its first build until go.sum changes. That is acceptable, because the repo's own packages rebuild in seconds.

**Guest cache invalidation:** the key includes the image sha and `hashFiles('ci/real-zfs-vm.sh')`. That script changed **9 times in 2 hours** (`git log -- ci/real-zfs-vm.sh`). Most of those edits touched test invocation or checks, not what is on the disk. `stonking/` publishes a new serial roughly weekly (20260918, 0919, 1006, 1011, 1019, 1025). → Patch 02 keys on `ci/guest/**` only; patch 04 re-provisions in the default-branch scope when the serial changes.

**Freshness note:** a cached guest pins the guest package versions until the image serial changes (about a week). A cold run would install the archive's newest zfsutils. The 2.4.x assert still runs on every boot.

---

## 4. Variance table (VM job)

| Run | Region | apt step | apt silent gap | image dl+sha | qemu-img resize | boot→cloud-init done | endpoint leg | csi mount/block | tests (nfsd→end) | VM step |
|---|---|---|---|---|---|---|---|---|---|---|
| 8cc52ef | centralus | 13s | 4.0s | 6.2s | 5.1s | 47.2s (5s poll) | 9.3s | 4.5/4.5 | 20.2s | 81s |
| b5c4ec4 | westus | 13s | 3.1s | 8.5s | 1.8s | 47.7s (5s poll) | 10.8s | 5.1/5.8 | 24s | 85s |
| 95e003a | westus | 14s | 3.3s | 11.9s | 1.8s | 73.6s (5s poll) | 11.5s | 5.5/6.2 | 26.5s | 117s |
| c73e40d | centralus | 13s | 6.3s | 6.6s | 4.4s | ≤40.3s incl. poweroff (1s poll) + reboot 13.6s | **6.4s** | **3.3/3.6** | **14.8s** | 86s |

| Job | Cold | Warm | Swing driver |
|---|---|---|---|
| static | 50s (vet 30s) | 15s (vet 0.2s) | GOCACHE hit/miss |
| test | 60s (`go test -race` 49s) | 19s (9.4s) | GOCACHE hit/miss; 6.6s of real test runtime in internal/server stays either way |
| setup-go | 3s (miss) | 4–5s (hit: 2–3.5s restore) | the warm restore is slower than a miss |

All runners report image `ubuntu-24.04` version 20261012.334.1. Regions varied within a single run: westus2, eastus, westus, centralus, westcentralus. Every run happened 05:38–06:11 UTC, so time of day cannot be separated with this data.

---

## 5. Supply chain and cache poisoning

**Scoping rules applied:**
- A cache saved by a `pull_request` run belongs to `refs/pull/N/merge`. Only later runs and re-runs of that PR can restore it.
- A PR run restores from its own scope, then the base branch (`main`), then the default branch.
- A fork PR gets a read-only token, but it can still **save** into its own `refs/pull/N/merge` scope. Nothing outside that PR can read it.

**Verdict:**
- **No cross-PR or PR→default-branch poisoning is possible** through the new save-on-miss steps, for both the Go caches and the guest.
- The only writers to scopes other PRs can read are `push` runs on `main`, which require write access. No workflow writes the default branch's scope (that is A1).
- What a scope-writer *could* do, it can already do through the PR's own code. Two kinds of cached content are trusted without re-verification:
  - the guest disk (no hash);
  - the extracted modcache (go does not re-hash extracted modules; `go mod verify` would).
- If patch 04 lands, the default-branch scope becomes a shared read source. Only the schedule and `workflow_dispatch` (write access) write it, which is the intended trust boundary.

**Integrity per download:**
| Download | Check |
|---|---|
| Go tarball | sha256 pin |
| buf | sha256 pin |
| Go modules | go.sum + sum.golang.org (default GOPROXY/GOSUMDB; nothing overrides them) |
| qemu debs | apt-signed |
| cloud image | gpgv over SHA256SUMS → sha256 |
| guest packages | apt-signed |
| Go caches and guest | scope only |
| actions | full-SHA pins |

**Permissions:**
- `ci.yml`: `contents: read` at the top level. The logs show `Contents: read, Metadata: read` in every job, and checkout uses `persist-credentials: false`.
- `nightly.yml`: job-level `issues: write` on the jobs that also run repo code (fuzz, explore). `GH_TOKEN` is passed only to the file-issue step. Least-privilege would split issue filing into a `needs:` job. Low risk, no patch.

**One reliability-integrity bug in the guest store (fixed in patch 02):**
- cloud-init writes `boot-finished` even when `packages:` fails, and the script then writes `ok` and saves the disk.
- A transient mirror failure would therefore be cached. Every warm run would fail `exportfs missing` until the key changed, where a cold run would have retried apt.
- Patch 02 stores the disk only when `dpkg-query` reports both packages as `ii`.

---

## 6. Fixes ranked by (seconds saved × reliability gain) / effort

| Rank | Fix | Saves | Reliability | Effort | Patch |
|---|---|---|---|---|---|
| 1 | Keep the default-branch cache scope warm (scheduled + dispatch `ci` run). Also consider making `main` the default branch once PR #1 lands. | 60–90s on the first run of every PR and after each image serial or go.sum change (cold 1:59–2:27 → warm) | High: removes guest-apt variance (40–74s) from PRs | XS | `04-warm-default-branch-scope.patch` |
| 2 | Install qemu in the background from job start; wait just before the VM | ≈13s on **every** VM run (the long pole) | Neutral; drops translations; NEEDRESTART suspended | S | `01-vm-apt-in-background.patch` |
| 3 | Key the guest on `ci/guest/**` (provisioning only), reuse one gpg-verified SHA256SUMS, store only a validated guest, disable guest apt timers, `chmod 600` the restored key, silence the pidfile `cat` | Avoids a cold provision (≈45s + 7.6s save) on each script edit (9 edits in 2h) | High: no sticky half-provisioned guest; no SHA256SUMS race; no unattended-upgrades skewing userland ahead of the kmod on a days-old disk | S | `02-guest-cache-key-and-validation.patch` |
| 4 | Kill the child's process group, not just the leader | 0s CI | Fixes a real orphan leak in `LocalRunner` (A7), verified 5/5 → 0/5 | XS | `05-localrunner-kill-process-group.patch` |
| 5 | Phase timestamps + `systemd-analyze blame` in the VM script | 0s directly; makes A4, A5 and the warm-boot 13.6s measurable | Diagnostic | XS | `03-vm-phase-timestamps.patch` |
| 6 | Cold path: boot a 20G overlay instead of resizing the fresh image | 2–5s on cold runs only | Neutral | XS | `06-cold-overlay-not-resize.patch` |
| — | Not patched (unverified gain): `fstrim` with `discard=unmap` before storing, to shrink the 838 MB guest; point guest apt at a nearer mirror; build both test binaries in one `go test -c -o dir/` call; add `go mod verify` after restore; have nightly save its caches | — | — | — | — |

The patches apply in order 01→06 on c73e40d. They were checked with `git apply` on a scratch copy, then `bash -n`, a YAML parse of ci.yml and of the rendered user-data, and `go vet` plus the `internal/zfs` and `internal/endpoint` tests. **No check that ADR 0005 lists is removed or weakened:**
- the version, kmod, exportfs, nfsd and skip-budget checks run on every boot;
- `guest_ok` gates only whether the disk is *stored*.

---

## 7. Claims

```json
[
 {"claim":"No run writes the default-branch or main cache scope: default branch is the PR head, ci runs on push only for main, nightly never saves; every cache entry is scoped to refs/pull/1/merge so new PRs and nightly start cold","kind":"anomaly","evidence":"repo default_branch=claude/gallant-johnson-1qaxp5; PR #1 base=main; ci.yml on.push.branches=[main]; nightly has no save step; all saves in runs 40458837093/40459452633 were pull_request events","verified":true},
 {"claim":"The go.sum-keyed Go caches are hit exactly on the next run: 95e003a missed and saved static 124,567,966 B, test 102,138,415 B, real-zfs 95,843,038 B; c73e40d hit all three and skipped saves","kind":"speed","evidence":"job logs 122038382969/122038383164/122038382941 'Cache saved'; 122040348773/122040348487/122040348754 'Cache hit for'","verified":true},
 {"claim":"Static job 50s cold vs 15s warm (vet 30s vs 0.2s); test job 60s cold vs 19s warm (go test -race 49s vs 9.4s)","kind":"speed","evidence":"jobs API step timings for runs 40458837093 and 40459452633","verified":true},
 {"claim":"The 81s->117s VM-step gap is only ~26s guest apt; image download +5.7s and in-VM test phase +6.3s explain the rest","kind":"anomaly","evidence":"8cc52ef vs 95e003a: boot->done 47.2 vs 73.6s, image 6.2 vs 11.9s, tests 20.2 vs 26.5s, resize 5.1 vs 1.8s","verified":true},
 {"claim":"VM tests ran ~40% faster on the rebooted overlay guest (endpoint 6.4s vs 9.3-11.5s, csi 3.3/3.6 vs 4.5-6.2s) with identical test code","kind":"anomaly","evidence":"phases.py over the four VM logs; git diff 8cc52ef c73e40d -- internal cmd is empty","verified":true},
 {"claim":"The cause of A3 is post-provisioning background work on the provisioning boot","kind":"anomaly","evidence":"hypothesis; no console log on success","verified":false},
 {"claim":"A 12s silent gap follows 'Image resized.' in 95e003a before any go output","kind":"anomaly","evidence":"vm-95e003a.log 06:04:07.34 -> 06:04:19.38","verified":true},
 {"claim":"qemu-img resize takes 1.8-5.1s, slower in centralus, consistent with flushing the freshly downloaded 877MB image","kind":"anomaly","evidence":"resize 5.1/1.8/1.8/4.4s across runs; flush cause is a hypothesis","verified":false},
 {"claim":"apt-get update has a 3.1-6.3s silent gap after the last Get line in every run; apt step is 13-14s every run","kind":"anomaly","evidence":"Get:21 -> Fetched timestamps in all four VM logs","verified":true},
 {"claim":"LocalRunner signals only the leader pid on cancel, leaking grandchildren; the test job's runner reports an orphan sleep every run","kind":"bug","evidence":"'Terminate orphan process: pid (...) (sleep)' in jobs 122038383164 and 122040348487; local repro 5/5 orphans before patch 05, 0/5 after","verified":true},
 {"claim":"The cold guest is 2.3G on disk, 837,868,951 B in cache, saved in 7.6s on the VM critical path; the warm restore has never been observed","kind":"speed","evidence":"vm-c73e40d.log 'stored provisioned guest (2.3G, 7s)', 'Sent 837868951', no run after 40459452633","verified":true},
 {"claim":"On the overlay (warm-path) boot the zfs module and nfsd come up: zfs-kmod-2.4.4, nfsd threads 16, 13.6s from qemu start to settled","kind":"bug","evidence":"vm-c73e40d.log 06:11:06.2 -> 06:11:19.8, 06:11:20.08, 06:11:21.19; modprobe runs explicitly","verified":true},
 {"claim":"The projected warm VM job (guest hit) is 57-65s without patch 01, ~45-52s with it","kind":"speed","evidence":"c73e40d phase sums plus estimated 6-14s restore of 838MB","verified":false},
 {"claim":"Background qemu install saves ~13s on every VM run","kind":"speed","evidence":"apt step 13-14s in all runs; no dependency on setup-go, key resolve or guest restore","verified":false},
 {"claim":"The guest cache key includes the whole VM script, which changed 9 times in ~2h, so most edits would force a cold provision","kind":"speed","evidence":"git log -- ci/real-zfs-vm.sh; key uses hashFiles('ci/real-zfs-vm.sh')","verified":true},
 {"claim":"A half-provisioned guest (cloud-init boot-finished after a failed apt) would be stored and fail every warm run until the key changes","kind":"bug","evidence":"script writes ok after boot-finished without checking packages; save runs if ok exists","verified":false},
 {"claim":"SHA256SUMS is fetched twice (workflow key step unverified, script gpg-verified); if current/ moves between them a new-image disk is saved under the old key","kind":"bug","evidence":"ci.yml resolve step and real-zfs-vm.sh both curl SHA256SUMS","verified":true},
 {"claim":"PR-scoped caches (Go and guest) cannot be restored by other PRs, forks or the default branch, so save-on-miss creates no cross-PR poisoning path","kind":"integrity","evidence":"GitHub cache scoping: own ref, base branch, default branch; all saves are from pull_request runs","verified":true},
 {"claim":"Restored caches are not re-verified: the guest disk has no hash and go trusts the extracted modcache","kind":"integrity","evidence":"script warm path checks only cache/sum self-report; no go mod verify step","verified":true},
 {"claim":"Downloads are integrity-checked: Go and buf sha256-pinned, cloud image gpgv+sha256, apt-signed debs, go.sum for modules, actions SHA-pinned","kind":"integrity","evidence":"setup-go action, ci.yml, real-zfs-vm.sh","verified":true},
 {"claim":"ci jobs run with contents:read only; nightly grants issues:write to jobs that execute repo code","kind":"integrity","evidence":"job logs GITHUB_TOKEN Permissions; nightly.yml","verified":true},
 {"claim":"The Go SDK tarball (71.6MB) is downloaded 3x per PR run and 5x per nightly, never cached; nightly always builds cold","kind":"speed","evidence":"setup-go action; Content-Length 71607465; nightly has no save","verified":true},
 {"claim":"Image download speed correlates with region (centralus 6.2-6.6s, westus 8.5-11.9s)","kind":"anomaly","evidence":"n=4 runs","verified":false},
 {"claim":"Run 40458729931 failed in 11s because the composite action outputs used hashFiles; fixed in 95e003a","kind":"anomaly","evidence":"jobs API: setup-go step failed at 0s in all three jobs; commit message 95e003a","verified":true}
]
```
