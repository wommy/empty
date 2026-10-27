# Real-ZFS VM job: plan to ~35s, measured where possible

Branch: local `speed-vm` at `e0ee9f5`, one commit on top of c73e40d (the coordinator's provisioned-guest cache). It has not been pushed. There is also a local `speed-vm-prebake` (`a4ed49a` + 1), an alternative design kept for reference.

Patches (in `wave10/vm-patches/`):
- `0001-ci-real-ZFS-VM-parallel-guest-shards-cached-qemu-tre.patch`: `git am` onto c73e40d. This is the recommended change.
- `alt-A-prebaked-self-running-guest.patch`: a diff against 8cc52ef. This is the more radical design (§7). It is not recommended now.

**Caveat on evidence.** This sandbox has no `/dev/kvm`, so nothing here was booted under KVM. Boot and test times come from the timestamps of runs 40456494050 (8cc52ef) and 40459452633 (c73e40d, a cold guest), plus local measurements of everything that does not need KVM (listed in §9). Each "target" number below is marked *measured* or *estimate*. The first warm run of this patch will settle the estimates. §10 lists what to read off that run.

## 1. Where the time goes today

**Run 40456494050** (8cc52ef). The job ran 1:51 from created to completed. The test step took 81s:

| t (s) | phase |
|---|---|
| 0.0–0.8 | SHA256SUMS + gpgv |
| 0.8–7.0 | image download + sha256 |
| 7.0–12.1 | `qemu-img resize` (5s, oddly slow) |
| 12.1–59.2 | boot + cloud-init apt (zfsutils, nfs). The compile overlaps this |
| 59.2–60.8 | 9 ssh round trips (version, serial, uname, exportfs, nfsd) |
| 60.8–61.5 | scp + zpool create |
| 61.5–70.8 | endpoint.test, serial, 9.25s |
| 70.8–76.3 | csi.test mount, 5.5s (sanity 4.5s) |
| 76.3–80.8 | csi.test block, 4.5s |
| 80.8–81.6 | scp logs + check-skips |

**Run 40459452633** (c73e40d, cold guest). The VM job took 1:55, against about 20s for the other jobs, so the VM job sets the pace.

| step | s | |
|---|---|---|
| setup-go (composite, pinned tarball) | 4 | measured |
| install qemu (apt-get update + install) | 13 | measured |
| resolve image | 1 | measured |
| test step (cold: provisioning, store, then **a warm overlay boot**) | 86 | measured |
| ↳ store: poweroff + convert to a 2.3G plain qcow2 | 7 | measured |
| ↳ **warm boot of the stored disk → `status: done`** | **13.5** | measured (06:11:06.2 → 06:11:19.8) |
| ↳ checks (version / module / nfsd) | 1.4 | measured |
| ↳ tests, serial: 6.4 + 3.7 + 3.4 | 13.5 | measured |
| save guest cache: archive of 838MB | 7 | measured |

The c73e40d warm path has not run yet. Projecting it from the measured pieces gives about **62s from push**: queue 3, setup 2, setup-go 4, qemu 13, resolve 1, restore about 8 (estimate), gpg 0.6, boot 13.5, checks 1.4, scp 0.7, tests 13.5, check 0.7, post 1. That is over the 60s budget even when warm.

## 2. Target timeline (speed-vm, warm guest, warm qemu tree)

| step | c73e40d warm | **speed-vm warm** | basis |
|---|---|---|---|
| queue (created→started) | 3 | 3 | measured |
| set up job + checkout | 2 | 2 | measured |
| setup-go | 4 | 4 | measured |
| install qemu | 13 | 0 | the step is removed |
| resolve image | 1 | 1 | measured |
| restore provisioned guest | ~8 | ~6 | estimate. The archive is 751MB plain vs 808MB zstd-qcow2. Unpacking writes 2.2G vs 0.8G (22s vs 9s on this slow disk) |
| restore + verify qemu tree (93MB) | – | ~1.5 | verify 0.4s measured. Restore is an estimate at the 87–96MB/s seen for the Go cache |
| gpgv SHA256SUMS | 0.6 | 0.6 | measured |
| warm boot → sshd → nfs-server active (compile overlapped) | 13.5 | **~7** | 13.5 measured. 7 is an estimate: cloud-init disabled, snapd purged, timers masked, wait for nfs-server only, ssh poll 0.2s |
| version / module / nfsd checks | 1.4 | ~0.4 | 9 ssh calls over one ControlMaster connection (estimate) |
| scp binaries + script | 0.7 | 0.7 | measured |
| tests | 13.5 | **~5** | serial measured. The longest of six shards is csi-sanity at 3.3–4.5s measured, plus startup and contention on 4 vCPUs (estimate) |
| scp logs + check-skips | 0.7 | 0.8 | measured |
| post / complete | 1 | 1 | measured |
| **total from push** | **~62** | **~33** | |

That leaves about 12s of headroom to the ~45s goal and about 27s to the ADR's 60s, before any stretch item.

**Cold runs.** A cold guest (new image serial, or a new cloud-config) still costs about 115s. A cold qemu tree (new runner image, about weekly) adds about 13s once. See §5 for keeping PRs off the cold path.

## 3. Changes ranked by seconds saved per unit of risk

Implemented in 0001 (risk is 1 = low to 3 = high):

| # | change | saves (warm) | risk | why the risk is what it is |
|---|---|---|---|---|
| 1 | **Cached qemu tree** (`ci/qemu-tree.sh`). It does a `dpkg-deb -x` of exactly the packages `apt-get install -s` would add on this runner image, then runs them through wrappers that set `LD_LIBRARY_PATH` and `-L` for seabios, qemu and ipxe. The tree is cached per `ImageOS-ImageVersion`, with apt as the fallback | **~12s** (13 → ~1.5) | 1 | Built locally on Ubuntu 24.04.5 and booted under TCG: SeaBIOS, SeaVGABIOS and both virtio option ROMs ran. A tree that does not run here fails `--version` or the firmware check, and the step installs from apt with a warning |
| 2 | **Parallel shards in the guest** (`ci/real-zfs-guest.sh`). endpoint ×3, split by the measured weights in `ci/real-zfs-weights.txt` (longest-first onto the lightest shard, file-export tests kept in one shard). csi non-sanity, csi-sanity mount and csi-sanity block run at the same time. Six pools, each on a sparse 10G file in a guest tmpfs. `-smp nproc -m 6144` | **~8.5s** (13.5 → ~5) | 1–2 | Coverage is enforced on the merged logs. Every top-level Test/Fuzz/Example must PASS or SKIP. The new risk is interference between concurrent processes: one nfsd, the `spa_namespace_lock`. Each shard owns its pool, and the file-export tests share one shard |
| 3 | **Leaner warm boot.** Provisioning writes `modules-load.d/zfs.conf`, purges snapd and unattended-upgrades, masks the apt, motd, man-db and fwupd timers and multipathd, and touches `cloud-init.disabled`. The script waits for `nfs-server` instead of `is-system-running --wait`. ssh uses one ControlMaster connection and polls every 0.2s | **~7s** (13.5+1.4 → ~7.4) | 2 | It changes what boots. Every check still runs on every boot: zfs userland and kmod, nfsd threads, exportfs |
| 4 | **Guest cache keyed by `ci/real-zfs-cloud-config.yaml`**, not the whole script (provisioning moved into that file) | 0 per warm run, but it avoids a ~115s cold run every time the test runner changes, including on this very patch | 1 | A provisioning change outside the YAML (for example the store step) needs the `v2` bump in the key. That is noted in the workflow |
| 5 | **Cacheable only after a verified boot.** `ok` is written only after a boot from the stored disk passes the version, kmod and nfsd checks. Before, it was written before that boot, so an unbootable disk could be saved | 0 | 1 | Pure robustness |
| 6 | **Trimmed and zstd-compressed stored disk** (`fstrim`, `discard=unmap`, `qemu-img convert -m 16 -W -c -o compression_type=zstd`) | ~1–2s | 1 | Measured on the stonking base image: plain 2177MB → archive 751MB, unpack 22.2s; zstd 817MB → archive 808MB, unpack 9.1s; convert 33.5s vs 32s (with `-m16 -W`; without them it took 114.6s). The gain is the write volume on restore, not the download. Check this on the first warm run, and revert if the restore is not faster |
| 7 | `cache=unsafe` on the guest disk | <1s | 1 | Flushes only reach the host page cache, and the disk is thrown away after the run. The cold store happens after a clean poweroff |

Stretch items, not in 0001, in order:

| # | change | saves | risk | note |
|---|---|---|---|---|
| S1 | Split `real-zfs-vm.sh` into `boot` / `build+run` steps. Start the guest before setup-go, so setup-go (4s) and the compile overlap the boot | ~3–4s | 2 | The same pattern as alt A's `boot` / `build` / `wait`. Background qemu survives between steps (GitHub kills orphans only at job end) |
| S2 | Fold "resolve image" into the script's own SHA256SUMS fetch: restore with a key prefix, then check `cache/sum` | ~1s | 2 | The script already compares `cache/sum` with the signed list |
| S3 | Direct kernel boot (`-kernel/-initrd` copied out at provisioning) | ~1–2s | 2 | Skips SeaBIOS and GRUB. The kernel must match the disk's `/lib/modules`, which the version and kmod checks catch |
| S4 | Alt A: a prebaked bundle (disk + kernel + qemu) baked nightly. The guest runs the tests itself from a 9p share, with no ssh, and the host waits for `out/rc` | ~4–6s over 0001 | 3 | §7. It is a big change. Adopt it only if 0001 plus S1–S3 are not enough |

Evaluated and rejected:
- **loadvm / savevm snapshot of a booted guest.** Hosted runners mix Intel and AMD SKUs, and state saved under `-cpu host` does not restore across them. The RAM image (≥300MB) would also ride in the cache, so its download eats the saving. qemu must match exactly. ADR 0005 wants the assert "inside every boot", and a resume is not a boot. Once S1 is in, the boot sits mostly off the critical path, so the realistic gain is ≤2s for high risk.
- **Splitting the VM tests over 2–3 jobs.** Each job pays a fixed ~12–15s: setup, checkout, setup-go, guest restore, boot. After #2 the test phase is about 5s, so a split makes the slowest job longer.
- **Cross-compiling in another job and passing an artifact.** `needs:` serialises: about 15s of build job, then artifact upload and download (2–4s). Today the build overlaps the boot and costs nothing on the critical path. Measured locally: a warm-cache build with no change takes 1.7s, and a touched package 7–10s on this slow 4-vCPU sandbox.
- **virtiofs.** It needs the separate `virtiofsd` package and a memfd memory backend. scp moves 38MB in about 0.7s. Alt A uses 9p, which is built into qemu.
- **`-test.parallel` / `t.Parallel`.** It is a code change across the test suite, which the ADR has for later. The process shards in #2 get the VM gain now. Each test already owns its parent dataset, so t.Parallel can follow without harness changes.
- **Sleeps in the nfsd checks.** The checks have none. The waste was the 5s, then 1s, ssh and cloud-init polls, now 0.2s.

## 4. The diffs

`vm-patches/0001-…patch`, against c73e40d:
- `.github/workflows/ci.yml`:
  - drops "install qemu";
  - adds "restore qemu", "set up qemu" (verify, build or apt fallback), "save qemu", and "install cloud-image-utils (cold guest only)";
  - the guest key becomes `zfsvm-v2-stonking-<image sha256>-<hash(ci/real-zfs-cloud-config.yaml)>`;
  - adds the qemu key `qemu-tree-$ImageOS-$ImageVersion-<hash(ci/qemu-tree.sh)>`.
- `ci/real-zfs-vm.sh`:
  - user-data now comes from the YAML template;
  - `-smp nproc -m 6144 discard=unmap cache=unsafe`;
  - ControlMaster ssh with a 0.2s poll;
  - the cold run fstrims and stores a zstd qcow2, and writes `ok` only after the verified boot;
  - waits for nfs-server instead of `is-system-running`;
  - the test block becomes `sudo bash real-zfs-guest.sh`, then scp of `out/`, a merge, and the same three `check-skips.sh` calls;
  - per-shard logs print in collapsed `::group::` sections;
  - the EXIT trap no longer turns an already-dead qemu into a failure.
- New files:
  - `ci/qemu-tree.sh`;
  - `ci/real-zfs-cloud-config.yaml`;
  - `ci/real-zfs-guest.sh`;
  - `ci/real-zfs-weights.txt`: the per-test cost as data, from run 40456494050, as the ADR asks.

Lint: `actionlint` is clean. `shellcheck -S warning` is clean on all scripts.

## 5. Cache keys and invalidation

| cache | key | what changes it | written by | miss cost |
|---|---|---|---|---|
| provisioned guest (c73e40d, amended) | `zfsvm-v2-stonking-<sha256 of the gpg-verified image>-<hash(ci/real-zfs-cloud-config.yaml)>` | a new upstream image serial; a cloud-config edit; a bump of `v2` when the store format changes | the first run that misses it, only after a verified boot | ~115s once |
| qemu tree | `qemu-tree-<ImageOS>-<ImageVersion>-<hash(ci/qemu-tree.sh)>` | a runner image release (about weekly); a script edit | the first run that misses it | ~13s once |
| Go (unchanged) | `go1.27.3-real-zfs-<hash(go.sum)>` | go.sum | the first miss | – |

Points to fix around the patch, recommended for the coordinator:
1. **Cache scope.** Caches saved by a `pull_request` run are scoped to that PR's merge ref. Other PRs cannot read them, and this repository's default branch is not `main`. So **every new PR pays the cold run (~115s)** unless something on the default branch saves first. Add a scheduled job (nightly and on push to the default branch) that runs the real-zfs job's cache steps with no tests, or the whole job. Its saves go to default-branch scope, which every PR reads.
2. **Staleness within 2.4.x.** The key follows the image sha256, so a new daily image reprovisions, and any SRU in the image comes with it. `apt` at provisioning time also picks up zfsutils SRUs published *after* that image. Those do not change the key, so a guest can lag the archive until the next image serial. Bound it by adding the ISO week to the key (`$(date +%G-W%V)`), or let the nightly job from point 1 reprovision. A lag inside 2.4.x is not a validity hole, because the assert still proves 2.4 kmod and userland. But the summary should show provisioning age: the cache directory could carry `provisioned_at`.
3. Do not key on `hashFiles('ci/real-zfs-vm.sh')`, as c73e40d did. Every edit to how the tests run would cost a cold run. 0001 changes this.

## 6. What still proves real OpenZFS 2.4 (validity)

Nothing in the cache is trusted. On every boot, warm or cold, and unchanged from c73e40d:
- `zfs version` must print `zfs-2.4.*` and `zfs-kmod-2.4.*`, and userland must equal kmod. The kmod line comes from the loaded module, so a missing module fails here. Alt A also compares it with `/sys/module/zfs/version` and with the baked kernel name. That check is cheap and could be ported to 0001 as one more ssh line.
- `exportfs` must be present and `/proc/fs/nfsd/threads` must be above 0.
- The image serial, sha256, kernel and zfs versions go to the step summary.
- `ZFS_REQUIRE_REAL=1` and `ZFS_TEST_POOL` are set for every shard, so a binary cannot fall back to the simulator.
- `ci/check-skips.sh` runs unchanged on:
  - endpoint: the union of the three shard logs, which must cover `endpoint.list`;
  - csi-mount: non-sanity plus sanity-mount, which must cover `csi.list`, with a floor of 44 specs;
  - csi-block: floor of 44.
  - Every allowed skip must still skip (the simulator-fallback detector), and no unlisted skip is allowed.
- `zpool destroy` of each pool at the end still fails the run if a test left a pool busy.

The coverage check caught a real bug while I prototyped. My first sharding sent only `^Test` names to shards, so `FuzzCreateConverges` and `FuzzReconcileSafe` (their seed corpora) silently stopped running. `check-skips.sh` failed with "never reported PASS or SKIP". The shard now takes Test, Fuzz and Example, as check-skips counts them. To verify, I replayed the run's real logs into the new merge path, and ran the guest script against the simulator with stubs (§9).

The pools moved from a file on the guest's ext4 to a file on a guest tmpfs. ZFS still sees a file vdev, and the kmod, userland, txg sync and ZIL are the same real code. Only the medium under the vdev changed, and the tests never measured that medium.

## 7. Alternative A (`speed-vm-prebake`): a prebaked, self-running guest

This was written before c73e40d landed. It is kept as a patch:
- `ci/vm/bake.sh` builds a bundle: a zstd qcow2 with packages, cloud-init off and a baked `zfsci.service`, plus vmlinuz, initrd, cmdline, provenance, and an extracted qemu. A nightly `vm-image.yml` rebakes it, and the cache key is a hash of the bake inputs.
- The PR job boots the bundle in the background (direct kernel boot, 9p share, no ssh), fetches Go and builds while it boots, and touches `ready`.
- The guest runs the same sharded tests and writes `out/`. The host asserts:
  - zfs userland = kmod = `/sys/module/zfs/version`, all 2.4;
  - the booted kernel is the baked one;
  - nfsd threads above 0;
  - the bundle's age: a warning at 7 days, an error at 30.
- I dry-ran it with replayed logs. The good case passes. A 2.3 kmod, a missing test and a stale kernel each fail with a specific `::error::`.
- It saves a few more seconds than 0001: no ssh, no BIOS/GRUB, boot overlapped with setup-go. But it has more moving parts, and it duplicates what c73e40d now does. Not recommended unless 0001 plus S1–S3 miss the target.

## 8. Failure modes

| failure | what happens | mitigation |
|---|---|---|
| Stale guest, still 2.4.x | Tests run on an older 2.4 SRU, and the summary shows the serial | Week-in-key or nightly reprovision (§5.2) |
| Upstream moves 26.10 to non-2.4, or the kmod and userland split | Version assert fails loudly, and the cold run never marks the disk cacheable | This is the intended behaviour |
| Stored disk cannot boot (cloud-init off but netplan missing, snapd purge hangs) | Before: saved anyway. Now: no `ok`, so nothing is saved, the run fails, and the next run is cold again | Fix the cloud-config. Purge and mask only what exists |
| PR-scoped cache | Each new PR runs cold (~115s) | Default-branch warm job (§5.1) |
| Runner image update | qemu-tree key misses: about 13s once, then saved | Automatic |
| Extracted qemu misses a library or firmware on a new image | `qemu-tree.sh` verify fails, apt fallback with a `::warning::` | Automatic |
| tmpfs fills (a test writes more than about 3.6G) | Pool suspends, the shard hits `-test.timeout=4m` and the run fails. It does not pass silently | Raise `-m` or the tmpfs size, or move vdevs to the disk |
| A shard drops a test (regex or list bug) | check-skips: "never reported PASS or SKIP" | Shown working in §6 |
| Concurrency flake (nfsd, exportfs, global locks) | It shows as a FAIL, never a SKIP | File-export tests share one shard. If flakes appear, pin more groups, or run csi-block after csi-mount |
| Weights go stale | Only the balance suffers, never the coverage | Refresh `ci/real-zfs-weights.txt` from a run's durations |
| ControlMaster socket left behind | The trap runs `ssh -O exit`. A leaked master is killed at job end | – |

## 9. Measured locally (no KVM)

- **qemu tree:** built from apt in 34–39s (this sandbox's mirror is slow; the runner's apt install is 13s). 93MB. A warm `verify` takes 0.4s. TCG boot to SeaBIOS, SeaVGABIOS and both option ROMs works through the wrappers. Found and fixed during the work:
  - `libfuse3` and `libbrlapi` live under `/lib/x86_64-linux-gnu`;
  - firmware is spread over `usr/share/{qemu,seabios}` and `usr/lib/ipxe/qemu`.
- **Guest disk formats** (unprovisioned stonking image, 836MiB zlib qcow2):

  | format | file | archive | convert | unpack |
  |---|---|---|---|---|
  | plain | 2177MB | 751MB | 33.5s | 22.2s |
  | zstd qcow2 (`-m16 -W`) | 817MB | 808MB | 32s | 9.1s |

  Without `-m16 -W` the zstd convert took 114.6s.
- **Go test binaries:** a cold cache took 278s here (sequential) and 308s (one invocation). Warm with no change took 1.7s; a touched endpoint file 7.1–10.4s. The runner is much faster, but it shows the Go cache must stay warm, which c73e40d does.
- **Sharding** on the real `-test.list` (109 endpoint tests): 3 shards of 3.07s weight each, with the file-export tests in one shard. Run on the simulator, the union of the shard logs covers the list.
- **Merge path:** I replayed run 40456494050's real guest output through the new merge and `check-skips.sh` calls. The good case exits 0. The bad cases (2.3 kmod, a missing test, a stale kernel in alt A) fail with specific errors.

## 10. Read off the first warm run of 0001

- "restore the provisioned guest" (zstd qcow2 vs the earlier plain archive): keep or revert #6.
- `qemu.pid` to `nfs-server active`: the warm boot target is about 7s. If it is over about 9s, do S3 and S1.
- `shards.txt` in the log: per-shard start and end. If csi-block or an endpoint shard dominates, rebalance with `ep_shards=4` or by moving csi-other into an endpoint shard.
- Job total from push. If it is still over about 40s, do S1, which is the next cheapest.

## 11. ADR 0005: places to change

- **"keyed by a hash of its build script and rebuilt when that hash changes":**
  - Key on the *provisioning inputs* (the cloud-config) plus the upstream image sha256, not the script that runs the tests.
  - Add a time bound (week in key or nightly) for SRUs published after the image.
- **"The version assert runs inside every boot, so a stale image fails loudly":** this is true only when staleness leaves 2.4.x. A stale 2.4.x passes, by design. The ADR should say that staleness inside 2.4.x is bounded by the time bound above and reported in the step summary, not caught by the assert.
- **The 60s budget and cold caches:** state that the budget is for warm caches, and that a default-branch warm job is what keeps PRs warm. PR-scoped caches do not help other PRs. A cold guest is about 115s.
- **"t.Parallel by default ... so the VM tests run in parallel too":** process-level shards over separate pools give the VM gain without code changes. t.Parallel stays the in-process follow-up.
- **"Tests that need wall-clock behaviour should declare their time cost as data, next to `ci/expected-skips.txt`":** `ci/real-zfs-weights.txt` is that file for the real-ZFS leg, and it drives the sharding.
- **"cached qemu":** say "unpacked from the runner image's own apt view, per runner image version, with apt as the fallback". It is not a custom runner image.
