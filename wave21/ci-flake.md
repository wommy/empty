# wave21: CI robustness, adversarial read (read-only)

Scope: ci/real-zfs-vm.sh (host), ci/real-zfs-guest.sh, ci/real-zfs-cloud-config.yaml,
.github/workflows/{ci,nightly,cache-warm}.yml, .github/actions/real-zfs-keys, ci/qemu-tree.sh,
ci/check-split.sh. Timings come from lanes/ci-runs.jsonl and anomalies.jsonl.
Ranked by likelihood x cost. Every line number refers to HEAD 4a63496.

## 1. Userland/kmod equality is checked down to the Ubuntu revision, and the guest reprovisions every week (HIGH)
- Where: real-zfs-vm.sh:433-434, cloud-config:11-12 (`package_update: true`, zfsutils-linux from the archive), real-zfs-keys action.yml (`date -u +%G-W%V` in the key).
- Scenario: the kmod ships inside the image's kernel (linux-modules / linux-main-modules-zfs). Userland is the newest zfsutils-linux in the archive at provisioning time. A zfs-linux upload (2.4.4-1ubuntu4 to ubuntu5) lands before a kernel rebuilt against it. Every cold provisioning then gets `zfs-2.4.4-1ubuntu5` with `zfs-kmod-2.4.4-1ubuntu4`, and line 433 fails with "userland and kmod versions differ". The ISO-week key forces a cold reprovision every Monday. cache-warm fails, nothing is stored, and every PR is cold and red until the kernel catches up. The stored guest is fixed within a week, so this is not flaky inside a week: it is a weekly cliff.
- Likelihood: medium. 26.10 is on a pre-release (RC) track, where packages migrate continuously and kernel respins lag zfs-linux uploads. Today both are 2.4.4-1ubuntu4 (two log hits). Cost: all VM jobs red until a fix lands.
- Minimal fix: compare only the upstream version. Strip `-*` from both strings before the equality, keep the 2.4.x gate at 431-432, and print the full revisions in the summary. Or make the revision mismatch a `::warning::`.

## 2. Nothing on the host bounds the guest test run, and ssh has no keepalive (MED-HIGH cost)
- Where: real-zfs-vm.sh:488 (`vm "sudo ... bash real-zfs-guest.sh"`, no timeout), common[] at 125-126 (no ServerAliveInterval), guest:47-48 and the comment at 42-44.
- Scenario: the comment says a runaway write gives "ENOSPC (pool suspends, test times out)". It does not. A tmpfs vdev ENOSPC suspends the pool with the default failmode=wait, so the writers sit in D state. Go's -test.timeout=4m panic cannot reap them. The `tee` pipe, the guest `wait` (225-227) and the host ssh then hang until the job's 30-min timeout. A guest kernel hang (qemu alive) also hangs ssh forever, because slirp keeps the TCP connection up and there is no ServerAlive. The cancelled job uploads only $work/*.log (console). out/ never comes back, so there are no shard logs.
- Likelihood: low per run (the tests write little). Cost: 30 runner-minutes, and the diagnosis is lost.
- Minimal fix: `timeout -k 10 8m` around line 488, plus `-o ServerAliveInterval=10 -o ServerAliveCountMax=6` in common[]. In mkpool (guest:48) add `-o failmode=continue`, so ENOSPC returns EIO and the test fails instead of hanging.

## 3. `rootwait` with no timeout defeats the "panics, qemu exits, GRUB fallback" design (latent, HIGH cost)
- Where: real-zfs-vm.sh:172 (adds `rootwait`), 173 (`panic=1 -no-reboot`), 318-325.
- Scenario: the fallback exists for "this kernel cannot mount /dev/vda1 without an initrd". The likeliest form of that is a new image kernel with virtio_blk or virtio_pci built as a module. Then /dev/vda1 never appears, and `rootwait` makes the kernel wait forever ("Waiting for root device"). It never panics, so qemu never exits and the fallback never runs. The result is a 600s wait_ssh, then "VM never became reachable", on every PR until fixed. A panic happens only when the device appears but the fs cannot mount (ext4 as a module).
- Likelihood: low today (kdeb/config: VIRTIO_BLK=y, VIRTIO_PCI=y, EXT4_FS=y). It is re-rolled on every new image serial, which reprovisions and stores the new kernel. Cost: hard red on all VM jobs, about 10 min each.
- Minimal fix: `rootwait=10` (a timeout in seconds, kernel >= 6.5; this kernel is 7.3), so the kernel panics after 10s and the fallback runs.

## 4. wait_ssh checks qemu liveness only after the 60s prompt gate (LOW-MED)
- Where: real-zfs-vm.sh:305-315 (`continue` while there is no prompt and SECONDS < gate) runs before the dead-qemu check at 318.
- Scenario: a direct-boot panic exits qemu within about 1s, but the loop sleeps out the whole 60s gate before it notices and reboots through GRUB. Every fallback run pays about +60s, which breaks the 60s ADR budget on every PR while the fallback is active. On a non-noinitrd boot (GRUB, cold, ZFSVM_INITRD) a qemu that dies is never noticed. The loop spins to the 600s deadline.
- Minimal fix: at the top of the loop, `kill -0 "$(cat "$work/qemu.pid")" || { if noinitrd: fallback; else: echo "qemu exited" >&2; exit 1; }`, before the prompt check.

## 5. The login-prompt regex is anchored to end of line (LOW cost; answers the cold-first-boot question)
- Where: real-zfs-vm.sh:306 (`/ login: *\r?$/`).
- agetty writes "host login: " with no newline. Anything printed to ttyS0 after it lands on the same line: kernel printk at level <4, or a process writing to /dev/console. Once that happens the line never matches again, and the gate waits the full 60s.
  - Warm direct boots: systemd stops its own status output once an idle (Type=idle) getty takes the console (no_console_output). The prompt matched in all 12 VM jobs of runs 40536341101, 40536492611, 40537085486 and 40537458909, since every one was green in under 60s from creation. Risk: kernel warnings only. Low.
  - Cold first boot: cloud-final runs with StandardOutput=journal+console, and its apt/runcmd output streams for 47-73s. getty (Type=idle) starts at the 5s idle timeout in the middle of that stream, so the prompt line very likely gets output glued onto it ("ubuntu login: cloud-init[..]: ..."). Expect the 60s fallback on cold runs. No cold run since 6629b7e is in the ledger, so this is unconfirmed.
  - The cold miss costs little and adds no penalties. By 60s, nologin is long gone (cloud-init's network stage, which writes the ci user's key, is Before=systemd-user-sessions, and getty is After=it). The first probe logs in, and line 337's wait for boot-finished (apt 47-73s) dominates anyway. So a cold gate cannot let ssh in before the key exists, and an early login is not penalised.
- Minimal fix (cheap): match ` login: ` anywhere on the line, i.e. drop `$`. An agetty prompt is the only console text of that shape.

## 6. The nfs-server wait has a 30s cap and swallows its own timeout (LOW)
- Where: real-zfs-vm.sh:438. The loop's exit status is the last `sleep`'s, 0, so a timeout shows up at 440 as "nfsd threads: 0 / nfs-server is not running".
- Observed: nfs-server came up 2.2s after ssh (run 40513522394). A contended boot took 31.1s of userspace (csi, run 40506940034). nfs-server sits behind network-online.target, which can wait up to systemd-networkd-wait-online's 120s on a link that is slow to come up.
- Minimal fix: a 120s cap, `systemctl is-active -q nfs-server` after the loop as its exit status, and on failure print `systemctl status nfs-server` and `systemd-analyze critical-chain nfs-server.service`.

## 7. Cold path: a guest that will not power off is rebooted on a port the old qemu may still hold (LOW)
- Where: real-zfs-vm.sh:376-381 (`kill`, `sleep 2`, `boot "$img"`).
- If the old qemu takes more than 2s to exit, the new qemu cannot bind hostfwd 127.0.0.1:2222. qemu -daemonize then fails, and set -e fails the job. Both instances would also write console.log.
- Minimal fix: wait for exit (`for _ in $(seq 50); do kill -0 ... || break; sleep 0.2; done`, then `kill -9`) before booting.

## 8. Cold path: the image is fetched from `current/` after SHA256SUMS was pinned (LOW)
- Where: real-zfs-keys action (SUMS fetched at job start), real-zfs-vm.sh:223-224 (image downloaded from `$base/$file` later).
- If the `current` serial is republished between the keys step and the curl (one restore plus qemu setup, 10-30s), `sha256sum -c` fails. Only cold runs (cache-warm, cold PRs) are exposed. This is a rare transient red.
- Minimal fix: take the serial from the `current` redirect target or build.info once in the keys step, and download from `$series/<serial>/`. Or retry once with a refreshed SUMS on mismatch.

## 9. Partial cache restore can look warm (LOW)
- Where: ci.yml:213-221 and real-zfs-vm.sh:218.
- actions/cache/restore only warns on an extraction error and leaves partially extracted files. If `ok` and `sum` are extracted but disk.qcow2 is truncated, the script boots "warm" on a broken disk and the job goes red with a confusing error.
- Minimal fix: before trusting `ok`, also run `qemu-img check -q "$cache/disk.qcow2"`, or store a size or sha of the disk in `ok` and compare it.

## 10. Cosmetic / no action
- real-zfs-vm.sh:88 `build=${2:-current}` is reused at 250/279 as the build PID, and cleanup (146) runs `kill current` in the boot phase. Harmless (2>/dev/null, `|| true`). Rename one of them.
- Cache races between the three VM jobs: only csi saves (ci.yml:296/303), and only on a miss or fallback. A concurrent save of the same key by cache-warm or a main push only warns in v4.2.3. Go caches: only part 0 saves. No correctness race found. Monday 00:00-00:23 UTC (week roll before the hourly cache-warm) and LRU eviction of the 0.6 GB guest under the 10 GB budget only make PRs cold (157s), not red.

## Checked and found sound
- console.log reuse across reboots: qemu's file chardev opens with O_TRUNC unless append=on, and -daemonize returns after the chardev is open. The GRUB fallback (319 copies the log first) and the cold-to-overlay reboot (375) both start from an empty log, so a stale "login:" cannot pass the gate.
- 60s fallback vs PerSourcePenalties: by 60s after start, a warm guest (about 9-10s to boot) or a cold guest (key written before user-sessions) is past nologin, so the first `vm true` succeeds. A guest still in nologin at 60s (only under extreme contention) costs one 15s block, then succeeds. It cannot hang.
- `wait` under set -e: 414, 475 and 508 are all in `||` lists. The guest uses no -e. Background `vm` jobs share the ControlMaster opened by wait_ssh.
- Endpoint split: check-split pins tot == propRTRows (196) in each job and recomputes the plan from the same commit. A green on both parts implies every row and test ran once. A renamed TestPropertyRoundTrip skips only the row check (documented), and check-skips covers the list.
- Test timeouts: PR shard phases are 6.6-10.7s against -test.timeout=4m. Nightly full runs: the crash sweep is about 29s and the full zfsdiff about 48s (21 seqs = 9.9s, scaled to 102), with 3 shards in one part. Ample margin under 4m.
