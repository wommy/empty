# Adversarial review: block-export design

Target: `scratchpad/align/gaps/block-export.md`. Checked against `internal/endpoint/{table,plan,apply,endpoint}.go`, the README, and `docs/research/{edges/block-export,edges2/ha-failover,edges2/node-attach,edges2/reconcile-gc,dod/multi-writer}.md`.

Kernel checks were made against torvalds/linux master (GitHub rate-limited part of the fetch). `V:` marks a claim that was verified in the source. `U:` marks one that is unverified and must be checked before it is relied on.

Ranked by severity. Each finding gives the scenario, then the smallest fix.

---

## 1. CRITICAL: a stale LIO config is restored at boot and exposes the wrong volume to the wrong client

**Scenario.** The design treats configfs as empty at boot, with the endpoint as the only writer. On most distros, `target.service` runs `targetctl restore` from `/etc/target/saveconfig.json` before the endpoint starts. By default, `targetcli` saves on exit (`auto_save_on_exit`). One admin opening `targetcli` to look around is enough to snapshot the endpoint's targets into that file. After a later reboot:
- **A revoked initiator gets access back.** An ACL removed by DenyInitiator comes back from the file. It stays live until the endpoint's reconcile runs, and permanently if reconcile fails (see #7).
- **The wrong volume is served.** Volume IDs are derived from names (`volumeID(r.Name)`). Suppose volume X is deleted and re-created under the same name. The new zvol has a new guid, so a new IQN. The restored *old* IQN's backstore still points at `/dev/zvol/<pool>/<X>`, which is now the new zvol. The old IQN's ACL'd initiator logs in and reads or writes another tenant's data.
- `targetctl clear` runs when `target.service` stops, and it wipes the endpoint's exports as a side effect.

**Fix.**
- At startup, refuse to run while `target.service`/`nvmet.service` is enabled or a saveconfig file exists. Report it as a typed startup error.
- In `DiffExports`, treat an owned backstore as wrong unless its `udev_path` resolves to the zvol whose guid derives that IQN. Name equality is not enough. A wrong backstore is torn down, never adopted.

## 2. CRITICAL: guid-derived identity changes when it should not, and a naive fix makes it collide when it must not

**Scenarios.**
- **HA onto a replicated pool, or DR restore after `zfs send -R | recv`.** The zvol's guid changes, so the IQN/NQN, serial, NAA WWN and nguid all change. The CSI PV's publish context, the node's stored session, and multipath's WWID all point at an identity that no longer exists. Initiators never find the volume. The design's HA test covers only shared-pool import, which hides this. The `Export` property is replicated with `send -p`, so the target host re-exports the volume immediately under a new identity.
- **The naive fix collides.** Persisting the identity as a user property makes it survive send/recv, but it also copies it. Any `send -R` into a second dataset on a live host produces two volumes with one serial/WWN. Examples: a backup copy received next to the original, or a DR test that imports the replica while the primary still serves. An initiator that sees both through discovery, or through both heads' portals, multipaths two different volumes as one, interleaves writes, and corrupts both.
- **Split brain on a shared pool.** The identities are equal by design here. Two heads exporting the same pool give the client two paths with the same WWID, and dm-multipath round-robins writes through two independent ZFS instances. `multihost` does not stop this: it checks only at import (`ha-failover.md` §2). Identical identity is exactly what makes the client fan writes out across both heads.
- **Pool imported under another name** (`zpool import tank tank2`, common for DR). `/dev/zvol/<pool>/…` changes. The backstore's device path cannot be changed while it is open (V: `target_core_iblock.c` returns `-EEXIST` for `udev_path=` while `ibd_bd` exists).

**Fix.**
- Mint a random 128-bit `<prefix>:block_id` once, at create or first export. Store it as a local user property, next to `<prefix>:block_id_guid` (the guid at mint time). Derive all names from `block_id`.
- Add a Table index `byBlockID`. If two rows share a `block_id`, the planner refuses to export either one (`FailedPrecondition`).
- If `block_id_guid` ≠ the row's guid, the volume was received or restored. It is not exported until an explicit `AdoptExport(volID, epoch)` re-stamps `block_id_guid`. Failover runbooks call AdoptExport. Backups never do.
- Gate any export on the pool's writer epoch (the CAS in `multi-writer.md`). A head that cannot win the epoch exports nothing.

## 3. CRITICAL: the export record and secrets sit where untrusted parties can read or write them

**Scenarios.**
- **The record is writable by delegated users.** `Export` (with `acls[]`) is a ZFS user property, and reconcile treats it as authority. Anyone with `zfs allow … userprop` on the parent can add their own IQN, and the next reconcile grants it access. Any user can read the property with `zfs get`, so the full ACL map leaks.
- **CHAP has no safe home.** The design does not say where CHAP lives across reboot.
  - In the user property, the secret is world-readable and is copied into every backup stream.
  - If it is not persisted, a reboot recreates the ACL without CHAP. Unless the TPG `authentication` attribute is forced on, the volume then opens to anyone who claims the IQN. IQNs and host NQNs are self-asserted.
- **CHAP is readable in configfs.** The `auth/password`, `password_mutual` and TPG/discovery auth attributes use `CONFIGFS_ATTR` (V: `iscsi_target_configfs.c:418,424,874`), which is mode 0644. Any local user, or any container with host `/sys` mounted (CSI node pods, monitoring agents), can read them. nvmet's `hosts/<hostnqn>/dhchap_key` is global, so setting it for our subsystem changes it for any foreign subsystem that allows the same host.
- **TPG security attributes are never set.** The design leaves `generate_node_acls`, `demo_mode_write_protect`, `cache_dynamic_acls` and `authentication` unset, which hands security to kernel defaults and to whatever an admin or tool last wrote. A TPG with `generate_node_acls=1` serves every LUN to every initiator. U: the kernel defaults.

**Fix.**
- Keep ACLs and secrets out of user properties. The property holds only `{proto, block_id, enabled}`.
  - ACLs come from the front end's desired set, the pillar `ReconcileState(complete)` pattern in `reconcile-gc.md`. Until that set arrives, exports are created disabled.
  - Secrets go in a root-0600 store under `/var/lib/<daemon>`.
- Planners always emit explicit writes: `generate_node_acls=0`, `demo_mode_write_protect=1`, `cache_dynamic_acls=0`, `authentication=1` whenever the policy requires CHAP. Read them back.
- **Fail closed.** If CHAP is required and the secret is unknown, do not enable the TPG.
- Document that `/sys/kernel/config` exposes secrets. Refuse to share `hosts/<nqn>` objects that carry a dhchap key we did not write.

## 4. HIGH, a certain blocker: the LIO backstore never binds a device, so step 4 always fails

**Scenario.** Step 2 writes only the generic `udev_path` attribute. That attribute just stores a string (V: `target_core_configfs.c:2432-2453`). IBLOCK learns its device only from `control` = `udev_path=<dev>` (V: `target_core_iblock.c:573,601-615`). Without it, configure fails: "Missing udev_path= parameters for IBLOCK", `-EINVAL` (V: `:102-104`). So `enable=1` fails for every export. The sim kernel has no such rule, so the planner tests would pass while real hardware never exports. The `liotest` fake in pillar has the same blind spot.

**Fix.** Step 2 becomes `OpCfsWrite control "udev_path=<dev>"` followed by `OpCfsWrite udev_path <dev>`, which is kept for read-back. Add the IBLOCK rule to the sim. The read-back of `control` is not meaningful, so verify through `info`, or through the `udev_path` attribute set at the same time.

## 5. HIGH: login ordering makes initiators give up for good

**Scenarios.**
- **The TPG opens before its ACLs exist.** Step 10 (`tpgt_1/enable=1`) runs before ACLs 11-13. An initiator that reconnects in that window gets an authorization failure (login status 0x0201). open-iscsi treats that as non-retryable, drops the session, and the mount errors out.
- **At boot, the first listening target makes the rest fatal.** The first export reconciled puts `np <ip>:3260` up. Every initiator whose target is not yet built then connects and gets "target not found" (0x0203), also fatal for iscsid. Before any np exists the result is `ECONNREFUSED`, which is retryable. So the first target to listen turns everyone else's retry into a permanent failure.
- **nvmet has the same problem on a smaller scale.** The first port symlink starts listening, and a hostnqn that is not yet allowed is rejected.

**Fix.**
- Reorder LIO so `tpgt_1/enable=1` is the last step, after the ACLs and mapped LUNs.
- Boot reconcile runs in two phases: (a) build every export with TPGs disabled and no port links; (b) enable all TPGs and link nvmet ports in one final pass. This is pillar's "gate port-listening until restore" (`server_export_restore.go:24-43`).

## 6. HIGH: diff ownership is based on names, so reconcile deletes what it should not

**Scenarios.**
- **Another endpoint on the same host.** Two endpoint instances (two parents or pools, the same `<domain>`) share the `iqn.2026-10.<domain>:zvol.` prefix. Each one sees the other's targets as owned extras whose guid matches no row in its own Table, and deletes them.
- **A per-request diff sees a partial world.** ExportVolume holds a `scanFor(vol)` Table, which contains only that volume. If it calls `DiffExports` against the full configfs scan, every other export looks extra and is torn down.
- **A transient scan sees nothing.** If a scan returns empty, for example because the pool is not imported yet or the parent was renamed, the desired set is ∅ and every owned export is deleted.
- **Shared nvmet objects.** `ports/<id>` and `hosts/<hostnqn>` are global. Teardown's "rmdir hosts/*" and "rmdir ports/<id> if empty" can remove a host object that a foreign subsystem's `allowed_hosts` links to (U: whether configfs refuses this with EBUSY or allows it), or race another daemon. Writing `addr_*` to an existing enabled port that someone else owns fails, and every pass retries it.
- **`reconcile-gc.md` says the opposite.** It requires startup to be non-destructive and unrecorded exports to be reported, not removed. The design deletes them at startup.

**Fix.**
- Make the owned prefix include an instance discriminator, a short hash of the parent dataset (or pool guid plus parent).
- Delete an owned export only when its `block_id` maps to a row whose record says "not exported". An export whose `block_id` maps to no row is reported, never deleted.
- `DiffExports` takes a scope (`[]block_id`). Per-request diffs only touch that scope.
- Add a circuit breaker: a desired set that is empty, or that drops more than N% compared with actual, aborts the run.
- Shared objects (ports, hosts) are refcounted from the actual table. They are created only when absent, `addr_*` is never written to a port that already exists, and they are removed only when the last link is ours.

## 7. HIGH: reconcile wedges on attributes the kernel will not change

**Scenario.** The design's step is "apply; re-scan; assert the diff is empty". Any mismatch on a write-once attribute leaves a write op that fails on every pass:
- `vpd_unit_serial` with `export_count>0` (EINVAL even when the value is identical)
- `attr_serial` after discovery
- `device_path`, `device_uuid` or `device_nguid` while the namespace is enabled (EBUSY)
- `control udev_path=` while the bdev is open (EEXIST)

Mismatches come from #1 (restored config), #2 (pool rename), a backstore made by another tool, or an older daemon version with different derivation. If a failed assert blocks startup, the daemon never serves. If it does not block startup, the failure is silent.

**Fix.**
- Tag each attribute in the diff as `mutable`, `mutableWhenDisabled`, or `immutable`.
- A mismatch on an immutable attribute does not produce a write. It produces either a drift condition (report only, while sessions exist) or a full teardown and recreate of that one export when the caller asks for it.
- For `mutableWhenDisabled`, the plan becomes `enable=0`, write, `enable=1`, and only on request, because it interrupts I/O.
- Reconcile is per export and best-effort. It returns `[]ExportStatus`, never one global assert.
- Drop `ReadFirst`. The diff already holds the scanned value, so it simply omits the write.

## 8. HIGH: lifecycle ordering against ZFS, and crash resurrection of revoked access

**Scenarios.**
- **DeleteVolume on an exported zvol.** IBLOCK opens the bdev with a holder (V: `target_core_iblock.c`, holder `ib_dev`). `zfs destroy -r` returns "dataset is busy", and the request comes back as an opaque error on every retry. `PlanDeleteVolume` never looks at exports.
- **Mutation order is unspecified.** Suppose DenyInitiator or UnexportVolume changes configfs first and crashes before clearing the record. Reconcile re-adds the revoked ACL. In the opposite case, ExportVolume writes configfs before the record and crashes, and the export becomes an "unrecorded" leftover.
- **ExpandVolume on an exported nvmet namespace.** The host sees no new size until `revalidate_size` is written (U: the attribute name on the target kernel). Under LIO, the initiator needs a rescan.

**Fix.**
- `PlanDeleteVolume` returns `FailedPrecondition "volume exported"` when the row's export record is set. CSI ControllerUnpublish must come first, and the volume is not unexported implicitly because that hangs on in-flight I/O.
- One rule for every export RPC: write the record (desired state) first, then converge configfs. A crash then always resumes toward the latest intent.
- The Expand planner appends an `OpCfsWrite revalidate_size=1` for nvmet rows.

## 9. MEDIUM: boot races, and initiators reconnecting to nothing

**Scenarios.**
- **The zvol device link is not there yet.** After `zpool import`, `/dev/zvol/...` links appear asynchronously through udev; `zvol_wait` exists because of this. A reconcile that starts immediately gets ENOENT on IBLOCK configure or nvmet enable for some volumes, and #7's global assert then fails the whole run.
- **The reconcile window is open-ended.** Between boot and the end of reconcile, initiators burn `replacement_timeout` (120 s by default) and `ctrl_loss_tmo`. Nothing bounds reconcile time. A slow pool scan with many volumes can exceed those timeouts, and clients then error out.

**Fix.**
- Wait per export for the device node, with a bounded retry such as `udevadm settle` or polling for the link.
- Resolve the device through `/dev/zd*`, read from `zvol` sysfs, so it does not depend on udev link names.
- Retry the run with backoff.
- Order with systemd `After=zfs-volumes.target` (`zvol_wait`).
- Measure reconcile time for 1k volumes against the initiator timeouts, and make it a test.

## 10. MEDIUM: fit with the data-oriented core

The design gets several things right: configfs is scanned once into `ActualTable`, the planners are pure, and `Op` stays a fat struct. These gaps remain:
- **`ReadFirst` puts a decision in `apply`.** Plans would stop being the whole truth, which breaks golden tests. Drop it, as in #7.
- **`apply`'s error tolerance is zfs-specific.** It checks `zfs.ErrExists` and `zfs.IsNotFound`. Configfs ops need their own errno mapping (EEXIST, ENOENT, EBUSY, EINVAL), and `zfsErr` mislabels them.
- **Shared configfs objects (np, ports, hosts) have no lock.** Per-volume keyed locks let two concurrent ExportVolumes race on `ports/<id>`. Fix: take the lock key `"cfs"` for any plan that touches a shared object, or adopt the single-writer actor from `multi-writer.md`, which also serializes reconcile against RPCs.
- **The join is not indexed.** Joining the actual table to the Table needs an index from guid (or `block_id`) to row in `NewTable`, built once, not a linear search per export.
- **`Guid` decoding.** Decode it from `-Hp` as uint64. The column is a native property, so the `Local()` check is not needed.
- **Missing sim rules.** Add the IBLOCK `control` rule (#4) and login-time ACL and np behaviour (#5) to the fake kernel, or the property test "apply then re-scan gives an empty diff" passes against a fiction.
