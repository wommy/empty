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
    - the real-ZFS VM suites, one VM job per part of a suite: endpoint in two parts, csi in one (see "The endpoint suite in two parts" below).
  - **Nightly** (`nightly.yml`, 03:17 UTC and on dispatch; nothing in it gates a PR). Three jobs:
    - `fuzz`: exploratory fuzzing of the nine targets in its matrix, 5 minutes each by default (the `fuzztime` input).
    - `real-zfs-full`: the same suites as `ci.yml` (`endpoint`, `csi`), one VM each, the endpoint suite unsplit (one part, 3 shards; no `ZFSVM_PART`), and the csi leg booted with `ZFSVM_INITRD=1`, the only run of the GRUB fallback path; with `ZFSDIFF_FULL=1` and `ZFS_CRASH_FULL=1`. Both sweeps are endpoint tests; the csi job runs the csi suite against the same guest.
      - `ZFSDIFF_FULL=1` replays the whole zfsdiff corpus on the real pool. Its divergences are reported, not failed, until the three ambiguities are settled (`ZFSDIFF_STRICT`).
      - `ZFS_CRASH_FULL=1` runs every crash point of every case on the real pool. The PR's real leg runs each crash case uninterrupted plus one crash point, rotated across cases; the simulator leg runs every point on every PR. The full sweep cost about 29s of the VM's two vCPUs (run 40468062545), and it slowed every other shard.
      - It also runs every destroy of `TestDestroyNeverHitsAReplacedDataset` (OWN-15) on the real pool. The PR's real leg swaps only the first destroy of each of its five cases (`swapSweep`): every case still reaches the guid guard, which is one code path for every destroy. The simulator leg swaps every destroy on every PR. The full sweep was 19 subtests and 18.8 test-seconds, all on one shard (run 40511369632); the first destroys are 5 subtests and 8.0 test-seconds. With the guard disabled (`sameDataset` returning nil), the first destroy of every case fails on the simulator, so the reduced run still catches it.
    - `explore`: every `TestExplore*` test (`-run '^TestExplore'`), which carry the `nightly` build tag, within a 25-minute timeout: the recovery FSM explorer (`TestExploreLifecycle`) and the model sweeps (`TestExploreModelSweep`, `TestExploreModelMasksStillBite`). `ci/check-explore-ran.sh` fails a run in which any `TestExplore*` function of the repository did not pass. A scale test, when one exists, is named `TestExplore*` too.

    Each job files an issue labelled `needs-triage` when it fails: `fuzz` with each reproducer inline (and as an artifact), `real-zfs-full` with the failing tests and the zfsdiff summary (the shard logs are an artifact), `explore` with the failing lines of its log. A human then commits a fuzz reproducer as a seed-corpus entry (`testdata/fuzz/<Target>/<hash>`), and a real-pool or explorer failure as a test, together with the fix, so the PR tier keeps it. The workflow never pushes: its only write permission is `issues`.
- **Mechanisms.** Measured in waves 10 and 13 and adopted where noted. The first CI run of each one confirms the estimates (see the wave 13 notes):
  - **No real waiting in tests:** clocks and durations are injected, and `testing/synctest` is used where no exec is involved.
  - **Assert at the lowest level that proves the row:**
    - a behaviour provable on the planner is tested there, as golden plans;
    - the simulator and VM keep only what needs zfs or races;
    - redundant tests become tables.
  - **Parallel on the VM (adopted):**
    - Process shards come first. Each guest runs three concurrent shards, each on its own pool: each endpoint part job 3 of the 6 endpoint shards, the csi job csi, csi-sanity on filesystems and csi-sanity on zvols (see the VM job sections below). This needs no test-code change.
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
    - **Warm boots load the stored kernel directly** (`-kernel`), with no BIOS and no GRUB, and since wave 16 with no initrd and with the pool-import units masked (see the boot section below). The booted release must match the one stored with the disk.
    - **qemu is unpacked from the runner image's own apt view**, once per runner image version, with apt as the fallback. It is not a custom runner image.
    - **The guest boots while the job sets up Go** and builds the test binaries. The job restores the guest and qemu first, a boot step starts qemu and returns, and the test step waits for the guest.
    - **`cache-warm.yml`** runs hourly and on dispatch on the default branch. It saves the guest, qemu and Go caches where every PR can read them, because a PR's own saves are scoped to that PR. The Go entries are keyed by go.sum and commit and refreshed once per default-branch commit. A PR saves only when nothing for its go.sum was restored.
  - **Tools are fetched, not compiled:**
    - buf is a prebuilt, checksum-pinned binary.
    - Go is the sha256-pinned release tarball. It is downloaded, verified and extracted in the background during the cache restore.
    - The Go build cache stays warm.
  - **No real waiting in the race build:** `GORACE=atexit_sleep_ms=0` drops the race runtime's 1s exit sleep in each test binary. A race still fails the binary.
  - **Few jobs, sized to the critical path:** the static checks share one job, and the real-ZFS VM jobs set the pace.
  - **Two real-ZFS VM jobs, one per suite (adopted, wave 15):** see below; the endpoint suite split in two parts since wave 18, so three VM jobs.

### Two VM jobs: endpoint and csi on separate runners

- **Data.** With all six shards in one guest on one runner (4 vCPUs), the guest was CPU-bound in kernel work. Run 40502060857 (7f5637f, job 122181926805, the first warm run with the wave 14 levers), over the 31.6s test phase:
  - CPU busy 92%: user 22%, sys+irq 69%, steal 1%, iowait 4%;
  - pressure: cpu some 78%, io some 62%, io full 3%, memory 0%;
  - vmstat: run queue 10–41 and idle 1–2% while five or six shards ran; still about 88% busy with only one or two endpoint shards left.
  - shard walls: endpoint 24.3 / 27.0 / 31.3s, csi-sanity mount 23.1s and block 23.2s, csi other 4.7s. csi-sanity took 13.5–13.9s on the 54s run 40497090324, so it is slowed by the other shards, not by itself.
  - Steal at 1% rules out a contended host, so a bigger guest on the same runner would not help. In-guest tuning was tried: the wave 14 levers were estimated at 45–49s and measured 70s push-to-green.
- **Decision.** The VM job is a matrix over the suites in `ci/real-zfs-suites.txt`: `endpoint` (the three endpoint shards, TestPropertyRoundTrip's rows split across them by `PROPRT_SHARD`) and `csi` (csi tests, csi-sanity on filesystems and on zvols). Each job restores the same cache entry (`zfsvm-v4`, guest and qemu; `zfsvm-v5` since the cache-bytes change below), boots its own VM and builds only its own test binaries (`ZFSVM_SUITE`). Its checks run in that job: `ci/check-skips.sh` for its binaries, and in the endpoint job `ci/check-split.sh`. `fail-fast` is off, so one suite's failure does not cancel the other's measurement. `nightly.yml`'s `real-zfs-full` runs the same suites (endpoint unsplit since the two-part split below). `cache-warm.yml` provisions the one guest both jobs restore, and warms one Go namespace per suite (`real-zfs-endpoint`, `real-zfs-csi`).
- **Completeness is proved, not assumed:**
  - `ci/check-suites.sh` (static job) fails unless every package whose tests read `ZFS_TEST_POOL` is in exactly one suite, and `ci.yml`'s and `nightly.yml`'s `suite` matrices and `cache-warm.yml`'s namespaces name every suite exactly once.
  - In each job, `ci/check-skips.sh` fails unless every test its binaries list passed or skipped.
  - In the endpoint job, `ci/check-split.sh` fails unless every top-level test reported in exactly one shard log (TestPropertyRoundTrip in each) and every TestPropertyRoundTrip row ran in exactly one shard.
  - Together: every listed test runs in exactly one job, and once within it.
- **Prediction (from run 40502060857, a 70s-class runner).** The guest's busy CPU over the phase was 31.6s × 4 × 0.92 = 116 CPU-seconds. Sharing each second's busy CPU equally among the shards running then (0.97 busy with three or more, 0.88 with one or two, as vmstat shows) attributes 81.6 CPU-s to the endpoint shards (19.0 / 23.7 / 39.0) and 37.9 to csi (17.3 / 17.5 / 3.1). The sum, 119.5, is within 3% of the measured 116.
  - **endpoint job:** the same model with only the three endpoint shards gives a 21.7s phase. Everything outside the phase took 69 − 31.7 = 37.3s (queue, restore 9.9s, boot to ssh 13.7s, checks, upload) and does not change. Predicted job: 37.3 + 21.7 = **59s**, from 69s.
  - **csi job:** CPU alone allows a 10.5s phase; the measured contended wall, 23.1s, is the upper bound. Predicted job: **48–60s**.
  - Push-to-green is the slower job plus about 1s: about **60s on a 70s-class runner**. Scaled by the phase ratio to a 54s-class runner (40497090324: 21.8s phase), the endpoint job is about 47s. The split alone does not reach the 45s operating margin on a slow runner; the fixed 37s is now the larger share.
- **Cost.** Two VMs boot instead of one. Runner time per push goes from 66s (one job, start to end, without the queue) to about 2 × 34.3s fixed (66 − 31.7) + 21.7s + 10.5–23.1s = 101–113s, so **+35 to +47 runner-seconds (+53% to +72%)**. Billed per job rounded up to the minute, that is 2 minutes before and 2–4 after. Each job also downloads the 899 MB guest entry, so cache egress doubles. A cold guest (no entry for the current key) is provisioned by both jobs, about 115s each; only the csi job saves it (wave 16, below). `nightly.yml` gains one VM job a night.
- **The boot (wave 16).** The guest's boot was on the endpoint job's critical path. Numbers from run 40505456077 (ab16195, warm guest and warm Go cache; the last single-VM run, same guest and boot as the split jobs):
  - timeline: test binaries built at +7.2s after qemu started, ssh up at +11.4s, so the job waited **4.2s** for the guest, then 0.5s of guest checks and 0.3s of scp before the tests;
  - `Startup finished in 1.175s (kernel) + 4.644s (initrd) + 4.352s (userspace) = 10.172s`; the directly booted initrd is 67,176 KiB (dracut, with dracut-network);
  - `critical-chain ssh.service`: ssh.service at 4.07s into userspace, behind network.target, systemd-networkd, netplan-configure, local-fs.target, zfs-mount.service and **zfs-import.target @3.439s**. The split endpoint job (run 40506940034, cold Go cache) shows the same: zfs-import.target @10.035s of 10.9s.
  - `blame`'s 15 lines were all `.device` units at 4.1–4.4s: the devices settling.
- **Levers taken:**
  - **No initrd on a warm boot** (−4.6s of boot). The kernel, 7.3.0-9-generic, has everything to mount the root itself: `CONFIG_VIRTIO_PCI=y`, `CONFIG_VIRTIO_BLK=y`, `CONFIG_EXT4_FS=y` (its `/boot/config-7.3.0-9-generic`, from linux-modules-7.3.0-9-generic 7.3.0-9.9). The stored cmdline names the root `LABEL=cloudimg-rootfs`, which only an initrd resolves; in the image's GPT that is partition 1 (`cloudimg-rootfs`, PARTUUID 48fa4c49-…; vda15 is d267c2dd-…, the UEFI partition in the log's device list). So a warm boot rewrites it to `root=/dev/vda1 rootfstype=ext4 rootwait` and passes no `-initrd`. Every such boot checks that `/` is `/dev/vda1` labelled `cloudimg-rootfs`. If the kernel cannot mount it, it panics (`panic=1`), qemu exits (`-no-reboot`), and the script boots again with the stored initrd, with a warning. `ZFSVM_INITRD=1` always uses the initrd. (Since `zfsvm-v5` the fallback boots through the BIOS and GRUB with the disk's own initrd; see the cache bytes section.)
  - **zfs-import-cache and zfs-import-scan masked on the kernel command line** (`systemd.mask=`, every direct boot). In OpenZFS 2.4 both units carry `Requires=systemd-udev-settle.service`, and zfs-mount.service orders after them and before local-fs.target. That is what held sshd until every device had settled. No pool exists at boot, because the guest's tests create their own on tmpfs. zfs-import.target, which the zfs-endpoint unit wants, is still reached. The analyze block now prints whether systemd-udev-settle ran, and `blame` without `.device` units, so the next run shows what is left on the chain.
  - **systemd-udev-settle masked too** (wave 17). With the imports masked, it still ran, 1.78s on ssh's chain, in run 40511369632: `zfs-mount.service @4.618s` ← `zfs-load-module.service @4.596s` ← `systemd-udev-settle.service @2.811s +1.781s`. zfsutils-linux 2.4.4-1ubuntu4 (the guest's) ships `zfs-load-module.service` with `Wants=` and `After=systemd-udev-settle.service`. It is a Debian unit; upstream 2.4 has none. No unit in nfs-kernel-server, nfs-common, multipath-tools, udisks2, lvm2, mdadm or open-iscsi (stonking) names it. A `Wants=` on a masked unit is no failure. zvol links come from udevd running `60-zvol.rules`, which still happens, and the endpoint waits for them with `udevadm settle` (`UdevSettle`), which asks udevd directly, not through this unit. The analyze block prints its state, who still wants it, and any failed unit.
  - **The guest checks overlap the build.** The test build runs in the background, while the script waits for ssh and runs the version, module and nfsd checks. The scp waits for the build, and a failed build still fails the job.
  - Neither boot lever changes the cache key. Both apply at boot, from the stored kernel and cmdline, so they cost no cold run. The provisioning boot is unchanged (BIOS, GRUB, initrd). The stored disk's first boot, which writes `ok`, is the no-initrd, masked boot, so the cache certifies the shape the PR jobs boot.
  - **Only the csi job saves the guest.** In run 40506940034 the endpoint job's 6s "save the provisioned guest and qemu" ran because the runner image changed (restored `…-20261023.334.3`, saving `…-20261026.354.1`, a fallback hit). It runs once per runner-image change per PR scope, not on every run. Both jobs saved the same key. The csi job's test phase is the shorter one (8.7s against 18.5s), so the save now runs only there, off push-to-green.
- **Derived saving.** ssh comes up after the build in every case, so the 4.2s wait goes, and the 0.5s of checks hide under the build:
  - pessimistic: the masks gain nothing, and loading modules moves out of the initrd into userspace (+0.5s). Then ssh is up at 11.4 − 4.6 + 0.5 = 7.3s, about the build's 7.2s.
  - expected: local-fs.target follows the root remount and fsck (about 1s), and ssh starts about 0.6s after it, as it does after zfs-import.target now. That gives 1.5 (qemu to the kernel's clock, plus the poll) + 1.2 (kernel, with no 67 MB initramfs to unpack) + about 1.6 + 0.5 ≈ 4.8s.
  - so the job saves 4.2–4.7s; on the 70s-class run 40502060857 (built +8.9s, ssh +13.7s) it saves 4.8–5.3s.
- **Predicted warm endpoint job** (run 40506940034's steps, with the cold-cache effects removed: its Go cache was cold, built at +18.6s, and the 6s guest save came from the fallback hit):
  - queue and set-up to the boot step: 16s;
  - setup-go: 3s;
  - test step: 3.9s to the build, plus 0.3s of scp, plus 18.4s of tests and checks = 22.6s (27.3s before the levers);
  - upload and post: 4s.
  - Total: 16 + 3 + 22.6 + 4 = **46s** (50s before). On a 70s-class runner: 59 − 5 = **54s**.
  - The csi job is about 10s shorter in its test phase, so it does not set the pace even when it pays the 6s save. Push-to-green is about 47s, nominal 45s not yet met.
  - The next lever is the build itself (+7.2s), which is now the critical path ahead of the tests.
- **Levers rejected:**
  - a smaller initrd (dracut `--hostonly`, fewer modules) still pays an initrd stage, and it needs a key bump (a cold run of about 115s per job). With the drivers built in, no initrd is strictly better;
  - direct kernel boot was already adopted (wave 13);
  - systemd-networkd-wait-online is only on nfs-server's chain, at +75ms; nfs-server was active 28ms after ssh;
  - cloud-init after provisioning was already off (`status: disabled`, by marker file);
  - a vsock or virtio-serial channel instead of sshd: once ssh is up before the build, waiting for sshd costs nothing, and the channel would need a guest agent and host tooling;
  - `quiet` on the console: no data that the serial console slows the boot, and it would cut the console tail that failures print.
- **The first runs must confirm the boot:** `Startup finished` without an initrd term; `ssh up` before `test binaries built`; `systemd-udev-settle: inactive, masked-runtime` and no failed units; no "booting it with the stored initrd" warning.
- **The first runs must confirm:** both jobs pass their checks, with `check-split: … top-level tests over 3 shard logs, each in exactly one`; the endpoint job's `guest load` line shows the phase near 22s; the csi job's phase is under the endpoint job's.

### The endpoint suite in two parts (three VM jobs)

- **Data.** Run 40513522394 (2b648ef). The endpoint job was the long pole, and csi finished 1s before it. Its test phase took 22.1s over 3 shards (15.6 / 22.2 / 22.5 guest-s). The guest was CPU-bound: busy 91%, sys+irq 69%, cpu some 73%. The work is about 83 CPU-s. Balancing the shards within the job does not help, because the CPU stays busy to the end.
- **Decision.** The endpoint suite runs as two jobs: matrix `include` entries `{suite: endpoint, part: 0|1, parts: 2}` and `{suite: csi, part: 0, parts: 1}`. Each job runs 3 shards of one 6-shard plan.
  - `ci/real-zfs-plan.sh` makes the plan. It is a pure function of the binary's `-test.list`, `ci/real-zfs-weights.txt` and the part count. It works on two levels:
    - first the tests go onto the parts, longest first onto the lightest, so the parts get equal test-seconds (37.09 / 37.10 at 7048b51);
    - then each part's tests go onto its 3 shards the same way;
    - TestPropertyRoundTrip runs in all 6 shards, with rows `i mod 6`;
    - the NFS tests stay together in one shard.
  - The host makes the plan from the built binary, in the background build, at about 25 ms. The guest runs only its part's shards.
  - With `parts: 1` the plan is byte-for-byte the old 3-shard guest plan, so `nightly.yml` is unchanged.
  - Both jobs restore the same guest entry. Only the csi job saves it. Only part 0 saves the shared `real-zfs-endpoint` Go namespace.
- **Exactly-once across jobs, without a job that waits for both.** A final job that compares the two jobs' uploaded lists would add a queue, a set-up and a download to push-to-green, about 8–10s. The plan is deterministic instead, and each part proves its own half in its own job:
  - **The plan is static.** `ci/check-split.sh` remakes the plan and requires the job's plan to match it byte for byte. It also proves the plan is a partition of the list over all 6 shards: every listed test in exactly one shard, TestPropertyRoundTrip in all 6, nothing unlisted. Both jobs build the same commit, so they make the same plan. Each job prints the list's and the plan's sha256, so any two can be compared. The guest's own list must equal the host's list.
  - **Each part's run is checked against the plan.** The shard logs must be exactly the part's shards. Each top-level result must appear once, in the shard the plan assigns it. Every test the plan gives those shards must report. Each `PROPRT_SHARD i/6: k of T` line must have k = #{r < T : r mod 6 = i}. The subtests must number Σk, with none twice.
  - So a test that one part drops fails that part's job, and a test that a part runs outside its shards fails that part's job. Rows `i mod 6` over i = 0..5 cover each row once, provided both parts split the same total T, which neither job sees of the other: so each shard's T must equal the committed `propRTRows` (`internal/endpoint/props_roundtrip_test.go`), which the test itself pins to `len(allowlistRows())` (3d6b248).
  - A shard the plan gives no test writes an empty log, which passes only as that: an empty shard log is expected, not a lost shard.
  - `ci/check-skips.sh` takes the part's own list. With `SKIPS_ELSEWHERE`, the other part's tests set the skip budget per job: an allowed skip planned for the other part is the other part's to meet, and the two budgets add up to the whole list (22 at 7048b51, all in part 1).
  - `ci/check-suites.sh` statically requires every part 0..P−1 of every suite exactly once, with one P per suite, and P > 1 only for endpoint.
  - Locally, against run 40513522394's shard logs and a synthetic 2×3 run built from them, the checks reject the following: a dropped test, a dropped shard log, a dropped row, a test run in both parts, a test run twice within a part, a duplicated row, a wrong `PROPRT_SHARD` split, a hand-edited plan, and a planner that drops or duplicates a test.
- **Prediction.** Each guest does half the CPU work: 41.5 CPU-s at about 3.6 effective vCPUs is 11.4s. TestRealFacts (9.5s contended) is the floor. So each endpoint job's phase is about 12s, against 22.1s, about 10s less, and the endpoint job ends at about 45s. Push-to-green is then bound by whichever VM job is slowest. The max is now over three VM jobs instead of two, which adds one more queue and runner sample.
- **Cost.**
  - Runner time per push grows by about 24s: two endpoint jobs of about 34s each (about 22.3s fixed plus 12s), against one of about 44s. Billed per job, rounded up to a minute, that is one more minute, which is free on a public repo.
  - Each push restores the guest entry three times instead of twice, which is about 590 MB more cache egress.
  - The weights still come from the uploaded `test-times.tsv`. There is one per part (`real-zfs-report-endpoint-<p>`). `ci/real-zfs-weights.sh` sums the parts of a run (the `run=` header) before it averages runs.
- **The first runs must confirm the following:**
  - both endpoint jobs print the same list and plan sha256;
  - `check-split: part p/2: … each in exactly its planned shard`;
  - each part's `guest load` phase is near 12s;
  - the csi job is now the last to finish.
- **Measured.** Each part's phase took 6.6–10.4s (runs 40536056973, 40536341101, 40536492611; 10.7s and 7.7s on the first three-job run, 40519558807). With the parts this short, no job is reliably last (csi was, on 40535710051): push-to-green is bound by the slowest of the three VM jobs in each run, whichever that is.

### Cache bytes (zfsvm-v5)

- **Data.** Every VM job restores one entry of 942,776,534 B (~899 MB). It downloaded at 60–234 MB/s depending on the host (wave 15 runs 40510207805, 40511042404, 40511369632), so it costs 4–15s on the critical path: 1s per 130 MB at nominal speed, 1s per 60 MB on a bad day. The disk is already a zstd-compressed qcow2 (`qemu-img convert -c -o compression_type=zstd`), so a higher actions/cache zstd level gains almost nothing. Only fewer bytes help. The cold run 40500713922 (job 122177403029) stored `zfsvm-cache` at 885 MiB (`du`):
  - the disk, about 844 MB;
  - the initrd beside it, 67,176 KiB (68.8 MB, dracut, zstd inside). It is a second copy: the disk's `/boot` already holds it;
  - the kernel (~15 MB) and the cmdline;
  - the qemu tree, 25.2 MB after `tar | zstd -3`.
- **Decision.** The key moves to `zfsvm-v5`, and the entry gets smaller:
  - **No initrd beside the disk** (−68.8 MB). Warm boots run without one (wave 16). The panic fallback now boots the same overlay through the BIOS and GRUB, which reads the kernel and initrd from the disk's own `/boot` (kept, `noauto` in fstab). This is slower (3.5–5.8s more to the kernel), but it runs only after a panic, with a warning. A second, rarely restored entry for the initrd was rejected: the fallback happens inside the test step, where no cache step can run, and the disk already holds the file.
  - **A trimmed qemu tree** (`ci/qemu-tree.sh`, 25.2 → 8.4 MB measured on noble's qemu 8.2.2): only `qemu-system-x86_64` and `qemu-img`, the x86 firmware (SeaBIOS, linuxboot, kvmvapic, the virtio option ROMs), no qemu-system-i386 or -microvm (33 MB uncompressed), no other targets' firmware, keymaps, docs or man pages. `verify()` still checks the firmware a boot loads.
  - **A slimmer guest** (`ci/real-zfs-cloud-config.yaml`, provisioning only), each item estimated from stonking's own packages and the cloud image manifest (674 packages, 1.74 GB installed). The qcow2 cost was measured by compressing the extracted packages in 64 KiB clusters with zstd -3, as qemu stores them:
    - purge bpftrace and bcc with libllvm22, libclang-cpp22 and libclang1-22, the kernel headers, linux-tools and perf: 426 MB installed, about 130 MB in the qcow2 (124.1 MB measured for the six largest packages). The purge is skipped whole if apt would also remove a package the guest needs (sshd, sudo, systemd, zfsutils, nfs, the kernel and its modules);
    - remove the kernel modules of hardware a virtio guest does not have (GPU, media, sound, wireless and wired NICs, staging, comedi, iio): 80.4 MB, already zstd, so they are stored at about full size. virtio-net, virtio-blk, ext4 and loop are built in, and xfs, nfsd and zfs are kept;
    - docs, man pages, non-English translations (3,922, 7,741 and 1,634 files): about 35 MB, the least certain estimate;
    - apt's lists, 117 MB of indexes: about 26 MB;
    - the provisioning journal, then `fstrim` as before, so that freed clusters leave the qcow2.
  - **Predicted entry: about 586 MB** (942.8 − 68.8 − 16.8 − 130 − 80 − 35 − 26), likely 570–630 MB, since freed blocks that share a 64 KiB cluster with live data stay allocated. The first cold run logs the stored size (`stored provisioned guest in … (…, …s)`), and every warm run logs `Cache Size`.
  - **Saving: about 357 MB per restore.** That is 2.7s at 130 MB/s and 5.9s at 60 MB/s, plus about 0.5s less unpacking (1.3s for 899 MB in run 40511042404).
- **Cost.** One cold run: each VM job provisions once, about 115s per job, on the first push after the change and then each ISO week as before. `cache-warm.yml` provisions the default branch's entry, and its cleanup step deletes the superseded v4 entries (all `zfsvm-` keys but the current one). Provisioning gains the purge, `depmod` and the deletions (a few seconds, cold only).
- **Rejected:**
  - two entries, qemu and the guest, restored in parallel: the trimmed qemu tree is 8.4 MB (0.06s at 130 MB/s), and a second restore costs about 2s of overhead (run 40494213342);
  - a higher actions/cache compression level: the qcow2 clusters are already zstd;
  - pruning more modules, or purging more packages (python3-botocore, which sos needs; vim; git): the guard and the checks would catch a removal that breaks the boot, but not a module or a tool that a single test needs. The listed removals are hardware the guest does not have and tools no test runs;
  - zero-filling the free space instead of `fstrim`: the disk is 20G, and writing it would add a cold-run cost for a few MB.
- **The first cold run must confirm:** `cloud-init status` without errors, no `slim: not purging` line and the `df` of `/` before the store; the checks and both suites pass on the slimmed guest; the stored size near the prediction. Warm runs must show `Cache Size` near 590 MB, no `booting it through the BIOS and GRUB` warning, and `direct kernel boot: … no initrd`.

### Where the endpoint phase's CPU goes

- **Data.** Run 2026-10-27T13:36 (the unsplit endpoint suite, 3 shards): the test phase took 22.8s on 4 vCPUs at 91% busy, about 83 CPU-s, of which about 57 are sys+irq. There were about 28k context switches/s and 12k interrupts/s, and `bi` ran at 4–10 MB/s, which the tmpfs vdevs cannot cause (vdev_file I/O never reaches submit_bio). The workload is about 10k zfs(8) launches, roughly 8 ms of CPU each. Each launch posts history zevents, which zed fans out to zedlets, and each zvol add/change/remove is a uevent that udev and udisks2 probe. The split of those CPU-s by source was unknown.
- **Counters (diagnostics only, `counters` in `ci/real-zfs-guest.sh`).** Before and after the shards, one awk reads `/proc/stat` (busy ticks, `processes` = forks, `ctxt`, `intr`), `/proc/interrupts` (the total, and LOC, RES, CAL and TLB), `/proc/vmstat` (pgpgin, pgfault, pgmajfault), vda's sectors read from `/proc/diskstats`, and cgroup v2 `cpu.stat` (`usage_usec`, `system_usec`) of the root, `init.scope`, `system.slice`, the session scope the shards run in (`tests`: the test binaries and their zfs(8)), and `zfs-zed`, `udisks2`, `systemd-udevd`, `systemd-journald` and `rsyslog`. The deltas are appended to `out/load.txt`, so to the job summary: forks/s and CPU-ms per fork, context switches/s, interrupts, block reads split into vda and the rest (zvols, which leave diskstats once destroyed), and CPU-s per cgroup.
  - Only files that exist are read. A stopped unit or a missing cgroup prints `-`, and nothing in it can fail the job. Measured on a 4-CPU container: 4 ms per snapshot, so well under 0.1s for the job.
  - Kernel threads and irq are root − (init + system + user.slice). The table prints root, init and system; the tests' scope is most of user.slice.
- **Measured (run 40536056973, endpoint part 0).** Phase 10.4s at 93% busy, about 39 CPU-s: 5886 forks at 6.6 CPU-ms each, so the phase is zfs(8) launches. zfs-zed used 3.62 CPU-s, udisks2 0.44, systemd-udevd 3.05 (reading 82 MB from new zvols). The lever is fewer launches from the tests, not the guest's daemons.
- **Quiet guest: tried and reverted.** Stopping zfs-zed and masking udisks2 for the tests (9a8b891) removed about 900 forks per part, but the phase measured 8.5s with it (run 40536492611) against 6.6s and 10.4s without it (runs 40536341101, 40536056973): the 1.3-4 CPU-s it saves are inside the run-to-run spread, under the keep rule 9a8b891 recorded with it (a drop of >= 1.5s, or >= 6 CPU-s in those units). zed and udisks2 stay as the image ships them.

### ssh poll and module load

- **Data.** Run 40513522394 (2b648ef, jobs 122221249079 endpoint and 122221249080 csi), times after qemu started:
  - endpoint: test binaries built at +5.75s, `ssh up` at +9.22s, `nfs-server active` at +9.25s. In the guest, ssh.service was active 5.4s after the kernel's clock started (1.20s kernel + 4.19s userspace), about +6.2s on the host clock;
  - csi: built at +7.87s, `ssh up` at +15.77s. ssh.service was active at about 8.7s of uptime (+9.5s);
  - so the host saw sshd 3s (endpoint) and 6s (csi) after it started. This was first blamed on the poll (slirp's hostfwd losing its SYN to the guest until the guest has its address, each 5s ssh try stalling). That theory was wrong; see the next bullet;
  - **the cause was OpenSSH's PerSourcePenalties** (OpenSSH ≥ 9.8; the guest runs 10.5). sshd refuses logins while /run/nologin exists (pam_nologin, `account required` in Ubuntu's sshd PAM stack, until systemd-user-sessions removes it), and charges each refused login as authfail (5s). Past 15s of charge it refuses every connection from the source ("Not allowed at this time") until the charge expires, and all host traffic reaches the guest from one address (slirp's 10.0.2.2). With 0.3s banner probes (run 40519558807) the host saw sshd 18–19s late; with 1s probes (run 40535710051, d0aae91) still 16–19s, with 8 penalised drops per boot. The banner probe bounds were never the lever;
  - nfs-server is now 2.2s behind ssh on the endpoint guest (rpc-statd after network-online.target, systemd-networkd-wait-online +2.0s). The wave 16 note that wait-online was at +75ms no longer holds;
  - on ssh's chain: `systemd-modules-load.service +978ms` (endpoint, sysinit.target) and `+2.600s` (csi), which loads zfs from `/etc/modules-load.d/zfs.conf`. On csi, `zfs-load-module.service +1.050s` held local-fs.target, and so ssh and nfs-server. `systemd-ask-password-console.path`, on both chains, started as modules-load finished on both guests (@3.076s after +978ms ending at 3.057s; @4.601s after 2.6s). This suggests, but does not prove, that it waits for it;
  - `apparmor.service +531ms` was on the endpoint's sysinit chain.
- **Levers taken:**
  - **Logins wait for the serial getty's login prompt** (6629b7e; `wait_ssh` in `ci/real-zfs-vm.sh`, the `login prompt` mark). getty starts after systemd-user-sessions, so once the console log shows ` login:`, /run/nologin is gone and the first login is not refused. Then each probe reads the banner with a 1s bound (`banner`, a bash `/dev/tcp` read) and requires `SSH-`, because a penalised source reads `Not `; only then does ssh log in and open the master, with its full ConnectTimeout. With no prompt in 60s (a console that prints none), it polls as before. The gate is re-armed after a GRUB reboot. The deadline is explicit: 600s. Before, it was 900 polls, 3 to 78 minutes depending on how they failed. The panic fallback is unchanged: qemu exits, and the loop boots again through GRUB. (An earlier version read the banner with a 0.3s bound, then 1s (d0aae91); each abandoned probe was itself charged as noauth.)
  - **systemd-modules-load, zfs-load-module and zfs-mount masked** on the kernel command line (direct boots only, no key change). zfs is loaded explicitly by `modprobe zfs` after ssh, in the background over the master, beside the checks that do not need it. Its time is logged (`modprobe zfs: …ms`). The version check (`zfs-kmod-2.4.x`) then gates on it, as before.
    - zfs-mount: no pool exists at boot, so `zfs mount -a` had nothing to do. Its upstream unit has `ConditionPathIsDirectory=/sys/module/zfs` anyway.
    - zfs-zed has the same condition, so it skips its boot start without the module. It is started (`--no-block`) after the modprobe, so it runs during the tests as before.
    - zfs-share has the same condition and only orders before nfs-server.
    - The zfs-endpoint unit (`Wants=zfs-mount.service`) is fine: a `Wants=` on a masked unit is no failure. It starts after the modprobe, so libzfs never needs `@module`.
    - nfsd is not in modules-load.d. Mounting `/proc/fs/nfsd` loads it, as before, and the nfsd-threads check gates it.
    - A GRUB boot (cold run, panic fallback) masks nothing.
  - **The nfs-server wait moved last** in the guest checks. The checks (cloud-init, build.info, rootfs, exportfs, modprobe, version) now run while nfs-server starts, instead of after it.
- **Levers rejected:**
  - **masking apparmor** (0.53s on the endpoint's sysinit chain). Once ssh is seen at once, nfs-server's chain (local-fs, networkd, wait-online) binds, not sysinit. So it would buy nothing. It would also change what is confined during the tests (no profiles loaded), which cannot be justified for no gain;
  - **a smaller `ConnectTimeout`** alone: it is whole seconds (1s at least), and a 1s login bound risks failing a real handshake on a busy guest.
- **Measured** (the poll's predictions from run 40513522394 assumed the slirp theory and are superseded):
  - run 40536341101 (6629b7e): `ssh up (1 probes)` about 0.6s after `login prompt`, at +10.1s; push-to-green **36s**, the first run under the 45s margin. Later runs took 43–57s (for example 40537085486 at 43s, 40537458909 at 51s), which is the run-to-run spread of hosted runners, not a regression of the gate.
  - push-to-green is bound by the slowest of the three VM jobs (see the two-part section).
  - The masks' prediction is separate and unaffected: they remove modules-load (and on csi zfs-load-module, +1.05s) from ssh's chain, up to −3.6s if modules-load gated local-fs.
- **The first runs must confirm:**
  - `login prompt`, then `ssh up (1 probes)`; the `sshd penalties:` line prints the setting, and the ssh journal block has no `drop connection … penalty` lines;
  - `modprobe zfs: …ms, exit 0` and `zfs loaded` before `nfs-server active`;
  - `systemd-modules-load: inactive, masked-runtime; zfs-load-module: inactive, masked-runtime; zfs-mount: inactive, masked-runtime; zfs-zed: active, enabled`;
  - the `modules-load.d` line lists only `zfs`, or nothing a test needs;
  - no failed units, and both suites and the unit check pass.
  - If modules-load turns out not to gate the path unit, local-fs.target and nfs-server do not move, and the masks gain only csi's 1.05s on the poll's ready time.
- **Guest kernel: `mitigations=off init_on_alloc=0`** (direct boots, appended after the masks; a separate commit so it can be reverted alone). The endpoint test phase is fork/exec- and syscall-bound: about 57 of 83 CPU-s are sys+irq, with about 28k context switches/s (run 40513522394). Mitigations add work to every syscall and context switch, and nested KVM makes that work more expensive. `init_on_alloc=1` (Ubuntu's default) zeroes every page allocated on fork/exec and by ZFS.
  - The guest is throwaway and single-tenant, and the ZFS code paths do not change.
  - Predicted saving: 1.5–3s on the endpoint phase. This is unmeasured.
  - The summary prints the guest's `/proc/cmdline` and `/sys/devices/system/cpu/vulnerabilities/*` once (`guest CPU mitigations: N of M vulnerable`).
  - The rule was to adopt it if the endpoint phase dropped by at least 1s, else revert it. No A/B run was made, and the phase's run-to-run spread (6.6–10.4s) is wider than the predicted saving, so the outcome is unknown: it is **kept, unmeasured**.

## Consequences

- Test code is held to the same data-oriented shape as the product: rows and plans as data, with exec only at the edges.
- A cached guest is one more artifact to keep honest. The version assert runs inside every boot, so a guest that has left 2.4.x fails loudly rather than testing the wrong ZFS. A guest that is stale but still on 2.4.x passes by design. That staleness is bounded by the ISO week in the cache key, and the job summary reports it (image serial, kernel and `zfs version`).
- Long fuzzing no longer gates a PR. Its findings arrive by the next morning as issues. Once a human commits them as corpus entries, they gate every PR.
- Tests that need wall-clock behaviour (real lock contention, nfsd start-up) should declare their time cost as data, next to `ci/expected-skips.txt`, so the budget is visible per test.
- Note (zfsdiff replay): the real-pool replay of the whole zfs(8) corpus (102 sequences, ~1,270 zfs invocations, estimated 15-30s) would at least double the slowest VM shard, so it runs nightly (`real-zfs-full`). The PR tier keeps it as measurement, not a skip: model vs simulator over the whole corpus with no pool, and a real-pool replay of `zfsdiff.PRSample` (21 sequences, 253 invocations, failing past 15s). On the real pool it measured 9.9s and 9.0s (runs 40469622947 and 40471254688) against an estimated 5s, and it is weighed at 9.5s now. A runner 1.5x slower brings it close to the 15s limit; if it nears that limit, the sample shrinks (it stays above zero sequences on the real pool) rather than another check being cut.
- If the budget cannot be met without cutting a check listed above, the check stays and this ADR is revisited. The budget does not outrank validity.
