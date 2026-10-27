# Wave 18 review: 2b648ef..7048b51

Read-only. I verified the findings on a scratch copy under /dev/shm (since deleted). `go vet` and `go test` pass for cmd/..., deploy/... and internal/endpoint at 7048b51.

Nothing here is blocking: there is no data loss, the timer cannot apply, and no CI change makes it test a different ZFS. One finding is a real false refusal (#1). The rest are low.

## 1. (Medium) Headroom over-counts a thick clone of a snapshot taken before its volume grew: false RESOURCE_EXHAUSTED

- internal/endpoint/headroom.go:72-78 (OpClone case). The code takes the clone's starting volsize from the source *volume's* current row (`t.Get(vol)`), not from the snapshot row. sizeOps (plan.go:465) sizes the clone from `snap.Volsize`.
- Trigger: a thick block volume of 4G, a CSI VolumeSnapshot of it, then ExpandVolume of the volume to 40G. Then a PVC restores from that snapshot at 4G (thick) with `-min-free-bytes` on. The plan is `clone` + `set refreservation=auto`. planReserves counts 40G, but zfs would reserve about 4G. The restore is refused with RESOURCE_EXHAUSTED even with plenty of space. external-provisioner treats that as non-final and retries forever.
- I ran this on the sim (10G available, 1G floor). The 4G clone failed with "reserves 42949672960".
- When the restore also grows the clone (a volsize in the set), the result is correct, which is why the existing table test passes. The clone-from-volume path is also correct, because its internal snapshot is cut from the current volume.
- Minimal fix: look up the snapshot row first, and fall back to the volume only when it is absent (the internal snapshot the same plan cuts):
  ```go
  rel := strings.TrimPrefix(op.Source, p.Parent+"/")
  if snap, ok := t.Get(rel); ok && snap.Name == op.Source {
      after.volsize = snap.Volsize
  } else if vol, _, ok := strings.Cut(rel, "@"); ok { if src, ok := t.Get(vol); ok { after.volsize = src.Volsize } }
  ```
  With this fix, the whole internal/endpoint suite passes.
- Failing test (sim, internal/endpoint):
  1. simEndpoint with `MinFreeBytes: 1G`, PoolSize 100G.
  2. Create a 4G volume "src" (sparse), CreateSnapshot "s1", ExpandVolume src to 40G.
  3. Set PoolSize 10G.
  4. CreateVolume a thick Block 4G volume with `SourceSnapshotID: s1.ID`. Want nil error; today it is RESOURCE_EXHAUSTED.
  5. Also add a table row in TestPlanReserves: the snapshot row's Volsize is smaller than the volume row's.

## 2. (Low) `zfs-endpoint reconcile -apply` exits 0 when destroys failed

- cmd/zfs-endpoint/reconcile.go:122-123. The server reports failed groups as Skipped entries with `SkipFailed`, `SkipDependentFailed` or `SkipNotAttempted` (reconcile.go:445-456), and the RPC returns OK. The CLI prints the entries and returns nil.
- Trigger: an operator applies a digest and one target is busy (EBUSY), held, or hit by a clone race. The output shows "actions applied: N" and a skipped line, but the exit status is 0. A wrapper (`... && echo done`, or a runbook script) treats a partial collection as complete.
- Fix: with `-apply`, return a non-zero error (exit 1, not 78) when any skip reason has prefix `endpoint.SkipFailed`, `SkipDependentFailed` or `SkipNotAttempted`. Print the plan first.
- Test (reconcile_test.go, against the sim server): make one destroy fail (sim injected EBUSY), then run `-apply -digest D`. Want a non-nil error and the failed target still printed.

## 3. (Low) `-max-deletions` silently wraps at 2^32

- cmd/zfs-endpoint/reconcile.go:115, `uint32(*maxDel)` on a `uint` flag.
- `-max-deletions 4294967296` sends 0, which becomes the default cap of 10. `4294967297` sends 1. The wrap only ever lowers the cap, so it fails safe, but the operator gets a cap they did not type.
- Fix: refuse values above math.MaxUint32 with configErr, or parse the flag as uint32 through fs.Func.
- Test: `runReconcile(..., "-max-deletions", "4294967296")` should return a config error.

## 4. (Low) Reconcile timer unit: hardening gaps, and no way to follow a shortened admin socket

- deploy/systemd/zfs-endpoint-reconcile@.service:28-38. The unit is a pure unix-socket client that runs as root. Unlike the daemon, it has no reason to avoid a mount namespace, yet it has no `ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`, `PrivateDevices`, `ProtectKernel*`, `ProtectControlGroups`, `RestrictNamespaces`, `CapabilityBoundingSet=` or `SystemCallFilter=@system-service`.
- Fix: add them. Keep `CAP_DAC_OVERRIDE` only if a daemon running as a non-root uid must be reached. `PrivateNetwork=yes` is also compatible, because path sockets cross network namespaces. Check under ci/real-zfs-unit.sh.
- The daemon unit's comment tells operators with long dataset names to shorten `-admin-listen` in a drop-in. The timer then dials the default path from `-parent` and fails every day.
- Fix: document a matching `-admin-socket` drop-in for the timer unit. deploy/reconcile_timer_test.go forbids `-admin-socket` only in the shipped unit, which is fine.
- Test gap: TestReconcileTimerIsReportOnly matches `-apply` and `-apply=` but not `--apply` / `--digest`, which Go's flag package accepts. Normalize a leading `--` to `-` before the check.

## 5. (Low, CI) check-explore-ran.sh exits with no message when `want` is empty

- ci/check-explore-ran.sh:12. Under `set -euo pipefail`, a `grep -r` with no match fails the `want=$(...)` assignment. The script exits 1 with no `::error::`.
- It still fails in the right direction.
- Fix: append `|| true`, then add an explicit `[ -n "$want" ] || { echo "::error::no TestExplore* functions found"; exit 1; }`.

## 6. (Note, CI cache) The no-initrd fallback is now GRUB, and no CI run exercises it

- ci/real-zfs-vm.sh:135-139 and 246-250. When the direct kernel boot panics, or with `ZFSVM_INITRD=1`, or when the stored cmdline has no `root=LABEL=cloudimg-rootfs`, the guest now boots through SeaBIOS and GRUB from the disk's own /boot. That boot also comes up without the zfs-import and udev-settle masks.
- The path is reasonable: GRUB reads the /boot partition itself, so fstab `noauto` does not matter, and the initrd on the disk predates the module slimming.
- However, it is only reached on failure, and no job runs it. A GRUB recordfail timeout, or a slower unmasked boot, would show up only on the day the fast path breaks.
- Suggest a nightly run with `ZFSVM_INITRD=1`.

## Checked, no finding

- The timer cannot apply. ExecStart has no `-apply`, so the CLI sends `DryRun=true`, and Endpoint.Reconcile returns before locking (reconcile.go:669). Instance names with spaces stay one argument, because systemd expands specifiers per word.
- `-digest` binds the actions (kind, target, guid, reason) of the unlocked re-plan. Different flags yield a different plan and get Aborted. A digest is needed by the CLI even when the server would not need one. (The locked re-plan ignoring the guid is known and out of scope.)
- checkHeadroom has no unsigned wrap. Empty and commit-only retries are never refused. The guard runs before apply on both CreateVolume and ExpandVolume. ResourceExhausted passes through grpcErr unchanged.
- CleanupReal and the test cleanups use a background ctx (not t.Context()). They settle once and then retry, and a missing udevadm is ignored.
- Cache slimming:
  - The purge is guarded by an `apt-get -s` keep-list. The removed module trees contain nothing the guest uses: virtio_net is under drivers/net, not drivers/net/ethernet, and zfs/nfsd are elsewhere.
  - Nothing in the guest scripts uses apt or the deleted docs afterwards.
  - The version, module and nfsd gates still run on every boot, so a different ZFS fails loudly.
  - The trimmed qemu tree keeps every ROM that verify() checks plus `*virtio*.rom`. A missing directory makes qemu-tree.sh exit non-zero, and the job then falls back to apt.
  - Key v5 isolates the old entries.
- The version ldflags check works: `-X` with `$WANT_VERSION` in ci.yml, and both TestVersionFlag tests assert it. The VM's `go test -c`, which runs without WANT_VERSION, skips the check.
- The sidecar digests match deploy_test's regex. zfs-csi is exempted, with a TODO.
