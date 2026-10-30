# Gap: block export (zvol over LIO iSCSI and nvmet NVMe-oF)

Citations: `K:` = torvalds/linux@21a7f44f, `P:` = isac322/pillar-csi@99587095, `R:` = open-iscsi/rtslib-fb@6b68db5e, `T:` = open-iscsi/targetd@e8affc39.

## CLAIMS CHECKED

- **(a) pillar-csi writes LIO and nvmet configfs directly, with read-back: HOLDS, with two breaks.**
  - LIO writes: `P: internal/agent/lio/target.go:664-696` (`ensureLUN`, `ensureMappedLUN`, LUN 0 only). Read-back: `target.go:643`.
  - nvmet writes and read-back: `P: internal/agent/nvmeof/configfs.go:1166-1200` (`verifyPrepared`).
  - BROKEN: "only Go nvmet writer." `P: docs/PRD.md:621` names `github.com/0xfd4d/nvmet-config` as a Go reference. Not checked.
  - BROKEN: identity is not purely derived. `P: internal/agent/nvme_identity.go:84-102` persists an identity record and lets live configfs values override the derived ones.
  - The LIO tests run against a fake kernel (`P: internal/agent/lio/liotest/kernel.go`), not real configfs.
- **(b) targetd persists with `save_to_file`: HOLDS.** `T: targetd/block.py:270,312,334` and six more calls. The save is an atomic tmp+fsync write (`R: rtslib/root.py:443-470`). The boot restore (`docker/launch:10`) is unverified. The save runs after the kernel mutation, so a crash between them leaves the file behind kernel state.
- **(c) configfs is lost on reboot: HOLDS (inference).** configfs lives in kernel memory. Nothing in the kernel source was found that persists it. `P: docs/E2E-TESTCASES.md:2097` treats configfs as surviving an agent restart, not a reboot.
- **Hardcoded LUN 0 per IQN: HOLDS.** `P: lio/target.go:664-666`.
- **LIO layout in pillar's comment: HOLDS.** `P: lio/configfs.go:22-40`. `control`, `info`, `udev_path`, `enable` exist (`K:drivers/target/target_core_configfs.c:2753-2766`).

## LIO: ordered operations

Root `/sys/kernel/config/target`. Every name is derived from the zvol guid (see IDENTITY).

Create, in order:
1. `mkdir core/iblock_0/<name>` (backstore; HBA index assumed 0).
2. write `core/iblock_0/<name>/udev_path` = `/dev/zvol/<pool>/<ds>` (`K:target_core_configfs.c:2432`).
3. write `core/iblock_0/<name>/wwn/vpd_unit_serial` = serial. It must precede any export, because the kernel returns EINVAL when `export_count > 0` (`K:2711-2716`). Without this write, LIO generates a random serial.
4. write `core/iblock_0/<name>/enable` = `1`. Only `1` is accepted; the store uses `strstr` (`K:2470-2488`). There is no disable: teardown uses rmdir.
5. `mkdir iscsi/<iqn>/` (target).
6. `mkdir iscsi/<iqn>/tpgt_1/`.
7. `mkdir iscsi/<iqn>/tpgt_1/np/<ip>:3260` (portal). The kernel adds a default np at `K:iscsi_target_configfs.c:235`; the enclosing function is unconfirmed. Treat np as EEXIST-tolerant.
8. `mkdir iscsi/<iqn>/tpgt_1/lun/lun_0/` (`R: rtslib/target.py:533`).
9. symlink `tpgt_1/lun/lun_0/<name>` → `core/iblock_0/<name>`. The kernel requires the device to be configured (step 4) and the LUN to be unbound (`K:target_core_fabric_configfs.c:620-654`).
10. write `tpgt_1/enable` = `1` (`K:iscsi_target_configfs.c:1088-1109`).
11. `mkdir tpgt_1/acls/<initiator-iqn>/`.
12. `mkdir tpgt_1/acls/<initiator-iqn>/lun_0/`.
13. symlink `acls/<initiator-iqn>/lun_0/<name>` → `tpgt_1/lun/lun_0` (mapped LUN). The kernel requires the tpg LUN to be linked (`K:target_core_fabric_configfs.c:66-110`, `:88`).

CHAP and `attrib/*` are described in `P: lio/configfs.go:33-40` and `lio/chap.go`. Unverified against the kernel.

Teardown, reverse order:
1. unlink `acls/<iqn-i>/lun_0/<name>`; rmdir `acls/<iqn-i>/lun_0`; rmdir `acls/<iqn-i>`.
2. write `tpgt_1/enable` = `0` (`K:1106`).
3. unlink `tpgt_1/lun/lun_0/<name>`; rmdir `tpgt_1/lun/lun_0`.
4. rmdir `tpgt_1/np/*`; rmdir `tpgt_1`; rmdir `iscsi/<iqn>`.
5. rmdir `core/iblock_0/<name>`. This is last because the backstore is referenced until its LUN link is gone (inference).

Idempotency:
- mkdir and symlink: EEXIST means done. Compare the symlink target before reporting success.
- `enable` = `1`: read before write.
- `vpd_unit_serial`: NOT idempotent once exported. The store checks `export_count` before any comparison (`K:1700-1716`), so even an identical value returns EINVAL. Read first and skip the write when it matches.
- `udev_path`: unverified.

## nvmet: ordered operations

Root `/sys/kernel/config/nvmet`.

Create, in order:
1. `mkdir subsystems/<nqn>/`.
2. write `subsystems/<nqn>/attr_allow_any_host` = `0` (`K:configfs.c:1286-1313`; setting `1` fails while hosts exist).
3. write `subsystems/<nqn>/attr_serial` = serial. It fails once discovered (`K:1401-1404`) and has no equality shortcut, so read first.
4. For each initiator: `mkdir hosts/<hostnqn>` and symlink `subsystems/<nqn>/allowed_hosts/<hostnqn>` → `hosts/<hostnqn>` (`P: nvmeof/configfs.go:221-242`; host dir is the subsystem's allowed-host path, not kernel-verified).
5. `mkdir subsystems/<nqn>/namespaces/1/`.
6. write `namespaces/1/device_path` = `/dev/zvol/<pool>/<ds>`. EBUSY if enabled (`K:530-555`).
7. write `namespaces/1/device_uuid` and `namespaces/1/device_nguid`. Both are EBUSY if enabled. The nguid parser takes 16 hex bytes with optional `-` or `:` (`K:609-679`).
8. write `namespaces/1/enable` = `1` (`K:722-745`).
9. `mkdir ports/<id>/`; write `addr_trtype`, `addr_adrfam`, `addr_traddr`, `addr_trsvcid` (`K:132-158`).
10. symlink `ports/<id>/subsystems/<nqn>` → `subsystems/<nqn>`. The first link enables the port; a duplicate returns EEXIST (`K:1125-1160`).

Teardown: unlink `ports/<id>/subsystems/<nqn>`; unlink and rmdir `allowed_hosts/*` and `hosts/*`; write `namespaces/1/enable` = `0` (`K:742`); rmdir `namespaces/1`; rmdir `subsystems/<nqn>`; rmdir `ports/<id>` if empty.

Read-back: `attr_serial` is `%.*s` padded with spaces (`K:1391`), so trim trailing spaces. `device_uuid` and `device_nguid` print as `%pUb` (`K:609-611`, `:637-639`), so compare by parsed UUID.

## IDENTITY

Input: zfs `guid`. The zfs man page says it "does not change over its entire lifetime" (`openzfs: man/man7/zfsprops.7:185-187`). The man page guarantees the guid for snapshots sent to another pool (lines 188-189), not for a live zvol. This design relies only on same-pool import, which keeps the guid.

Derivation (pure, one fixed namespace UUID, never changed):
- LIO IQN: `iqn.2026-10.<domain>:zvol.<guid-hex16>`. NVMe NQN: `nqn.2026-10.<domain>:zvol.<guid-hex16>`.
- LIO `vpd_unit_serial`: UUIDv5(ns, `"vpd:"+guid`). nvmet `attr_serial`: first 16 hex of SHA-256(`"serial:"+guid`) (under `NVMET_SN_MAX_SIZE`, `K:1407`).
- `device_uuid`: UUIDv5(ns, `"uuid:"+guid`). `device_nguid`: UUIDv5(ns, `"nguid:"+guid`).

Storage: nowhere separate. The only persistent input is the zfs guid, which travels with the pool. Configfs is read back for verification only. Do not follow pillar's identity record: it can drift from the derived value.

## DESIGN

Row (new columns, decoded in `decodeColumns`, added to `columns()`):
- `Guid uint64` from `guid` (read-only, numeric, not inherited).
- `Export string` from user property `<prefix>:export`, locally set. Value: JSON `{proto, lun, portal, acls[]}`. Set by ExportVolume, so reboot and HA import restore exports from the pool.

Pure functions:
- `DesiredExports(t Table, cfg ExportCfg) ([]Export, error)`: Rows with Export set → desired entries with derived identity.
- `ActualExports(fs ConfigfsFS) (ActualTable, error)`: the only I/O. Walks configfs into plain data, with trimmed values.
- `DiffExports(want []Export, have ActualTable) Plan`: pure. Emits Ops only for owned names (IQN/NQN matching our pattern). Foreign targets are never touched.
- `PlanLIOExport(e Export) Plan`, `PlanNVMeExport(e Export) Plan`, and their teardown twins: pure, ordered as above.

Ops: five new kinds on the existing enum: `OpCfsMkdir{Target}`, `OpCfsWrite{Target, Value, ReadFirst bool}`, `OpCfsSymlink{Target, Source}`, `OpCfsUnlink{Target}`, `OpCfsRmdir{Target}`. Add a `Value string` field to `Op`. `apply` dispatches to a `ConfigfsFS` interface. Idempotency lives in `OkExists`/`OkMissing` and `ReadFirst`.

Reconcile on start (under the existing locks): zfs scan → `DesiredExports`; configfs scan → `ActualExports`; `DiffExports`; apply; re-scan; assert the diff is empty.

Proto: add `Capability` values `EXPORT_ISCSI` and `EXPORT_NVMF_TCP`; `ExportVolume`, `UnexportVolume`, and `AllowInitiator`/`DenyInitiator` RPCs; a `repeated Export exports` field on `Volume`.

## TEST PLAN

- Planner table tests: Row fixtures → golden `Plan`. Diff tests for missing, partial, extra-owned, and foreign entries. Property test: applying a plan, then re-scanning, gives an empty diff.
- Sim: fake `ConfigfsFS` with the kernel rules above: symlink needs configured dev; serial EINVAL after export; device_path EBUSY when enabled; attr_serial EINVAL after discovery; first port link enables the port.
- Real: a root runner with `target_core_mod` and `nvmet nvmet-tcp` on a zvol. Reboot test: export, reboot, reconcile, and compare IQN/NQN, serial, and nguid. HA test: export on A, import the pool on B, and check identity is unchanged.

## RISKS

- Split brain: two hosts importing one pool both export. Needs fencing (existing gap).
- Guid changes on zfs recv into a new pool or on re-creation, which changes every identity.
- The LIO serial is locked while exported, and the nvmet serial is locked after discovery. A wrong identity is hard to fix in place.
- Teardown ordering is inferred, not tested against a kernel.
- CHAP drift after a crash (existing gap).
- Kernel-version differences in serial length and attribute set.
