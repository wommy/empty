# csi-block "SingleNodeWriter NoCapacity": `zfs destroy` EBUSY ~150ms after create

Run: 0a0bea3, real-ZFS VM (OpenZFS 2.4.4-1ubuntu4, kernel 7.3.0-9, Ubuntu 26.10).
Log: `vm-0a0bea3.log`. Compared with: `vm-088e69b.log` (extracted from `vm-088e69b.raw.json`).

## Verdict

- **Root cause.** The dataset is a **zvol**, not a filesystem. The failing shard is `csi-block` (`CSI_SANITY_ACCESS_TYPE=block`, pool `zcsi2`).
  - `zfs create -V` returns only once the zvol's minor exists. The kernel has then raised the `add` uevent for the new `/dev/zdN`.
  - udev's rules for that event open the device: `60-zvol.rules` runs `zvol_id $devnode`, and the persistent-storage rules run the `blkid` builtin.
  - While the device is open, the dataset is long-held, so `zfs destroy` gets EBUSY, which libzfs prints as "dataset is busy".
  - csi-sanity's NoCapacity spec does nothing between CreateVolume and its cleanup DeleteVolume (0.147s for the whole spec). On a guest where udev is still loaded, the delete arrived before udev had finished the event.
- **Not introduced by 0a0bea3.** The bug is pre-existing and was exposed by timing. Nothing 0a0bea3 changed runs on this path: the csi harness sets no `FileExport`, so S6 is off, and the plan for a plain zvol create is unchanged.
- **Not caused by 088e69b's NFS replay.** exportfs never opens a block device, and the run on 088e69b also ran the replay alongside csi-block, which passed there.
- **Confidence:**
  - High that the hold is on the zvol, an open of `/dev/zdN` and not an unmount or NFS problem.
  - Medium-high that the opener is udev's add-event processing. No guest journal or `fuser` output was captured. The other candidate is udisks2, which reacts to the same event.
- **Fix (product): `busy.patch`.**
  - When a destroy is busy, DeleteVolume settles the device events once (`udevadm settle`, through a new `Config.Settle` seam) and retries once.
  - If the retry is still busy, the result stays FAILED_PRECONDITION ("in use"), exactly as before.
  - There are no sleeps and no loops. The simulator test fails before the fix and passes after it.

## Anomalies, ranked

1. **The busy dataset is a zvol.**
   - The failure is in the `csi-block.log` group (log line 273).
   - `ci/real-zfs-guest.sh`: `run csi-block zcsi2 env CSI_SANITY_ACCESS_TYPE=block ./csi.test ... -test.run '^TestSanity$'`.
   - `internal/csi/sanity_test.go:75`: `"mount" (the default) provisions filesystems, "block" zvols`.
   - The error names `zcsi2/csi-1793085656772175680/sanity-controller-create-single-no-capacity-0A47B60A-5E90BF36` (log line 310).
   - So every filesystem-side candidate in the brief is ruled out: the second scan+plan pass, `sharenfs`, `exportfs -ra` re-reading zfs.exports, mount units and unmount.
   - The same spec on filesystems (the `csi-mount` group, line 410) passed in the same run, at the same second.
2. **0a0bea3's code is not on this path.**
   - `internal/csi/harness_test.go:47`: `endpoint.New(z, endpoint.Config{Parent: parent})`. FileExport is false, so `Planner.RequireMounted` is false.
   - The `pass` loop in CreateVolume breaks after one pass whenever the plan commits (`p.commits(plan)`). A Block create plan always ends in the commit.
   - The other Block changes in 0a0bea3 are confined to clones (`blockClone`, `sourceBlockSize`).
   - H-01 (TryAcquire) only changes how a lock is taken.
   - So the zfs command sequence for this spec is identical on 0237666 and 0a0bea3: `create -V`, then `set` (commit), then `get`.
3. **The error string is EBUSY from the destroy ioctl, not from an unmount.**
   - libzfs `zfs_destroy` → `lzc_destroy` → `zfs_standard_error_fmt` → `case EBUSY: "dataset is busy"` (libzfs_util.c:472-475).
   - In the kernel, `zfs_ioc_destroy` → `dsl_destroy_head` → `dsl_destroy_head_check_impl`: `if (zfs_refcount_count(&ds->ds_longholds) != expected_holds) return (SET_ERROR(EBUSY));` (dsl_destroy.c:776-777).
   - A zvol is long-held exactly while its block device is open: `zvol_first_open` → `dmu_objset_own(...)` (zvol.c:1255), released in `zvol_last_close` (zvol.c:1290).
   - An unmount failure on a filesystem would fail earlier in zfs(8) with an unmount error. For a zvol, `zfs_unmount_snap` is skipped anyway (`if (ost == DMU_OST_ZFS)`, zfs_ioctl.c:4417).
4. **OpenZFS itself treats EBUSY on a just-created zvol as transient.**
   - In zfs_ioctl.c:3805-3818 (`zfs_ioc_create` error path), the comment reads: "Volumes will return EBUSY and cannot be destroyed until all asynchronous minor handling ... has completed. Wait for the spa_zvol_taskq to drain then retry."
   - In 2.4.4 the minor itself is created synchronously before `zfs create` returns: `dmu_objset_create` → `zvol_create_minors(name)` → `taskq_wait_id` (dmu_objset.c:1373, zvol.c:2067-2081). So the kernel's own holds (`zvol_os_create_minor` owning the objset, zvol_os.c:1694/1771) are gone by the time the CLI exits.
   - What remains after `zfs create` is userspace reacting to `add_disk`.
   - zvol_os.c:1757 says so directly: "When udev detects the addition of the device it will immediately invoke blkid(8) to determine the type of content on the device." `60-zvol.rules` adds `PROGRAM=="@udevdir@/zvol_id $devnode"` on `add|change`.
5. **The guest is still booting while the shards run.**
   - `ci/real-zfs-vm.sh:142-143` says the job waits for nfs-server and zfs only, "not for the whole boot to settle".
   - Tests start at uptime 9.41s (log line 264). The console tail shows `udisks2.service`, `zfs-share`, `zed` and others starting during that boot.
   - Six shards on six pools create zvols concurrently. The endpoint shards and csi-block each create dozens, and every one is an udev `add` event plus a `zvol_id` exec and a blkid probe.
   - udevd's queue is shared by all shards, so the delay between `zfs create` returning and udev releasing the device grows with load. Here it was larger than csi-sanity's create→delete gap of under 150ms.
6. **Spec shape.**
   - "SingleNodeWriter NoCapacity" (csi-test v5.6.0 controller.go:379-387) is the tightest create→delete in the suite: `MustCreateVolume`, then the AfterEach `r.Cleanup()` (controller.go:123-124).
   - The cleanup sends NodeUnpublish and NodeGetCapabilities (fixture answers, microseconds), then DeleteVolume once with **no retry** (resources.go:292-299). Any error, whatever its code, fails the spec.
7. **Not related, noted for completeness.**
   - Both runs print "skip budget vm/endpoint: 20 skipped, 17 allowed" without an error. Subtests are counted against top-level entries, which is pre-existing.
   - 088e69b's red was TestNFSReplay (endpoint.0 rc=1), not csi.

## Q1: what holds a fresh dataset busy for ~150ms on this guest

| Candidate | Verdict | Evidence |
|---|---|---|
| The endpoint's second scan+plan pass (S6) | No | Not active in csi shards (FileExport unset); Block plans commit in pass 0 |
| zfs share / `sharenfs` on csi volumes | No | csi volumes are created with no `sharenfs`, and this one is a zvol (sharenfs does not apply) |
| Concurrent `exportfs -ra` from TestNFSReplay (endpoint.0) | No (load only) | exportfs walks exports and paths, never opens `/dev/zd*`. It also ran alongside on 088e69b (endpoint.0 9.45–14.30s vs csi-block 9.76–18.00s), and csi-block passed |
| zfs-mount-generator / systemd mount units | No | They act on filesystems; a zvol has no mount |
| Kernel async minor creation | No (in 2.4.4) | `zvol_create_minors` waits on the taskq before `zfs create` returns |
| **udev add-event processing (`zvol_id`, blkid)** | **Yes (most likely)** | Opens `/dev/zdN`, which long-holds the dataset (zvol.c:1255), causing EBUSY in destroy. It lags under boot plus six-shard load |
| udisks2 | Possible secondary | Starts during this boot and reacts to block uevents after udev. `udevadm settle` does not wait for it |

## Q2: introduced, or exposed

The cause was pre-existing and exposed by timing. The timeline at 07:20:57:

- The guest starts at 07:20:56.826 (uptime 9.41s). The spec runs 07:20:57.575→.721 (uptime ≈10.16→10.31s).
- `shards.txt` (log lines 266-272): every shard was running then.

  | Shard | Uptime |
  |---|---|
  | endpoint.0 (TestNFSReplay) | 9.51–14.49s |
  | endpoint.1 | 9.60–17.09s |
  | endpoint.2 | 9.68–18.01s |
  | csi-other | 9.74–11.70s |
  | csi-mount | 9.82–17.75s |
  | csi-block | 9.94–18.36s |

- 088e69b had the same concurrency (all six shards from about 9.45s), the same replay, and the same spec, and passed.
- So the difference is how fast udev reached that one zvol's event, not any code. 0a0bea3 changes no zvol code path that this spec takes. Its extra second-pass scans in endpoint shards with FileExport (TestFileExport*, TestNFSReplay) only add a little load to the shared guest.

## Q3: fix

Options considered:

- **Map busy-on-destroy to Aborted or Unavailable.** Rejected.
  - It does not fix this failure: csi-sanity's cleanup never retries (resources.go:292-299).
  - It would also break the CSI contract the code pins on purpose. "Volume in use" is FAILED_PRECONDITION; see `opErr` and TestBusyIsMappedByOperation.
- **Test isolation (shard ordering, unshared csi volumes, retry in the harness).** Rejected.
  - Nothing is shared, and the volume is a zvol.
  - Retrying in the harness, or making csi-block not delete right after create, would hide a product race that a real CO hits too. external-provisioner deletes a PVC's volume as soon as the claim is removed. That is cutting a check, which ADR 0005 forbids.
- **CreateVolume waits for `/dev/zvol/<ds>`** (`udevadm wait`). A possible follow-up, since `DevicePath` is reported before udev has made the link. As the only fix it is worse:
  - It blocks every create on udev.
  - It must special-case `volmode=none` (no device ever appears).
  - It adds a wait to the hot path, where the chosen fix pays only on the rare busy path.
- **Chosen: settle once and retry once in the destroy op (`busy.patch`).**
  - `Config.Settle func(ctx) error`, with the default implementation `endpoint.UdevSettle`, which runs `udevadm settle --timeout=min(ctx deadline, 10s)`.
    - It pings udevd, so an event already raised by `zfs create` is included.
    - With no udevadm, as in a container without udev, it is a no-op.
  - In `apply`, `OpDestroy` goes through `e.destroy`: on `zfs.ErrBusy`, settle once and retry once.
    - Still busy: the original mapping, FAILED_PRECONDITION.
    - Settle failed: the original busy error.
    - `Settle == nil`, as in the simulator and the default `Config`: unchanged behaviour.
  - Wired in `cmd/zfs-endpoint` (`deps.settle = endpoint.UdevSettle`; cmd tests leave it nil), in the real-pool csi harness, and in the endpoint real-pool harness (`setupPrefix`).
  - ADR fit:
    - **ADR 0003**: it mirrors OpenZFS's own `zfs_ioc_create` retry-after-drain for zvol EBUSY, and keeps OpenZFS's EBUSY as "in use" when the hold is real.
    - **ADR 0005**: no sleeps, no skips, no cut checks. The wait is event-driven, bounded by the caller's deadline, and paid only on a busy destroy. The new test is pure simulator, with no clock.
    - **ADR 0004**: unaffected.

### Test (red before, green after)

`internal/endpoint/settle_test.go`, `TestDeleteVolumeSettlesDeviceEventsOnBusy`:

- The simulator returns "dataset is busy" on destroy until Settle has been called. DeleteVolume must succeed, with exactly one settle between two destroys, and the dataset must be gone.
- With busy forever, DeleteVolume must return FailedPrecondition after exactly one settle and one retry, with no loop.

Before the fix (only the `Settle` field added, no retry):

```
--- FAIL: TestDeleteVolumeSettlesDeviceEventsOnBusy (0.00s)
    settle_test.go:69: DeleteVolume of a zvol udev was still probing: op 1/1 destroy tank/k8s/vols/fresh: zfs destroy tank/k8s/vols/fresh: busy: cannot destroy 'x': dataset is busy
```

This is the same failure shape as CI. After the fix, the test passes, and so do `go vet ./...` and `go test ./...`, including TestBusyIsMappedByOperation and TestAdv4BusyDestroyAbortsRun, which are unchanged.

### Residual risk and suggested follow-ups

- If the opener is udisks2 and not udev's rules, `udevadm settle` does not wait for it.
  - To confirm on the VM, have the csi-block shard record `udevadm monitor --udev --subsystem-match=block` and, on a busy destroy, run `fuser -v /dev/zd*`.
  - If udisks2 shows up, mask `udisks2.service` in the prebaked guest (`ci/real-zfs-cloud-config.yaml` already masks multipathd and ModemManager), as a test-host change on top of the product fix.
- `udevadm settle` waits for the whole udev queue, including other shards' events. That is bounded (≤10s or the caller's deadline) and paid only on a busy destroy.
