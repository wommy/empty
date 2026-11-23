# Adversarial check: block-export kernel/configfs claims

Sources: github:torvalds/linux@21a7f44f (K), github:open-iscsi/rtslib-fb@6b68db5e (R). Paths below are relative to those repos.

| # | Claim | Verdict | Evidence |
|---|---|---|---|
| 1a | "The kernel adds a default np at iscsi_target_configfs.c:235; enclosing function unconfirmed" | BROKEN | Line 235 is inside `lio_target_call_addnptotpg` (line 154), the handler for a user `np/<ip>:port` mkdir, parsing a user-supplied name. No default np is created there. K: drivers/target/iscsi/iscsi_target_configfs.c:154-245 |
| 1b | Serial citation "K:2711-2716" (export_count check) | BROKEN (citation) | Check is at drivers/target/target_core_configfs.c:1711-1716. Behavior is right, line is wrong. |
| 1c | "Exact order" of create steps (serial 3 before enable 4) | BROKEN | Kernel only requires serial and enable to come before the LUN link (step 9). Serial vs enable is unconstrained. rtslib calls enable first, then sets wwn (R: rtslib/tcm.py:528-530 and :405-407, :153-158). |
| 1d | Serial read-back: "trim and compare" | BROKEN (omission) | show prints `T10 VPD Unit Serial Number: <serial>` (K: target_core_configfs.c:1673-1674). A naive compare never matches. rtslib splits on ":" (R: rtslib/tcm.py:145-146). |
| 1e | Create order: backstore, udev_path, serial, enable, iqn/tpgt, lun_N + symlink, acls, portals, tpgt enable | HOLDS (constraints) | Hard constraints: enable before LUN link (ENODEV, K: drivers/target/target_core_fabric_configfs.c:639-642); LUN unbound (EEXIST, :649-652); lun_0 must exist before link (ENOENT, fs/configfs/symlink.c:98,144); serial before export (K: target_core_configfs.c:1711-1716); ACL link needs tpg LUN linked (EINVAL, K: target_core_fabric_configfs.c:88-91) and same TPG (:107-115). No kernel constraint found for np or tpgt enable placement. |
| 2 | vpd_unit_serial "refused once exported, even when unchanged" | HOLDS | export_count check runs before any compare, no equality shortcut, returns -EINVAL (K: drivers/target/target_core_configfs.c:1711-1716). Length >= 254 gives -EOVERFLOW (:1700-1704). |
| 2b | "Only `1` is accepted; uses strstr" (enable) | HOLDS (nuance) | `strstr(page,"1")` (K: target_core_configfs.c:2477-2482). Any input containing "1" is accepted. "0" gives -EINVAL, so there is no disable. Second "1" on a configured device gives -EEXIST (K: drivers/target/target_core_device.c:965-968), not success. |
| 3a | nvmet device_path EBUSY when enabled | HOLDS | K: drivers/nvme/target/configfs.c:540-541 |
| 3b | nvmet device_uuid / device_nguid EBUSY when enabled | HOLDS | K: drivers/nvme/target/configfs.c:622-623, :653-654 |
| 3c | nguid parser: 16 bytes, optional `-` or `:` | HOLDS | K: drivers/nvme/target/configfs.c:658-672 |
| 3d | attr_serial refused after discovery | HOLDS | EINVAL when `subsys_discovered`, K: drivers/nvme/target/configfs.c:1401-1404 |
| 3e | attr_serial read-back: padded with spaces, trim | HOLDS | show uses `%.*s` (K: configfs.c:1391-1392) |
| 3f | device_path must precede enable | HOLDS (added) | enable with no device_path gives -EINVAL, K: drivers/nvme/target/core.c:601-603 |
| 3g | allow_any_host=1 fails while hosts exist | HOLDS | K: drivers/nvme/target/configfs.c:1297-1299 (-EINVAL) |
| 3h | allowed_hosts link needs a host object and allow_any_host=0 | HOLDS (was UNVERIFIED) | K: drivers/nvme/target/configfs.c:1212-1215, :1225-1228 |
| 3i | First port-subsystem link enables the port; duplicate gives EEXIST | HOLDS | K: drivers/nvme/target/configfs.c:1144-1154 |
| 4a | Teardown: unlink LUN link before rmdir backstore | HOLDS | rmdir of a target with `s_links` gives -EBUSY (K: fs/configfs/dir.c:520), and symlink creation increments the target's `s_links` (K: fs/configfs/symlink.c:100). The design's "referenced until its LUN link is gone" is correct, and it is EBUSY. |
| 4b | Teardown: tpgt enable=0 before rmdir | HOLDS (path) | disable path K: drivers/target/iscsi/iscsi_target_configfs.c:1106. Required order for rmdir not checked. |
| 5a | mkdir of existing name gives EEXIST | HOLDS | VFS: K: fs/namei.c:5072-5075 (`filename_create` starts with -EEXIST). configfs_dirent_exists (fs/configfs/dir.c:243) is the subsystem-registration path. |
| 5b | Duplicate symlink gives EEXIST | HOLDS | K: fs/configfs/symlink.c:205-206 |
| 5c | tpgt enable same value is idempotent | HOLDS | K: drivers/target/target_core_fabric_configfs.c:845-846 |
| 5d | LIO enable=1 is "read before write" | HOLDS (precaution) | Second write gives EEXIST (K: target_core_device.c:965-968). Read-first is needed. |
| 5e | vpd_unit_serial "NOT idempotent once exported" | HOLDS | K: target_core_configfs.c:1711-1716 |
| 5f | nvmet ns enable=1 twice is safe | HOLDS (added) | no-op if already enabled, K: drivers/nvme/target/core.c:598-599 |
| 5g | nvmet allow_any_host same value | HOLDS (added) | no-op if unchanged, K: drivers/nvme/target/configfs.c:1303 |
| 5h | udev_path idempotent | UNVERIFIED | Store has no export or configured check, so re-write is accepted (K: target_core_configfs.c:2432-2461). Idempotency of the same value is not separately confirmed. |
| 6 | configfs does not survive reboot; must be restored from pool | HOLDS | Mounted via `get_tree_single` with no backing store (K: fs/configfs/mount.c:100). Kernel memory only. |
| 7a | IQN max length | HOLDS (constant) | ISCSI_IQN_LEN 224 (includes NUL, so 223 chars), K: include/target/iscsi/iscsi_target_core.h:28; TRANSPORT_IQN_LEN 224, include/target/target_core_base.h:33. Enforcement point not located. |
| 7b | NQN max length | HOLDS (constant) | NVMF_NQN_SIZE 223, K: include/linux/nvme.h:18 |
| 7c | vpd serial max | HOLDS | INQUIRY_VPD_SERIAL_LEN 254, `strlen(page) >= 254` gives -EOVERFLOW. Usable is 253 including newline. K: include/target/target_core_base.h:46, target_core_configfs.c:1700-1704 |
| 7d | nvmet serial max 20 | HOLDS | NVMET_SN_MAX_SIZE 20, K: drivers/nvme/target/nvmet.h:33; check at configfs.c:1407 |
| 7e | Derived values fit | HOLDS | IQN/NQN = 34 + len(domain) + 16 hex, so fits if domain <= 189. vpd serial is a 36-char UUID. nvmet serial is 16 hex. All under limits. |
| 8 | udev_path max | HOLDS (added) | SE_UDEV_PATH_LEN 512, so 511 bytes. K: target_core_base.h:894, target_core_configfs.c:2439-2442 |
| 9 | "LIO generates a random serial when unwritten" | UNVERIFIED (kernel) | Not found in kernel. rtslib generates it in userspace (R: rtslib/tcm.py:86). |
| 10 | Symlink target must exist before link | HOLDS | ENOENT, K: fs/configfs/symlink.c:98,144 |
| 11 | HBA dir `core/iblock_0` exists, index 0 | UNVERIFIED | Design assumes it. Not checked. |
| 12 | Port addr_* writes | UNVERIFIED (errno) | Writes while enabled give -EACCES, K: drivers/nvme/target/configfs.c:123-124 (not EINVAL). Design does not state the errno. |

## Summary for the design
- Fix the default-np claim (1a), the serial citation (1b), the "exact order" wording (1c), and the serial read-back prefix (1d).
- Teardown reverse order is correct and is enforced by configfs `s_links` EBUSY (4a).
- Remaining UNVERIFIED: 5h, 9, 11, 12, and tpgt enable placement.
