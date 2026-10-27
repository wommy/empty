# Block export v2: configfs ownership, minted identity, fail-closed ACLs

This revision of `block-export.md` resolves the three CRITICAL findings in `adv-block-design.md`:
- #1: distro restore units replay stale configuration.
- #2: guid-derived identity is unstable when it must be stable, and duplicated when it must be unique.
- #3: exports can be reachable before their ACL and auth are applied.

It also takes in HIGH findings #4 (IBLOCK `control`), #5 (enable last), #6 (ownership by join, not by name), #7 (frozen attributes) and #8 (record first), because the three critical fixes depend on them.

It is a design, not code. It keeps the endpoint's data-oriented shape:

```
lock → scan ZFS → Table ─┐
                         ├→ plan (pure) → []CfsOp (plain data) → apply (each op: own replay tolerance) → re-scan
lock → scan configfs → ActualTable ─┘
```

## Sources and marks

| Key | Source |
|---|---|
| `K:` | `github:torvalds/linux@21a7f44f` (master, 2026-10-25) |
| `R:` | `github:open-iscsi/rtslib-fb@6b68db5e` |
| `T:` | `github:open-iscsi/targetcli-fb@a2a26e28` |
| `N:` | `github:linux-nvme/nvmetcli@c7ac7019` (GitHub mirror; infradead was unreachable from the proxy) |
| `Z:` | `github:openzfs/zfs@a128b8a` (pinned clone) |
| `D:` | `github:democratic-csi/democratic-csi@69c7ebc` (pinned clone) |
| `AK:` | a claim already verified in `adv-block-kernel.md`, re-used without re-reading |

Every kernel or tool claim carries a citation. **U:** marks a claim that is unverified and must be checked in the KVM VM before code relies on it.

## Decisions at a glance

| # | Decision |
|---|---|
| D1 | The endpoint is the **only** writer of LIO and nvmet configfs on its host. It refuses to start while a restore unit (`target.service`, `nvmet.service`, …) is not masked, while a non-empty foreign saveconfig exists, or (by default) while foreign configfs objects exist. |
| D2 | Configfs is a **cache**. The desired state lives in ZFS user properties alone, and it is HMAC-sealed with a host key so that delegated `userprop` holders can't grant themselves access. Startup rebuilds in four phases: close, build, verify, open. |
| D3 | Identity is a minted 128-bit `block_id`, stored locally on the zvol with the guid at mint time. Every name and identifier is a pure function of it. A copy whose guid differs from the guid stamped on it is *unadopted* and is not exported until an explicit `AdoptBlockIdentity`. |
| D4 | Fail-closed means every export is built disabled. ACLs and auth come before the gate, then a read-back `Expect` gate, then enable. `generate_node_acls=0` and `allow_any_host=0` are written explicitly and asserted on every pass, and no code path can emit `1` for either. |
| D5 | CHAP and DH-HMAC-CHAP secrets are **derived** (HMAC of the host key), never stored. If the key is missing, CHAP-required exports stay closed. |
| D6 | The IBLOCK backstore binds its device through `control` = `udev_path=<dev>` (plus `readonly=0`, `exclusive=1`). The `udev_path` attribute is written only for read-back. |

---

## 1. Who owns configfs (finding #1)

### 1.1 Facts that force the decision

- **`targetctl restore` wipes everything first.** `target.service` runs `ExecStart=/usr/bin/targetctl restore` and `ExecStop=/usr/bin/targetctl clear` (`R: systemd/target.service:9-10`).
  - `targetctl restore` calls `RTSRoot().restore_from_file()` (`R: rtslib/targetctl.py:44-47`), whose default is `clear_existing=True` (`R: rtslib/root.py:478`).
  - `clear_existing` deletes **every** target and storage object, clears discovery auth, and removes every HBA (`R: rtslib/root.py:282-321`).
  - So a restore does not merge with our objects. It deletes them and replays whatever the file holds.
  - A missing file is not an error, and in that case nothing is cleared, because the file is opened before the clear (`R: rtslib/root.py:490-494`, `targetctl.py:48-50`).
- **`targetcli` snapshots our objects to disk on exit.** It defaults to `auto_save_on_exit: True` and saves for root on exit (`T: src/targetcli/targetcli_shell.py:62,317-319`). One admin session therefore writes our exports, ACLs and CHAP secrets into `/etc/target/saveconfig.json` (`R: rtslib/root.py:43`). The next `targetctl restore` replays that snapshot after it is stale.
- **nvmetcli behaves the same way.** `nvmet.service` runs `ExecStart=/usr/sbin/nvmetcli restore` and `ExecStop=/usr/sbin/nvmetcli clear` (`N: nvmet.service`). `restore_from_file` defaults to `clear_existing=True` (`N: nvmet/nvme.py:430-446`). `clear_existing` deletes every port, subsystem and host (`N: nvmet/nvme.py:370-380`). The default file is `/etc/nvmet/config.json` (`N: nvmet/nvme.py:33`).
- **The baseline depends on these units.** democratic-csi drives `targetcli`/`nvmetcli` and relies on their saved files for persistence (`D: src/driver/controller-zfs-generic/index.js:274-292,458-489`). Its example configuration is fail-open:
  - `generate_node_acls: 1` and `authentication: 0` (`D: examples/zfs-generic-iscsi.yaml:63,66`)
  - `allow_any_host: 1` (`D: examples/zfs-generic-nvmeof.yaml:82`)
- **Configfs has no persistence of its own** (`AK: fs/configfs/mount.c:100`). At boot, whatever runs first defines what is exposed.

The tools act on the whole configfs tree, so they cannot be made to coexist with a second writer by giving each writer its own names. `targetctl clear` and `nvmetcli clear` take no scope. The only safe arrangement is a single owner.

### 1.2 Decision

- **The endpoint is the single configfs owner on its host.** Supported deployments mask the restore units. The endpoint's own unit declares:
  ```
  Requires=sys-kernel-config.mount
  After=sys-kernel-config.mount zfs-volumes.target network-online.target
  Conflicts=target.service nvmet.service
  ```
  `zfs-volumes.target` is ordered after `zvol_wait` (`Z: etc/systemd/system/zfs-volumes.target.in:3`, `zfs-volume-wait.service.in:11`). `Conflicts=` makes a manual `systemctl start target` stop the endpoint first rather than run beside it.
- **"Family" objects.** These are objects whose name matches the endpoint's naming scheme (§2.4): IQN `<iqn_base>:<32 hex>`, NQN `<nqn_base>:<32 hex>`, backstore `ze_<32 hex>`. Several endpoint instances (one per parent dataset, as the README already decides) share the family on one host.
- **An instance acts only on family objects it owns.** It owns an object when the object's `block_id` (parsed from its name) is a row in its own `Table`. A family object whose `block_id` is not in the Table belongs to a sibling or is an orphan. It is reported, never modified or deleted (finding #6). Ownership therefore comes from a join, not from a name prefix. A transient empty scan can't delete anything, because deletion needs a row that *positively* says "unexported" (§2.5, §4.6).
- **Host-wide configfs lock.** Every configfs scan and every plan application holds `flock(/run/lock/zfs-endpoint-configfs.lock)`, in addition to the existing per-parent lock. Two sibling instances never interleave writes to shared objects (nvmet ports and hosts). This replaces finding #10's per-request `"cfs"` key.
- **Mode `configfs_owner: exclusive` (the default) or `shared`.** In `exclusive`, any foreign object blocks startup. In `shared`, foreign objects are tolerated but never touched, and exports that would need a foreign shared object (an nvmet port with the same address, or a `hosts/<nqn>` that carries a DH-CHAP key we did not derive) are `Blocked`. Both modes require the units and saveconfig checks below. `shared` exists for hosts that also run hand-made LIO targets. It is not the default because a foreign writer can still run `clear` (Self-review R1).

### 1.3 Foreign-owner detection (preflight)

This runs at startup under both locks, before any configfs write. Any failure is a typed `StartupError{Code: FailedPrecondition}`. The daemon exits non-zero and serves nothing. This is fail-closed: no exports is better than exports rebuilt beside a foreign owner.

| Check | How | Pass | On fail |
|---|---|---|---|
| P1 configfs mounted | `statfs("/sys/kernel/config")` magic `0x62656570` | mounted | refuse |
| P2 fabric modules | `target/` exists (iSCSI enabled), `nvmet/` exists (NVMe enabled). `modprobe` is the unit's job, not the daemon's | present | refuse that protocol; others continue |
| P3 restore units | `systemctl show -p LoadState,ActiveState <unit>` for each unit in `foreign_units` (default `target.service`, `rtslib-fb-targetctl.service`, `nvmet.service`). The Debian unit name is **U:** | `LoadState=masked` or `not-found`, and `ActiveState=inactive` | refuse, naming the unit |
| P4 saveconfig files | parse `/etc/target/saveconfig.json` and `/etc/nvmet/config.json` (paths configurable) | absent, or empty of `storage_objects`/`targets` and `subsystems`/`ports`/`hosts` | family objects only: **quarantine** (rename to `*.ze-quarantine-<unix>`, fsync the directory, log at WARN) and continue. Any foreign object: refuse |
| P5 foreign configfs objects | scan `target/core/*/*`, every fabric dir under `target/` (`iscsi`, `loopback`, `vhost`, `tcm_fc`, …), `nvmet/subsystems/*`, `nvmet/ports/*`, `nvmet/hosts/*` | `exclusive`: none outside the family (hosts referenced only by family subsystems count as family). `shared`: always passes | `exclusive`: refuse and list the objects |
| P6 device sharing | for every configfs object, foreign or family, resolve its backing device (IBLOCK `info` Major/Minor, `K: drivers/target/target_core_iblock.c:676-679`; nvmet `device_path` → `stat().Rdev`) | no two objects on one zvol device, and no foreign object on a zvol under our parent | `Blocked(DeviceShared)` for that export. It is never built, because nvmet opens without a holder (`K: drivers/nvme/target/io-cmd-bdev.c:105-106`) and does not exclude a second exporter |

Quarantine (P4) is what defuses the stale-replay scenario. Even if a masked unit is later unmasked, or someone runs `targetcli restoreconfig`, the file that would replay old ACLs is gone. Quarantine is a rename, not a delete, so no admin data is lost.

**At runtime**, a drift pass runs every `drift_interval` (default 60 s) and before every export RPC. It re-runs P3 to P6.
- If a foreign object or a new saveconfig appears: quarantine the file (P4 rules), raise the `ForeignConfigfsWriter` condition, and refuse new exports until it clears. Existing exports are left serving, because tearing them down on suspicion is an outage.
- If our own objects disappear (someone ran `targetctl clear`): rebuild through the startup algorithm.

### 1.4 Startup rebuild (desired state from ZFS → configfs)

```
rebuild():
  0. take per-parent flock and host configfs flock
  1. preflight P1..P6                                  — any refusal: exit, touch nothing
  2. t   := scan ZFS (one zfs get -Hp; Row gains Guid, BlockID, BlockIDGuid, Export)
     if parent unavailable: exit Unavailable          — never act on a partial world
  3. a   := scan configfs → ActualTable               — plain data; secrets read as root
  4. for each row r with r.Export != nil:
       st[r] := classify(r, t, a, key)                — §2.5: Ready | Blocked(reason)
  5. PHASE CLOSE  — for every family object o owned by a row r (join on block_id):
       if st[r] != Ready, or r.Export.On == false, or unsafe(o), or wrongDevice(o, r):
         apply(planClose(o))                          — tpg enable=0; nvmet port unlink
  6. PHASE BUILD  — for each r with st[r]==Ready && r.Export.On:
       apply(planBuild(r, a))                         — everything except np/port links and enable
  7. PHASE VERIFY — for each built r: apply(planExpect(r))   — read-back gate (Expect ops)
  8. PHASE OPEN   — for each r that passed 7: apply(planOpen(r))   — np mkdir, tpg enable=1, port link
  9. PHASE GC     — owned objects of rows whose record says On=false: apply(planTeardown)
 10. a' := re-scan; statuses := diff(t, a')           — []ExportStatus, never one global assert
 11. mark export RPCs ready (pillar's "gate until restore", P: server_export_restore.go:24-43)
```

- **Each export applies its own plan.** A failure stops that export's plan at the failing op and records `ExportStatus{Phase, Op, Errno}`. Other exports continue (finding #7). A failed build or verify means step 8 never runs for that export, so it stays closed.
- **Close runs before build for every export.** A stale restored ACL is cut before anything else happens, including when a later step fails.
- **Open runs last and all together.** This shrinks finding #5's window: the first listener to come up makes initiators of not-yet-built targets receive "target not found". The kernel shares one listening socket per `ip:port`, and a TPG that is not active is skipped during login lookup (`K: drivers/target/iscsi/iscsi_target_tpg.c:131-138`).
- **Device nodes are waited for per export.** If the zvol node is missing (`ENOENT` on `control`/`enable`), poll for up to `device_wait` (default 30 s). Then mark that export `Blocked(DeviceMissing)` and retry it in the drift pass (finding #9).

### 1.5 What is *not* a source of truth

| Store | Role |
|---|---|
| ZFS user properties on the zvol | **The** desired state: identity, and the export record with its ACL list |
| Host key file `/etc/zfs-endpoint/block.key` (0600 root) | Configuration, the same on both HA heads. It holds no per-export facts, so it is not a second source of truth. It seals records (§2.3) and derives secrets (§3.3) |
| configfs | A cache, rebuilt from the two above. Read back only to plan and verify |
| `/etc/target/saveconfig.json`, `/etc/nvmet/config.json` | Never written. Quarantined if found (P4) |

---

## 2. Identity (finding #2)

### 2.1 Why the zfs `guid` is unusable, with corrections

- **Every new dataset gets a random guid.** This covers create, clone, and a full receive into a new name (`Z: module/zfs/dsl_dataset.c:1214-1215` in `dsl_dataset_create_sync_dd`). A snapshot also gets a fresh one (`:1779-1780`). So DR and replication (`zfs send -R | zfs recv` into another pool) change the identity of every volume. Initiators, multipath WWIDs and PV publish contexts then point at nothing. This is the original finding, and it holds.
- **Correction to the brief: rollback and incremental receive keep the head's guid.**
  - Rollback builds a `%rollback` clone and calls `dsl_dataset_clone_swap_sync_impl` (`Z: dsl_dataset.c:3441-3449`). Incremental receive uses the same swap (`Z: module/zfs/dmu_recv.c:5245`).
  - The swap exchanges byte counts and deadlists (`Z: dsl_dataset.c:4484-4502`) but has no `SWITCH64` of `ds_guid`.
  - So the head guid survives both (inference from code; **U:** confirm with a real-pool test, which is T-ID-7 below).
  - The guid is therefore not a problem on rollback. It is a problem on cross-pool moves.
- **Copying the identity into a property duplicates it.** `zfs send -p`/`-R` carries local user properties. A backup received next to the original, or a DR replica imported while the primary still serves, would carry the same identity. This is the "naive fix collides" half of finding #2, and the design must refuse it, not just survive it.

### 2.2 Properties (own namespace, local only)

New properties live under `zfs-endpoint:`, not under the baseline's `democratic-csi:` prefix. democratic-csi never reads them, and a future baseline property can't collide with them.

| Property | Value | Written |
|---|---|---|
| `zfs-endpoint:block_id` | 32 lowercase hex chars, 128 bits from `crypto/rand` | once, at first export |
| `zfs-endpoint:block_id_guid` | decimal `guid` of this zvol at mint or adopt time | at mint, `AdoptBlockIdentity` |
| `zfs-endpoint:block_export` | `v1 on=<0\|1> proto=<iscsi\|nvme-tcp> auth=<chap\|chap-mutual\|dhchap\|dhchap-bidi\|none> gen=<n> hosts=<id>[,<id>…] mac=<32 hex>` | every export RPC (record first, §3.6) |

- **Mint and record land in one `zfs set` call** (`internal/zfs/zfs.go:278-292` passes every pair to one `zfs set`).
  - User properties go to the generic nvlist (`Z: module/zfs/zfs_ioctl.c:3259`) and are written by one `dsl_sync_task` (`Z: module/zfs/dsl_prop.c:1027-1044`). That makes the three properties atomic.
  - **U:** that the CLI sends all pairs in one ioctl. If it does not, a half-written mint is caught by the classification rule "block_id without valid record → Blocked", and nothing is exported under it.
- **Only local values count.** This matches `decodeColumns`'s existing `Local()` rule (`internal/endpoint/table.go:146-152`). A clone does not carry the origin's local properties: the endpoint's clone path sets its own properties explicitly (`plan.go` `PlanCreateVolume`). **U:** that `zfs clone` never copies the origin's local user properties; T-ID-6 tests it.
- **Size.** User property values are limited to 8192 bytes (`Z: man/man7/zfsprops.7:2440`). With IQNs of at most 223 bytes, a record holds at least 30 initiators. More than that returns `InvalidArgument`.

### 2.3 Sealing the record

`mac = hex(HMAC-SHA256(K, "ze/rec/v1\0" ‖ block_id ‖ "\0" ‖ block_id_guid ‖ "\0" ‖ canonical(record without mac)))[:32]`

- **Delegated `userprop` holders can't add their own IQN** (finding #3, first bullet). They can't produce a valid MAC without `K`.
- **The record is bound to the zvol's guid.** A received copy fails `block_id_guid == guid`. If someone also rewrites `block_id_guid`, the MAC fails.
- **The name is deliberately not covered.** `block_id`, and therefore every identifier, survives `zfs rename`.

### 2.4 Derivations (pure functions of `B` = the 16 bytes of `block_id`, `h` = its 32 hex chars)

| Identifier | Derivation | Length / limit | Bits from B |
|---|---|---|---|
| iSCSI target IQN | `<iqn_base>:` + `h`, e.g. `iqn.2026-10.media.technomad:ze.3f1c…0918` | ≤ 223 (`ISCSI_IQN_LEN` 224 incl. NUL, `K: include/target/iscsi/iscsi_target_core.h:28`). With `iqn_base` ≤ 190 it fits | 128 |
| NVMe subsystem NQN | `<nqn_base>:` + `h` | ≤ 223 (`NVMF_NQN_SIZE`, `K: include/linux/nvme.h:18`) | 128 |
| LIO backstore name | `ze_` + `h` (35 chars) | under `TARGET_CORE_NAME_MAX_LEN` (**U:** the value, 64 expected; used at `K: target_core_configfs.c:3638`) | 128 |
| LIO HBA dir | `iblock_<4194304 + u24(B[0:3])>` | `kstrtoul` (`K: target_core_configfs.c:3666`) | 24 (collisions harmless, see below) |
| SCSI unit serial (VPD 0x80) | `h` | 32 < 254 (`INQUIRY_VPD_SERIAL_LEN`, `K: include/target/target_core_base.h:46`; `-EOVERFLOW` at ≥ 254, `K: target_core_configfs.c:1700-1704`) | 128 |
| SCSI NAA (VPD 0x83) | kernel-derived: `6` ‖ company_id ‖ first 25 hex digits of the serial (see below) | 16 bytes | **100** |
| LIO company_id | `0x001405`, written explicitly | 24 bits | — |
| NVMe `attr_serial` | `h[:20]` | ≤ 20 (`NVMET_SN_MAX_SIZE`, `K: drivers/nvme/target/nvmet.h:33`; check `configfs.c:1407`) | 80 |
| NVMe `device_nguid` | `B` (as `%pUb`-style hex) | 16 bytes, parser `K: configfs.c:658-672` (via AK) | 128 |
| NVMe `device_uuid` | `B` with version nibble set to 8 and variant `10` (RFC 9562 UUIDv8) | 16 bytes | 122 |
| NVMe EUI-64 | **not set.** nvmet has no EUI-64 attribute. Identify Namespace copies only the NGUID (`K: drivers/nvme/target/admin-cmd.c:877`), and the descriptor list emits UUID, NGUID and CSI only (`:1011-1027`). We also own no IEEE OUI to put in one | — | — |

- **How the NAA is built, and why the serial must be hex.** LIO does not let you set a WWN. It builds the NAA IEEE Registered Extended designator from the unit serial:
  - nibble 0 is `6`, the next 24 bits are `company_id`, and the remaining 25 nibbles come from the serial's hex digits. Non-hex characters are skipped (`K: drivers/target/target_core_spc.c:176-217`, the loop `for (cnt = off + 13; *p && off < cnt; p++)` with `hex_to_bin`).
  - The designator is emitted only when a serial was written through configfs (`DF_EMULATED_VPD_UNIT_SERIAL`, set only at `K: target_core_configfs.c:1727` and tested at `target_core_spc.c:245`).
  - **Without our write there is no NAA at all.** The kernel does not generate a serial (this resolves `adv-block-kernel.md` #9: rtslib generates one in userspace, `R: rtslib/tcm.py:86`).
  - So the serial is the bare 32-hex `h`. A UUID with dashes would also work, since dashes are skipped, but plain hex makes the mapping obvious.
- **Test vector.** For `block_id = 3f1c9a0b5d2e47f8a6b4c3d2e1f00918`:
  - NAA `60014053f1c9a0b5d2e47f8a6b4c3d2e`. Linux shows it as `/dev/disk/by-id/wwn-0x60014053f1c9a0b5d2e47f8a6b4c3d2e`, and the multipath WWID is `3600140…`.
  - NVMe serial `3f1c9a0b5d2e47f8a6b4`.
  - UUID `3f1c9a0b-5d2e-87f8-a6b4-c3d2e1f00918`.
  - HBA `iblock_8330394`.
- **company_id.** The default is `0x001405` (`K: drivers/target/target_core_device.c:783`). We write it explicitly before export, because it is frozen afterwards (`K: target_core_configfs.c:1653-1657`). Our WWNs then don't change if a kernel ever changes the default. Its show format is `%#08x` (`:1623`), so read-back parses it as a number.
- **HBA collisions are harmless.** An HBA is only a grouping level and can hold several devices. rtslib allocates one HBA per storage object from `0..1048575` (`R: rtslib/tcm.py:1002-1036`), so our range starts above that and won't collide with targetcli in `shared` mode. Two of our backstores in one HBA are legal. Teardown's `rmdir` of the HBA then returns `ENOTEMPTY`/`EBUSY` and is tolerated (`OkBusy`).

**Collision analysis** (birthday bound p ≈ n²/2^(k+1), where n is the number of volumes visible in one initiator's multipath domain):

| Identifier | k | n = 10³ | n = 10⁴ | n = 10⁶ |
|---|---|---|---|---|
| block_id, IQN, NQN, nguid | 128 | 1.5e-33 | 1.5e-31 | 1.5e-27 |
| NAA (shared with every LIO array on the SAN that uses company_id 0x001405) | 100 | 3.9e-25 | 3.9e-23 | 3.9e-19 |
| NVMe serial (only needs to be unique per subsystem, and each volume is its own subsystem) | 80 | 4.1e-19 | 4.1e-17 | 4.1e-13 |

- **Random collisions are negligible.** The real collision source is *copying* (send -p), and that is handled by classification, not by bits.
- **Foreign LIO arrays on the same SAN.** rtslib's random uuid4 serials share our company_id, which leaves the 100-bit space. uuid4 fixes the version nibble at position 13, inside the 25 used digits, so their space is 96 bits. A cross-array collision is still at most about n²/2^97 and is ignored.

### 2.5 Classification (pure, from `Table` + `ActualTable` + key)

| State | Condition | Effect |
|---|---|---|
| `Unminted` | no `block_id`, no record | first `ExportVolume` mints (one `zfs set`) |
| `Ready` | block_id valid hex, record MAC valid, `block_id_guid == guid`, block_id unique in `Table`, no family object of the same block_id on another device (P6), key present (if auth needs it), device node present | build/open allowed |
| `Blocked(Unadopted)` | `block_id_guid != guid` (received copy, DR replica) | closed. `AdoptBlockIdentity(vol)` re-stamps guid and MAC and keeps `block_id`, so the WWN is unchanged (failover). `ReissueBlockIdentity(vol)` mints a new `block_id` (a copy that must coexist). Refused while any family object for the old id is enabled |
| `Blocked(Duplicate)` | another row in this Table has the same block_id | **both** rows are closed until one is reissued. Table gains a `byBlockID` index built once in `NewTable` (finding #10) |
| `Blocked(Tampered)` | MAC invalid, block_id malformed, or block_id present without a valid record | closed and reported. Never auto-reminted, because re-minting the identity of a volume that was once exported would orphan its initiators |
| `Blocked(NoKey)` | key file missing or unreadable | closed. Every CHAP export fails closed |
| `Blocked(DeviceShared / DeviceMissing / WrongDevice)` | P6, ENOENT, or an owned backstore whose device rdev ≠ `stat(/dev/zvol/<row>)` | closed. `WrongDevice` is torn down and rebuilt, which fixes finding #1's "wrong volume served" case: name equality is never trusted |

- **Adopt and Reissue are explicit RPCs** called by failover runbooks. Backups never call them.
- **The design does not try to stop two heads exporting the same pool.** That is HA fencing (README: `multihost` + STONITH). Self-review R6 covers it.

---

## 3. Fail-closed ACLs and auth (finding #3)

### 3.1 Kernel facts the ordering rests on

- **A new TPG is inactive.** `mkdir tpgt_N` leaves it `TPG_STATE_INACTIVE` (`K: drivers/target/iscsi/iscsi_target_tpg.c:237`). Logins only find `ACTIVE` TPGs (`:131-138`), and a TPG that is not active is not listed in SendTargets (`K: drivers/target/iscsi/iscsi_target.c:3403-3407`, with `tpg_enabled_sendtargets` defaulting to 1, `iscsi_target_core.h:69`).
- **TPG defaults are safe, but we write them anyway.** `authentication=1`, `generate_node_acls=0`, `cache_dynamic_acls=0`, `demo_mode_write_protect=1`, `demo_mode_discovery=1` (`K: iscsi_target_core.h:49-62`, applied at `iscsi_target_tpg.c:207-214`). The baseline overwrites them to fail-open values (§1.1), and any root tool can do the same, so defaults are not relied on. We write them explicitly and assert them on every pass.
- **Without an ACL, login is refused.** With `generate_node_acls=0`, `core_tpg_check_initiator_node_acl` returns NULL (`K: drivers/target/target_core_tpg.c:242-253`), and the login fails as "not authorized" (`K: iscsi_target_nego.c:1292-1297`).
- **When auth is required:**
  - for a known ACL, it follows the ACL's `attrib/authentication`, or the TPG's when the ACL's value is `-1` (inherited, the default) (`K: iscsi_target_nego.c:811-845`; default `iscsi_target_nodeattrib.c:33`)
  - for an unknown ACL, auth is always required (`:826-829`)
  - `AuthMethod=None` is rejected when auth is required (`:914-923`)
- **CHAP without credentials fails closed.** If `userid` or `password` is unset, CHAP fails (`K: iscsi_target_auth.c:149-153`). An ACL whose credentials are not yet written therefore cannot log in when auth is required.
- **Secrets are stored verbatim.** The store is `snprintf(auth->name, …, "%s", page)` (`K: iscsi_target_configfs.c:387`), so a trailing `\n` would become part of the secret. We write values with **no newline**. Show appends `\n`, so read-back trims exactly one.
- **CHAP attributes need `CAP_SYS_ADMIN` to read or write** (`K: iscsi_target_configfs.c:371-372,383-384`; TPG `:826-827,838-839`). This **corrects finding #3's** "any local user can read them": the file mode is 0644 (`K: include/linux/configfs.h:135-137`), but show returns `-EPERM` without the capability. Privileged containers can still read them.
- **nvmet DH-CHAP keys are world-readable.** `hosts/<nqn>/dhchap_key` show has no capability check (`K: drivers/nvme/target/configfs.c:2219-2233`), and the mode is 0644. Finding #3's claim holds for nvmet.
- **Removing a LIO ACL kills its sessions** (`core_tpg_del_initiator_node_acl` → `target_shutdown_sessions`, `K: target_core_tpg.c:345-355`). Disabling a TPG force-releases its sessions (`K: iscsi_target_configfs.c:1104-1106` → `iscsi_target_tpg.c:346-371`).
- **nvmet subsystems start closed.** A new subsystem is `kzalloc`'d, so `allow_any_host=false`, and it gets a random serial (`K: drivers/nvme/target/core.c:1871-1878`). A host is allowed only when `allow_any_host` is set or the host is linked (`K: core.c:1551-1569`), and that check also filters discovery (`K: discovery.c:152`).
- **nvmet DH-CHAP enforcement:**
  - an allowed host *without* a `dhchap_key` connects **without** authentication (`K: auth.c:170-172`)
  - a host that is not in the list fails (`:152-155`)
  - `allow_any_host` skips auth entirely (`:143-144`)
- **Unlinking an nvmet host does not disconnect its controllers.** `allowed_hosts` unlink only removes the link and posts a discovery change (`K: configfs.c:1246-1267`). Unlinking `ports/<p>/subsystems/<nqn>` does disconnect them (`nvmet_port_del_ctrls`, `K: configfs.c:1184-1185`).
- **The first port link enables the port, and removing the last disables it** (`K: configfs.c:1150-1151,1188-1189`).

### 3.2 Invariants

These are checked by the test harness after **every single op** (§6).

- **I1:** No family TPG is `enable=1` unless all of these hold:
  - `generate_node_acls=0`, `cache_dynamic_acls=0`, `demo_mode_discovery=0`, `demo_mode_write_protect=1`
  - `authentication` = policy
  - every ACL dir is in the record's host list
  - each such ACL has `userid` and `password` set when policy ≠ `none` (`*_mutual` too for mutual)
- **I2:** No family nvmet subsystem is linked into any port unless `attr_allow_any_host=0`, its `allowed_hosts` ⊆ record hosts, and (when policy is `dhchap*`) every linked host has a non-empty `dhchap_key`.
- **I3:** A family LUN is never linked to a backstore whose device rdev differs from its row's zvol.
- **I4:** The planner has no code path that emits `generate_node_acls=1`, `cache_dynamic_acls=1` or `attr_allow_any_host=1`. A static test greps the generated op tables for these values.

### 3.3 Policy and secrets

- **`auth`** is per export and comes from the request, the StorageClass, or a default. `none` is accepted only if the endpoint config has `allow_unauthenticated_block: true` (default false). Otherwise it returns `InvalidArgument`.
- **iSCSI CHAP:**
  - `userid = "ze-" + h[:16]`
  - `password = b64url(HMAC-SHA256(K, "ze/chap/v1\0" ‖ B ‖ "\0" ‖ initiatorIQN ‖ "\0" ‖ gen))[:24]`, which is 144 bits and under `MAX_PASS_LEN` 256 (`K: iscsi_target_core.h:740`)
  - mutual: `userid_mutual = h[:16]+"-t"`, with `password_mutual` from the `ze/chap-mutual/v1` domain
- **NVMe DH-CHAP:**
  - per **host**, not per (host, subsystem), because `hosts/<nqn>` is global: `key = HMAC(K, "ze/dhchap/v1\0" ‖ hostnqn ‖ "\0" ‖ hostgen)[:32]`
  - formatted `DHHC-1:00:<b64(key ‖ crc32le(key))>:`
  - the kernel parses the `DHHC-1:%hhd:` prefix (`K: drivers/nvme/target/auth.c:40-43`). The rest of the format is **U:**
  - requires `CONFIG_NVME_TARGET_AUTH` (`K: configfs.c:2218`). If the attribute file is absent, `dhchap*` exports are `Blocked(NoAuthSupport)`
- **Delivery.** `ExportVolume`/`AllowInitiator` responses carry the derived credentials to the caller over the owner-only socket. The CSI front end puts them in the node-stage secret path. Nothing is persisted.
- **Rotation.** Bump `gen` in the record. That is a record write, then a configfs write. New logins use the new secret, and existing sessions continue.

### 3.4 Order per export (summary; the full op lists are in §4)

```
LIO   build : backstore(control → udev_path → company_id → serial → enable)
            → iqn → tpgt (inactive) → TPG security attribs → lun_0 + link
            → ACL dirs → ACL attrib/authentication=1 → ACL auth creds → mapped LUNs
      verify: Expect(all of I1, I3)
      open  : np mkdir → tpgt enable=1
nvmet build : subsys → allow_any_host=0 → serial → ns(device_path, uuid, nguid, enable)
            → hosts (dhchap_key first) → allowed_hosts links
      verify: Expect(all of I2)
      open  : port (create if absent) → port link
```

The serial must be written before the LUN link, because `export_count>0` freezes it (`AK: target_core_configfs.c:1711-1716`). The nvmet serial must be written before any port link, because `subsys_discovered` freezes it (`AK: configfs.c:1401-1404`, set on Identify, `K: admin-cmd.c:660`).

### 3.5 Live changes on an open export

Each step of these sequences leaves the export closed or narrower:

- **Allow an iSCSI initiator:** `mkdir acl` (login now fails on missing creds) → `attrib/authentication=1` → creds → `mkdir lun_0` + link (the LUN becomes visible last).
- **Deny an iSCSI initiator:** unlink the mapped LUN (access to the LUN ends) → `rmdir acls/<i>/lun_0` → `rmdir acls/<i>` (the kernel kills its sessions).
- **Allow an NVMe host:** `mkdir hosts/<h>` → `dhchap_key` → link `allowed_hosts/<h>`.
- **Deny an NVMe host:**
  - unlink `allowed_hosts/<h>` (new connects fail)
  - for each port, unlink `ports/<p>/subsystems/<nqn>` (disconnects every controller of this subsystem on that port)
  - relink the ports
  - `rmdir hosts/<h>` with `OkBusy`, since configfs refuses removal while another subsystem links it (`AK: fs/configfs/dir.c:520`)
  - This costs other allowed hosts of the same volume a reconnect (Self-review R4).
- **Unexport:** record `on=0` first → close (iSCSI `tpgt enable=0`, which kills sessions; nvmet port unlink) → teardown.

### 3.6 Write order between ZFS and configfs

Every export RPC follows one rule: **record first, then converge** (finding #8).
- ZFS `zfs set` of the record (MAC included) happens first.
- Configfs converges second.
- A crash between them resumes toward the newest intent on restart or on the next RPC. The record holds the *intent* (deny), so a crash cannot resurrect a revoked ACL.
- `PlanDeleteVolume` refuses with `FailedPrecondition("volume exported")` while `on=1` or while any owned family object exists for the block_id. IBLOCK would hold the device exclusively anyway, so `destroy` would fail with "busy" (`K: target_core_iblock.c:67,98,121-125`).

### 3.7 Partial failure and restart

- **`apply` stops a plan at its first failing op** (as `internal/endpoint/apply.go:12-46` does today).
  - Because enable and port links sit after the `Expect` gate in the same plan, any failure in build or verify leaves the export closed.
  - A failure *during* open can leave it half open (iSCSI np up, TPG still disabled), which is closed.
  - A failure in the middle of the nvmet port links leaves it reachable on a subset of ports, but every reachable port has already passed I2.
- **After a daemon crash, configfs is intact** (it is kernel memory). The next start runs §1.4: close anything unsafe first, then converge.
- **After a reboot, configfs is empty.** The rebuild produces everything from ZFS. Restore units are masked, so nothing else races us.
- **When the record and configfs disagree, the record wins.** Configfs objects the record does not list are removed (owned only). Objects the record lists are added.

---

## 4. The configfs op list (plain data)

```go
type CfsKind uint8 // Mkdir, Rmdir, Write, Symlink, Unlink, Expect

type Tol uint8 // bit set
const (
    OkExists  Tol = 1 << iota // EEXIST is done
    OkMissing                 // ENOENT is done
    OkBusy                    // ENOTEMPTY/EBUSY is done (shared container still in use)
)

type CfsOp struct {
    Kind   CfsKind
    Path   string // relative to /sys/kernel/config
    Value  string // Write: bytes written, no trailing newline. Symlink: link target. Expect: wanted value
    Match  Match  // Expect: Exact | TrimNL | Prefix("T10 VPD Unit Serial Number: ") | Hex | RdevOf(path)
    Tol    Tol
    Secret bool   // redact Value when printing; golden files show "<secret:chap/…>"
    Rule   string // the kernel citation that forces the position, for reviewers and golden output
}
type CfsPlan []CfsOp
```

- **`CfsOp` is its own fat struct, not new `OpKind`s on the ZFS `Op`.** It has a different errno vocabulary (finding #10: `zfs.ErrExists`/`zfsErr` would mislabel `EEXIST`/`EBUSY`), and `apply` stays a flat switch per type.
- **The planner decides everything from the scan.** For example, it omits a write whose scanned value already matches. There is no `ReadFirst` (finding #7). `Expect` is the only op that reads, and it is a gate, not a decision: a mismatch is an error that stops the plan.
- **Frozen attributes** (`vpd_unit_serial`, `company_id` after export; `attr_serial` after discovery; `device_*` while the namespace is enabled; `control udev_path=` while the bdev is open):
  - the planner emits them only when the scan shows they are still writable
  - on a mismatch it emits a close plus `Blocked(IdentityDrift)`, never a write
  - for `WrongDevice` it tears the export down

Placeholders: `H` = `core/iblock_<n>`, `S` = `H/ze_<h>`, `T` = `target/iscsi/<iqn>/tpgt_1`, `dev` = `/dev/zvol/<pool>/<ds>`, `i` = initiator IQN, `X` = `nvmet/subsystems/<nqn>`, `hn` = host NQN, `p` = port id.

### 4.1 LIO build (phase BUILD, export closed throughout)

| # | Op | Path | Value | Tol | Why here / kernel rule |
|---|---|---|---|---|---|
| L1 | Mkdir | `target/core/iblock_<n>` | | OkExists | HBA; shared and harmless (§2.4) |
| L2 | Mkdir | `S` | | OkExists | a block_id collision is caught earlier by classification |
| L3 | Write | `S/control` | `udev_path=<dev>,readonly=0,exclusive=1` | — (EEXIST only if the bdev is open; the planner skips L3 when the scan shows it configured) | IBLOCK learns its device **only** here (`K: target_core_iblock.c:601-616`). Missing: enable fails `-EINVAL` "Missing udev_path=" (`:102-104`). Unknown tokens are ignored (`:651-652`), so `exclusive=` is harmless on kernels without it. `exclusive` already defaults to true (`:67`) |
| L4 | Write | `S/udev_path` | `<dev>` | — (idempotent: plain `snprintf`, no state check, `K: target_core_configfs.c:2432-2461`) | read-back and display only; does not bind |
| L5 | Write | `S/wwn/company_id` | `0x001405` | — | frozen once exported (`K: target_core_configfs.c:1653-1657`) |
| L6 | Write | `S/wwn/vpd_unit_serial` | `<h>` | — (frozen, `AK: :1711-1716`) | must precede L11. Creates the NAA (§2.4) |
| L7 | Write | `S/enable` | `1` | OkExists (`-EEXIST` = already configured, `AK: target_core_device.c:965-968`) | opens the bdev with an exclusive holder (`K: target_core_iblock.c:98,121-125`); must precede L11 (`AK: target_core_fabric_configfs.c:639-642`) |
| L8 | Mkdir | `target/iscsi/<iqn>` | | OkExists | |
| L9 | Mkdir | `T` | | OkExists | born INACTIVE (`K: iscsi_target_tpg.c:237`) |
| L10a–f | Write | `T/attrib/{generate_node_acls,cache_dynamic_acls,demo_mode_write_protect,prod_mode_write_protect,demo_mode_discovery,authentication}` | `0,0,1,0,0,<1 unless auth=none>` | — (same value accepted) | explicit, never trusted to defaults. `demo_mode_discovery=0` hides the target from SendTargets for initiators without an ACL (`K: iscsi_target.c:3390-3399`) |
| L10g | Write | `T/param/AuthMethod` | `CHAP` (or `None` iff auth=none) | — | enable also forces CHAP when authentication=1 (`K: iscsi_target_tpg.c:318-328`); written so read-back is exact. **U:** param write syntax |
| L11a | Mkdir | `T/lun/lun_0` | | OkExists | `AK: rtslib/target.py:533` |
| L11b | Symlink | `T/lun/lun_0/ze` | → `S` | OkExists + Expect L-V3 | needs a configured device and an unbound LUN (`AK: target_core_fabric_configfs.c:620-654`). `export_count++`, so L5/L6 are now frozen |
| L12 *(per i)* | Mkdir | `T/acls/<i>` | | OkExists | |
| L13 | Write | `T/acls/<i>/attrib/authentication` | `1` (`0` iff auth=none) | — | explicit, not `-1`/inherited (`K: iscsi_target_nego.c:841-844`) |
| L14 | Write ×2 or ×4 | `T/acls/<i>/auth/{userid,password[,userid_mutual,password_mutual]}` | derived; **Secret** | — (overwrite is idempotent) | CHAP fails without them (`K: iscsi_target_auth.c:149-153`) |
| L15a | Mkdir | `T/acls/<i>/lun_0` | | OkExists | |
| L15b | Symlink | `T/acls/<i>/lun_0/ze` | → `T/lun/lun_0` | OkExists + Expect | needs the TPG LUN linked first (`AK: target_core_fabric_configfs.c:88-91`) |
| L15c | Write | `T/acls/<i>/lun_0/write_protect` | `0` | — | **U:** attribute name |

### 4.2 LIO verify and open

| # | Op | Path | Value / Match |
|---|---|---|---|
| L-V1 | Expect | `T/attrib/generate_node_acls`, `cache_dynamic_acls`, `demo_mode_discovery`, `demo_mode_write_protect`, `authentication` | `0,0,0,1,<policy>`, TrimNL |
| L-V2 | Expect | `T/acls/` listing | equals the record's host set, exactly |
| L-V3 | Expect | `T/lun/lun_0/ze` readlink; `S/info` | link → `S`. `RdevOf(dev)` against the "Major: M Minor: m" in `info` (`K: target_core_iblock.c:676-679`) |
| L-V4 | Expect | each `T/acls/<i>/auth/userid`, `/password` | equals derived, TrimNL (root read, `K: iscsi_target_configfs.c:371`); **Secret** |
| L-V5 | Expect | `S/wwn/vpd_unit_serial` | `Prefix("T10 VPD Unit Serial Number: ")` + `h` (show format `K: target_core_configfs.c:1673-1674`) |
| L-O1 *(per portal)* | Mkdir | `T/np/<ip>:<port>` | OkExists. Starts or joins the shared listener |
| L-O2 | Write | `T/enable` | `1`. Same value is a no-op (`K: target_core_fabric_configfs.c:845-846`) |

### 4.3 LIO close and teardown

| # | Op | Path | Value | Tol |
|---|---|---|---|---|
| L-C1 | Write | `T/enable` | `0` (kills sessions, `K: iscsi_target_configfs.c:1104-1106`) | — (same value is a no-op) |
| L-D1 *(per i)* | Unlink | `T/acls/<i>/lun_0/ze` | | OkMissing |
| L-D2 | Rmdir | `T/acls/<i>/lun_0`, then `T/acls/<i>` | | OkMissing |
| L-D3 | Unlink | `T/lun/lun_0/ze` | | OkMissing |
| L-D4 | Rmdir | `T/lun/lun_0`, `T/np/*`, `T`, `target/iscsi/<iqn>` | | OkMissing |
| L-D5 | Rmdir | `S` | | OkMissing (EBUSY while linked, `AK: fs/configfs/dir.c:520`; the order prevents it) |
| L-D6 | Rmdir | `H` | | OkMissing \| OkBusy |

For deny on a live TPG, run L-D1 and L-D2 for that `i` only, without L-C1.

### 4.4 nvmet build, verify, open, close

| # | Op | Path | Value | Tol | Rule |
|---|---|---|---|---|---|
| N1 | Mkdir | `X` | | OkExists | born `allow_any_host=0`, random serial (`K: core.c:1871-1878`) |
| N2 | Write | `X/attr_allow_any_host` | `0` | — (no-op if unchanged, `K: configfs.c:1303`) | explicit |
| N3 | Write | `X/attr_serial` | `h[:20]` | — (frozen after discovery) | before any port link |
| N4 | Mkdir | `X/namespaces/1` | | OkExists | |
| N5 | Write | `X/namespaces/1/device_path` | `<dev>` | — (EBUSY when enabled, `AK: configfs.c:540-541`; skipped when the scan shows enabled) | |
| N6 | Write | `X/namespaces/1/device_uuid`, `device_nguid` | UUIDv8(B), B | — (EBUSY when enabled, `AK: :622-623,653-654`) | defaults are a random uuid (`K: core.c:746`) and a zero nguid (NGUID descriptor omitted, `K: admin-cmd.c:1017`) |
| N7 | Write | `X/namespaces/1/enable` | `1` | — (no-op if enabled, `AK: core.c:598-599`) | needs device_path (`AK: core.c:601-603`) |
| N8 *(per hn)* | Mkdir | `nvmet/hosts/<hn>` | | OkExists (shared) | |
| N9 | Write | `nvmet/hosts/<hn>/dhchap_key` (+ `dhchap_ctrl_key` for bidi) | derived; **Secret** | — | before N10, or the host connects unauthenticated (`K: auth.c:170-172`) |
| N10 | Symlink | `X/allowed_hosts/<hn>` | → `nvmet/hosts/<hn>` | OkExists (dup = EEXIST, `K: configfs.c:1230-1233`) | EINVAL if allow_any_host (`:1225-1228`) |
| N-V1 | Expect | `X/attr_allow_any_host` | `0` | | |
| N-V2 | Expect | `X/allowed_hosts/` listing | equals the record's hosts | | |
| N-V3 | Expect | each `hosts/<hn>/dhchap_key` | equals derived (policy dhchap*) | | |
| N-V4 | Expect | `X/namespaces/1/device_path` | `RdevOf(dev)` | | |
| N-O1 *(per p, if absent)* | Mkdir + Write | `nvmet/ports/<p>`, `addr_{trtype,adrfam,traddr,trsvcid}` | from config | OkExists. If the port exists, **never** rewrite `addr_*` (EACCES while enabled, `AK: configfs.c:123-124`); compare them in the scan instead, and on mismatch `Blocked(PortConflict)` | |
| N-O2 | Symlink | `nvmet/ports/<p>/subsystems/<nqn>` | → `X` | OkExists (`K: configfs.c:1144-1147`) | the first link enables the port (`:1150-1151`) |
| N-C1 | Unlink | `nvmet/ports/<p>/subsystems/<nqn>` | | OkMissing | disconnects controllers (`:1184-1185`) |
| N-D1 | Unlink | `X/allowed_hosts/*`; Rmdir `nvmet/hosts/<hn>` | | OkMissing; host rmdir OkBusy | |
| N-D2 | Write | `X/namespaces/1/enable` | `0` | — | `AK: configfs.c:742` |
| N-D3 | Rmdir | `X/namespaces/1`, `X` | | OkMissing | |

Ports are never removed. A port with no links is already disabled and not listening (`K: configfs.c:1188-1189`).

### 4.5 ZFS ops added to the existing `Plan`

| Request | Ops |
|---|---|
| first `ExportVolume` | `OpSet{block_id, block_id_guid, block_export(on=1, mac)}` (one `zfs set`) |
| `Allow/DenyInitiator`, `Unexport`, rotation | `OpSet{block_export(…, mac)}` |
| `AdoptBlockIdentity` | `OpSet{block_id_guid=<guid>, block_export(…, mac′)}` |
| `ReissueBlockIdentity` | `OpSet{block_id=<new>, block_id_guid, block_export(mac′)}`; only when no owned family object for the old id is open |
| `ExpandVolume` on an exported nvmet volume | after `volsize`: `CfsOp{Write, X/namespaces/1/revalidate_size, "1"}` (`K: configfs.c:778-802`; it requires an enabled namespace, `:791-792`) |

---

## 5. Data model changes

- **`Row` gains** `Guid uint64` (native, not `Local()`-filtered), `BlockID [16]byte`, `BlockIDGuid uint64`, and `Export *ExportRecord` (decoded and MAC-checked once in `decodeColumns`). The key is passed into the decoder.
- **`Table` gains** a `byBlockID map[[16]byte][]int`, built once in `NewTable`.
- **`ActualTable` is plain data from one configfs walk.** It holds backstores (name, hba, udev_path, rdev from `info`, serial, company_id, enabled), iSCSI targets (iqn, tpg attrs, enabled, np list, lun link, acls with auth flags and values), nvmet subsystems (nqn, allow_any_host, serial, ns path/uuid/nguid/enabled, allowed_hosts), ports (addr, links) and hosts (key present, key value). Each object is tagged `Family{block_id}` or `Foreign`.
- **Pure functions:**
  - `Classify(Row, Table, ActualTable, KeyState) Status`
  - `PlanClose/Build/Expect/Open/Teardown(Row, ActualObj) CfsPlan`
  - `Unsafe(ActualObj) bool`, which encodes I1 to I3
  - `Preflight(UnitStates, SaveFiles, ActualTable, Mode) error`
- **The only I/O seams** are the `CfsFS` (read, write, mkdir, rmdir, symlink, readlink, list, stat-rdev) and the `Units` query.

## 6. Test plan

### 6.1 Unit tests: a fake configfs tree in a temp dir

- **`cfsfake` implements `CfsFS` over `t.TempDir()`.**
  - Directories, files and symlinks are real, so a failing test leaves an inspectable tree, and golden files can be `tree` dumps.
  - A **rule table** sits in front: `{op, path glob, precondition over the tree, errno, citation}`. It holds one row per kernel behavior cited above, for example:
    - `S/enable` without `control udev_path=` gives EINVAL
    - a second `enable` gives EEXIST
    - `vpd_unit_serial`/`company_id` give EINVAL when the backstore is linked
    - `device_*` give EBUSY when ns enabled
    - `attr_serial` gives EINVAL after a fake "discovered" flag
    - `allowed_hosts` link gives EINVAL when allow_any_host=1
    - rmdir of a link target gives EBUSY
    - port link toggles a "listening" flag
    - `auth/*` stores verbatim
    - `mkdir` existing gives EEXIST
  - Side effects are modelled too: dirs born with kernel defaults (TPG attribs at their kernel defaults, random nvmet serial), `info` text with Major/Minor from a fake rdev map, and the serial show prefix.
- **Golden plans:** for each scenario, the printed `CfsPlan` (secrets redacted) is compared with `testdata/*.golden`. Scenarios: fresh iSCSI export, fresh NVMe export, allow, deny, unexport, rotation, adopt.
- **Invariant-at-every-step property test:** the core test for finding #3. Apply a plan op by op against the fake. After each op, evaluate `Exposed(tree, records)`, which is I1 to I3 written independently of the planner. It must stay false. Run over every scenario and every prefix.
- **Crash or fault fuzzing** (`FuzzExportConverges`, in the style of `FuzzCreateConverges`):
  - random fault injection: errno at op k, or a crash after op k
  - then run the full startup `rebuild()` on the surviving tree
  - assert: `Exposed` false at every step, the final re-scan diff is empty, and a second `rebuild()` emits no ops (idempotency)
- **Stale-restore scenario:** pre-load the fake with a family export holding an extra ACL, `generate_node_acls=1`, and a wrong CHAP password (as a stale `targetctl restore` would leave it). Assert that phase CLOSE is the first op and the final state equals the record.
- **Wrong-device scenario:** a family backstore whose rdev points at another zvol. Assert close, then teardown, then rebuild, and that L-V3 would have failed.
- **Preflight table tests:**
  - every unit state combination
  - saveconfig empty, family-only (quarantine renames the file), and foreign (refuse)
  - a foreign IQN in `exclusive` (refuse) and `shared` (untouched, and its shared port gives `Blocked(PortConflict)`)
- **Identity tests:**
  - derivation vectors (§2.4), with the NAA computed by an independent Go port of `spc_gen_naa_6h_vendor_specific`
  - classification table: unminted, ready, unadopted, duplicate, tampered (flip one byte of the record, of block_id, of block_id_guid), no-key
  - rename keeps every identifier (pure)
  - a static check that no op table contains `generate_node_acls` `1` or `attr_allow_any_host` `1` (I4)

### 6.2 Real kernel: the KVM CI VM (extend `ci/real-zfs-vm.sh`)

- **Setup:** add `targetcli-fb nvmetcli open-iscsi nvme-cli sg3-utils` to cloud-init; `modprobe target_core_mod iscsi_target_mod nvmet nvmet_tcp`. Initiators run in the same VM against 127.0.0.1.
- **R-RULES (rule parity):** each row of the `cfsfake` rule table has a twin that runs the same op sequence on real configfs and asserts the same errno. This keeps the fake honest. It retires the **U:** marks in §3.3, §4.1, and `adv-block-kernel.md` #11 (HBA naming).
- **R-LOGIN (fail-closed, end to end):**
  - iscsiadm with no ACL fails
  - an ACL without creds while auth is required fails
  - the wrong password fails
  - the right CHAP logs in, and `sg_inq -p 0x83` shows NAA `6001405<h[:25]>`
  - deny drops the session
  - `nvme connect` with an unlisted hostnqn fails
  - a listed host without a key fails when the policy is dhchap (the endpoint never builds that state; inject it with a raw write to prove the gate)
  - the right key connects, and `nvme id-ns` shows nguid = B and uuid = UUIDv8(B)
- **R-STEP:** run the startup rebuild with a `CfsFS` wrapper that runs the `iscsiadm`/`nvme discover` probes after every op. Every probe must fail until the open phase.
- **R-RESTORE:**
  - enable `target.service` → the endpoint refuses to start
  - mask it → it starts
  - run `targetcli` interactively and exit (auto-save) → the next drift pass quarantines `saveconfig.json`
  - unmask and start `target.service` → `Conflicts=` stops the endpoint, the restore runs, and the endpoint's next start refuses (unit not masked). Document the outage.
- **R-REBOOT:** export, reboot the VM, and check the rebuild, initiator reconnect, the same WWN, NGUID and IQN, and that no LUN was visible before its ACL (initiator log timestamps against the endpoint's open-phase log).
- **R-DR:**
  - `zfs send -p` into a second pool on the same VM → `Blocked(Unadopted)`
  - export the copy → `FailedPrecondition`
  - `AdoptBlockIdentity` while the original is exported → refused (`Duplicate` within the Table if in the same parent; P6 for the device across parents)
  - destroy the original, adopt → same WWN
- **R-ID-7:** `zfs rollback` and an incremental `zfs recv` keep `guid`. This confirms §2.1, and the classification depends on it.
- **R-TIME:** rebuild time for 200 and 1000 zvols, measured against open-iscsi `replacement_timeout` (120 s default, per finding #9; **U:** for the distro).

---

## 7. Self-review (adversary)

I attacked the design above. These risks remain, ordered by how much they worry me.

- **R1. Root can always beat us. The exclusive-owner guarantee is procedural, not technical.**
  - Masking units and quarantining files does not stop `targetcli restoreconfig`, `nvmetcli restore`, or `targetctl clear` typed by an admin.
  - Between such a command and our next drift pass (up to `drift_interval`), a restored stale ACL is live.
  - `Conflicts=` stops *us* when `target.service` starts, so restore then runs unopposed, and stale ACLs stay live until someone fixes the unit. The endpoint is down by then.
  - Mitigations are detection only: a shorter drift interval, an inotify watch on the save paths, and an alert on `ForeignConfigfsWriter`.
  - A real fix would need an LSM or AppArmor profile denying configfs writes from anything but the endpoint. That is not designed.
- **R2. The host key is a single point of failure and of compromise.**
  - Loss: every record fails its MAC and every CHAP export stays closed. That is a full block outage, which is fail-closed, but still an outage.
  - Compromise: an attacker can forge records (if they can also write user properties) and derive every CHAP secret.
  - HA heads and DR sites must share it, which widens exposure.
  - Key rotation (re-MAC every record under a new key while honouring the old) is not designed.
  - A delegated `userprop` holder without the key can still **delete** a record. Its export is then torn down at the next pass because the row no longer says exported. That is a denial of service, not an exposure.
- **R3. Secrets are visible in configfs.**
  - iSCSI CHAP is readable by any `CAP_SYS_ADMIN` process, which includes privileged CSI node pods with host `/sys`.
  - nvmet `dhchap_key` is world-readable by any process that can see `/sys/kernel/config` (`K: configfs.c:2219-2233`, mode 0644). Deriving the secrets limits the blast radius to the exports whose initiator could already log in, but nothing hides them.
  - iSCSI CHAP is MD5-based and offline-crackable from captured logins. 144-bit secrets make that infeasible, but mutual CHAP should be the default.
- **R4. NVMe deny is coarse.**
  - Because `allowed_hosts` unlink does not drop live controllers, deny bounces the subsystem's port links.
  - Every other host of that volume reconnects. That is acceptable for RWO block, but disruptive for multi-attach.
  - If a port relink fails after the unlink, the volume is unreachable for everyone. The relink is part of the same plan, and the drift pass retries, but it is still an outage window.
- **R5. Migrating from the baseline is blocked, and nothing is designed for it.**
  - A host currently serving democratic-csi exports has foreign IQNs with `generate_node_acls=1`, `allow_any_host=1`, and active `target.service`/`nvmet.service`. In exclusive mode the endpoint refuses to start.
  - Adopting baseline exports (re-identifying them would change the WWN under live initiators) needs a migration tool and a maintenance window.
  - This contradicts the README's "volumes it provisioned are adopted as they are" for the export side.
- **R6. Split brain is still out of scope, and identity makes it worse.**
  - On a shared pool, two heads import and export the same `block_id`, with the same WWN on both paths. dm-multipath merges them and interleaves writes across two ZFS instances.
  - Adopt and Reissue protect against copies, not against the same dataset imported twice. That remains the HA layer's job (`multihost` + STONITH), and the endpoint cannot detect it.
- **R7. The trust anchor for "unadopted" is the guid, and that rests on a code reading.**
  - If some path rewrites a head's guid while keeping its properties, a legitimate volume goes `Unadopted` and stops exporting after a reboot (fail-closed, but an outage).
  - Candidates are a ZFS version change in clone-swap, or `zfs recv -F` of a full stream onto an existing dataset (**U:** whether that keeps the guid).
  - R-ID-7 must cover both receive forms before this ships.
- **R8. Assumptions about TPG defaults and kernel versions.**
  - The design writes every security attribute explicitly, so defaults don't matter. But a *missing* attribute on an older kernel (for example `exclusive=` in `control`, `company_id`, `dhchap_*`) changes behavior silently in places:
    - `exclusive=` is ignored by older parsers (safe)
    - a missing `company_id` file makes L5 fail (closed)
    - a missing `dhchap_key` file means `Blocked(NoAuthSupport)` (closed)
  - The minimum kernel is not stated. It needs a version matrix in CI.
- **R9. Shared nvmet hosts and ports across sibling instances need one key.**
  - Two instances on a host with different keys derive different `dhchap_key`s for the same host NQN and fight. The second one sees a key it did not derive and blocks.
  - This is fail-closed, but it is easy to misconfigure. The key should be host-level config, not per-instance.
- **R10. The record size cap** (about 30 initiators per volume) is fine for CSI and wrong for a general target daemon.
- **R11. The boot open window is shortened, not closed.** Between the first `np` mkdir and the last TPG enable, initiators of not-yet-opened targets get "target not found". Whether open-iscsi treats that as fatal is still **U:** (finding #5 asserted it). R-TIME should measure the window.
- **R12. Several unverified details sit on the path:**
  - `param/AuthMethod` write syntax
  - `acls/<i>/lun_0/write_protect` name
  - the `DHHC-1` body format beyond the prefix
  - the Debian name of the targetctl unit
  - `TARGET_CORE_NAME_MAX_LEN`
  - whether `zfs clone` copies origin user properties

  Each one is either closed by a rule-parity test or fails closed if wrong. None of them can open an export.
