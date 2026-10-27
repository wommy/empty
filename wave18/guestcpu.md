# Where the endpoint guest's sys+irq time goes, and which cheap changes could cut it (wave 18)

This was a read-only investigation. I did not boot a VM. The only thing I ran was the simulator test run in section 1, which changed nothing in the repo.

## 0. What the run 2026-10-27T13:36 log shows (wave16/logs/endpoint-full.log)

- The test phase took 22.8s on 4 vCPUs. CPU was 91% busy: user 21%, sys+irq 69%, steal 1%, iowait 5%. That is about **83 CPU-s**, and about 57 CPU-s of it is sys+irq. PSI: cpu some 73%, io some 58%.
- In vmstat, `r` stays at 8-31 runnable on 4 vCPUs. There were about 12k interrupts/s and **about 28k context switches/s**. `bi` ran at 4-10 MB/s with almost no `bo`. `buff` held flat at about 13.5 MB while `cache` grew about 10 MB/s, which is the tmpfs vdevs filling.
  - The `bi` rate cannot come from the pools. vdev_file I/O to tmpfs never goes through submit_bio, so it never counts in pgpgin.
  - So the reads come either from vda or from zd* (zvol) devices. The flat `buff` fits block-device page cache that is dropped as each zvol is destroyed, which means short-lived zvols being probed.
- Shard walls were ep.0 15.6s, ep.1 22.2s and ep.2 22.5s. The slowest tests were TestRealFacts 9.5s, TestZFSDiffReplay 6.0s and TestCloneSourcesMustBeOwned 4.4s.
- Units still running in the guest (systemd-analyze blame): **udisks2.service** and **rsyslog.service**. The default target is graphical.target, so udisks2 runs. Nothing in ci/ or the cloud-config stops or masks **zfs-zed**, which Ubuntu installs because zfsutils-linux recommends it. The guest cmdline is `root=... ro console=tty1 console=ttyS0` plus the masks, so **kernel mitigations are on** and Ubuntu's default `init_on_alloc=1` is in effect.
- The workload is mostly zfs(8) fork/exec. I ran the endpoint package on the simulator with `ZFS_TEST_CALLS=1` (`go test ./internal/endpoint`). The setupConfig harness alone issued **288 setups, 5,777 get/list and 2,202 mutating zfs calls**. TestRealFacts, modelrun, zfsdiff and crash use their own rigs and add more on top, so the real run is likely more than 10k execs.
  - 83 CPU-s over about 10k execs is roughly 8 ms of CPU per zfs op. That fits process creation, mount churn and event-handler fan-out much better than it fits ZFS data-path work, because the tests write very little.
  - Every mutating op runs a dsl sync task, which waits for a txg. That costs latency, not CPU.
- How CPU-s turn into wall time: the phase is CPU-contended, so wall time drops about in proportion to the CPU-s removed. A cut of 3s of wall needs about 11 CPU-s, or 13% of 83. The floor is the longest serial chain, around TestRealFacts, roughly 9-10s.

## 1. Candidates

| # | Change | Evidence | Predicted endpoint saving | Risk to what the tests validate | How to toggle |
|---|---|---|---|---|---|
| A | **Stop zfs-zed (and so its syslog/journald fan-out) during the shards** | Each history record (every zfs command, plus internal create/set/destroy records) posts a `sysevent.fs.zfs.history_event` zevent. For every event, zed forks each matching zedlet. `all-syslog.sh` matches every event: it forks sh and sources zed.rc and zed-functions.sh before applying `ZED_SYSLOG_SUBCLASS_EXCLUDE=history_event`. Ubuntu also enables `history_event-zfs-list-cacher.sh` (needs verifying, see M). That is about 2 or more short shells per event, over roughly 5-8k events, landing as sys (fork/exec/mm). | **2-6s.** This is the largest unknown, and the instrumentation in M resolves it. | **Low.** zed only consumes events; the endpoint and the tests never depend on it. sharenfs goes through libzfs inside zfs(8), not zed. The zfs-list cache only feeds the mount generator at boot. | `systemctl stop zfs-zed` in real-zfs-guest.sh before the shards. This is a runtime change, so no cache-key bump. Alternatively, mask it from the kernel cmdline in `masks` (vm.sh:130). |
| B | **Kernel cmdline `mitigations=off init_on_alloc=0`** (append to `masks` in vm.sh boot(), direct boot only) | The workload is fork/exec plus about 28k context switches/s and is 69% sys. Mitigations add work to each syscall entry and exit and each context switch: retbleed/SRSO return thunks on the AMD EPYC runners, IBPB, and MDS/VERW clears. Ubuntu's `init_on_alloc=1` zeroes every page on fork/exec and every ZFS ABD/kmem page. Nested KVM (the GitHub runner is itself a VM) makes the MSR- and exit-heavy parts cost more. | **1.5-3s** (a 10-20% cut of the sys CPU-s) | **None for ZFS semantics.** It is a throwaway, single-tenant CI guest. The ZFS code paths and the kmod do not change. | Edit the `masks` string. The stored cmdline is rewritten at every boot, so no cache-key bump. |
| C | **Stop and mask udisks2 during the shards** | It is running (blame 441ms). It reacts to every block uevent (zd* add/change/remove) by reading the udev db and sysfs and probing, and to every mount/umount by re-reading the whole mountinfo. The tests create and mount hundreds of datasets across 3 pools, so each change costs O(mounts). It can also hold /dev/zdN open, which produces busy destroys that `udevadm settle` does not cover. | **0.5-2s** | **Low.** udisks2 is not part of the endpoint's contract. Stopping it also removes a source of spurious EBUSY. | `systemctl stop udisks2; systemctl mask --runtime udisks2` (D-Bus-activatable, so mask it) |
| D | **Skip udev persistent-storage probing (blkid) on zd*** while keeping 60-zvol.rules | `bi` of 4-10 MB/s with no data-path reads in the tests (no endpoint test opens /dev/zvol or /dev/zd: I grepped every `*_test.go`). That fits blkid probing, which reads superblock offsets including the end of the device with readahead, plus the kernel partition scan, on every new zvol and clone. It also costs udev worker CPU for rule evaluation. | **0.5-1.5s** | **Low-moderate.** The /dev/zvol links survive, because zvol_id still runs. The real "udev holds a new zvol open" window shrinks, so the settle-and-retry path (endpoint.go:175-187, apply.go:73-95; harness cleanup endpoint_test.go:111-118) is exercised less often on real ZFS. It is a race either way, and the deterministic test of that path (settle_test.go) is simulator-only. Production hosts do run blkid, so call this out in the ADR if it is adopted. | `printf 'KERNEL=="zd*", ENV{UDEV_DISABLE_PERSISTENT_STORAGE_RULES_FLAG}="1"\n' >/run/udev/rules.d/59-zd-noprobe.rules; udevadm control --reload` (first check the guest's 60-persistent-storage.rules honours the flag) |
| E | Go runtime: `GOMAXPROCS=2` per shard and/or `GOGC=200` | 3 processes × GOMAXPROCS=4 on 4 vCPUs, with -test.parallel=8, means spinning Ms, futex traffic and context switches. user is only 21%. | 0-0.5s | None | env on `run` in real-zfs-guest.sh |
| F | `zvol_volmode=3` (none) or `zvol_inhibit_dev=1` for the endpoint guest | Removes every zvol minor, and with it every udev, blkid, udisks and partition-scan cost and the zvol taskq. | 1-3s (an upper bound on C+D, zvol share only) | **High.** No /dev/zvol at all, so the real device lifecycle and the busy-hold race the endpoint is built to handle disappear from the only real-pool run. `zfs get volmode` still reads `default`, which makes the change invisible in reports and therefore easy to forget. **Do not adopt.** It is only useful as a measurement upper bound. | modprobe option |
| G | zfs_txg_timeout / sync=disabled | Sync tasks force their own txg via txg_wait_synced, so timeout is irrelevant. sync=disabled only affects ZIL (fsync/O_SYNC), which the tests barely use. Changing sync on the pool root also changes inherited property values the property tests read. | about 0 | Medium if set as a property | none |
| H | ARC size | Memory PSI 0%, about 5.1 GB free throughout | 0 | none | none |
| I | tmpfs vdev → raw virtio disk | tmpfs is already the cheapest option. A virtio disk adds qemu I/O, vda interrupts and host page cache work. | negative | none | none |
| J | qemu `-smp` vs nproc | Steal is 1% and the phase is CPU-bound with 4 host CPUs. Overcommitting does not add CPU. | 0 | none | none |
| K | KVM halt-polling | Idle is 4%, so the vCPUs almost never HLT | 0 | none | none |
| L | virtio IRQs | vda/net traffic is tiny. The 12k/s interrupts are mostly LOC timers and RES/CAL/TLB IPIs from exec, munmap and context-switch churn. B reduces their cost; nothing cheap removes them. | about 0 directly | none | none |
| — | Rejected: run the shards in a private mount namespace (hides mount churn from PID1 and udisks) | It breaks NFS fidelity, because nfsd and mountd resolve export paths in the init namespace. | — | High | — |
| — | Rejected: pool `compression=lz4` → off | Children's inherited compression value changes. Metadata is lz4 regardless. | about 0 | Medium | — |

The other suspects are PID1 mount-unit churn (rate-limited mountinfo re-parse plus D-Bus signals to dbus-broker) and libzfs's per-exec mnttab read, which is O(mounts). They are part of real behaviour and have no cheap fidelity-neutral fix. The measurement below sizes them (init.scope, dbus-broker), so they can be pursued or dropped on data.

## M. One measurement build

This build adds diagnostics only and has no gates. Use a workflow_dispatch workflow that runs the endpoint job as a matrix of 4 variants × 2 repeats in one run. The variants are passed as `ZFSVM_EXPERIMENT` from real-zfs-vm.sh to real-zfs-guest.sh, the same way `ZFSDIFF_FULL` is passed today:

1. `base`
2. `quiet`: A + C, i.e. `systemctl stop zfs-zed udisks2; systemctl mask --runtime udisks2`, before the shards.
3. `quiet+noprobe`: also D, the /run/udev rule plus `udevadm control --reload`.
4. `cmdline`: B, with `mitigations=off init_on_alloc=0` appended to `masks`. The guest-side toggles are off in this variant.

Compare the shard walls in shards.txt and the busy/sys numbers in load.txt across variants. Runner noise is about ±1s, so take the mean of the 2 repeats.

Instrumentation to add to `snap()` in ci/real-zfs-guest.sh, taken before and after the shards and diffed with awk into out/load.txt. Everything below is /proc, /sys or cgroup reads; no perf or bpf is needed, since the cloud-config purges them anyway.

- The whole of `/proc/stat`: the `cpu`/`cpuN` lines plus `intr` (first field), `ctxt`, `softirq`, and **`processes` (the fork count)**. Report forks/s and CPU-ms per fork.
- `/proc/interrupts`, diffed per row: LOC, RES, CAL, TLB and the virtio* rows. Also `/proc/softirqs`.
- `/proc/vmstat`: pgpgin, pgfault, pgmajfault. `/proc/diskstats` for vda: sectors read × 512. **zvol bytes read = pgpgin×1024 − vda bytes.** This attributes the `bi` directly.
- cgroup v2 CPU, perf-free: `usage_usec user_usec system_usec` from `cpu.stat` in each of these:
  - `/sys/fs/cgroup` (the root, which is system-wide)
  - `init.scope`
  - `system.slice/{zfs-zed,udisks2,systemd-udevd,systemd-journald,rsyslog,dbus-broker,nfs-mountd,nfs-server}.service`
  - `system.slice` (the whole slice)
  - the shards' own cgroup (`$(cut -d: -f3 /proc/self/cgroup)`, the ssh session scope that holds the test binaries and their zfs(8) children)

  Kernel threads plus irq = root − (init.scope + system.slice + user.slice).
- Kernel threads by name: for `/proc/[0-9]*` with an empty `cmdline`, collect `comm`, utime and stime (fields 14 and 15), then sum by prefix: `txg_`, `z_wr_`, `z_rd_`, `z_zvol`/`zvol`, `spl_`, `kworker`, `ksoftirqd`, `rcu`, `z_` (the rest). Dynamic SPL taskq threads that exit mid-phase drop out of this, but the root-minus-cgroups total above still covers them.
- Event counts:
  - Run `udevadm monitor --kernel --subsystem-match=block >out/uevents.txt &` beside vmstat. Report the add/remove/change counts for `zd*`; the add count is the number of zvol minors.
  - Run `zpool history -il zep$i | wc -l` per pool just before the destroy loop. That counts the history records, which equals the history zevents zed received.
  - Run `journalctl -u zfs-zed --since @<start> -o cat | wc -l`.
- Rusage split. In the shard subshell, run `times` after the pipeline: the children's u/s is the test binary plus all its zfs(8) children. Optionally, behind an env var, add a diagnostic-only log of `syscall.Getrusage(RUSAGE_SELF)` versus `RUSAGE_CHILDREN` to endpoint's TestMain (main_test.go:9), which splits Go's own time from zfs(8)'s.
- One-time context lines, written once into out/:
  - `cat /proc/cmdline`
  - `grep . /sys/devices/system/cpu/vulnerabilities/*`
  - `cat /sys/devices/system/clocksource/clocksource0/current_clocksource`. If it is not kvm-clock or tsc, every time call is a syscall or exit, and that becomes its own lever.
  - `ls -l /etc/zfs/zed.d`, which settles whether the list-cacher zedlet is enabled.
  - `systemctl is-active zfs-zed udisks2`
  - `udevadm --version`
  - `grep -c UDEV_DISABLE_PERSISTENT_STORAGE_RULES_FLAG /usr/lib/udev/rules.d/60-persistent-storage.rules`

Decision rule:

- Adopt A+C if `quiet` saves ≥1.5s, or if the zed+udisks2+journald+rsyslog cgroups total ≥6 CPU-s.
- Adopt B if `cmdline` saves ≥1s. It has zero fidelity cost.
- Adopt D only if `quiet+noprobe` adds ≥0.7s over `quiet`, and record the narrowed udev-hold window in ADR 0005.
- Never adopt F.

## Ranked (expected seconds × fidelity safety)

1. **A: stop zfs-zed** for the shards. 2-6s, low risk, the leading suspect for the fork storm.
2. **B: guest cmdline `mitigations=off init_on_alloc=0`.** 1.5-3s, no ZFS-fidelity risk, no cache-key bump.
3. **C: stop and mask udisks2.** 0.5-2s, low risk, and fewer spurious EBUSY holds.
4. **D: skip blkid on zd*** while keeping the /dev/zvol links. 0.5-1.5s, low-moderate risk (the settle path is exercised less).
5. E: GOMAXPROCS/GOGC per shard. ≤0.5s.
6. F: zvol_volmode=none. Upper bound only; rejected on fidelity.
7. G-L (txg/sync, ARC, vdev type, -smp, halt-polling, virtio IRQ): about 0s, with the evidence given in the table.

A+B+C together plausibly reach the ≥3s target without dropping or weakening any check. A and B carry the most expected value. The measurement build above confirms the split in one workflow run.
