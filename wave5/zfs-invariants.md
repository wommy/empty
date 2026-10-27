# OpenZFS dataset property invariants for the endpoint parser

Scope: every property the endpoint reads (`internal/endpoint/table.go` `columns()`, `internal/endpoint/props.go` `propNames`, own user properties from `internal/endpoint/endpoint.go:44-54`), plus the properties named in the request that the endpoint does not read (marked "no" in the table).

Source: `scratchpad/repos/openzfs`, HEAD `a128b8a` (META: `Version: 2.4.99`). The clone is shallow, but tags `zfs-2.2.9` and `zfs-2.4.0` resolve and `git diff` works, so the 2.2 vs 2.4 comparison in section 6 is verified, not assumed.

Citations are `path:line` relative to the openzfs root unless they start with `internal/` (endpoint repo, `/home/user/empty`). "Verified" means read in the source at the cited line; "derived" means the claim follows from the cited code but was not separately exercised.

---

## 1. What `zfs get -Hp -o name,property,value,source` prints

| Situation | Value | Source | Cite |
|---|---|---|---|
| Property not valid for the object's type | `-` | `-` | `lib/libzfs/libzfs_dataset.c:2547-2550` returns -1 before anything else is set; `cmd/zfs/zfs_main.c:2102-2105` (`get_callback`) pads `-` and sets `ZPROP_SRC_NONE` |
| Readonly value (type, creation, origin, used, available, mounted, defer_destroy, userrefs, guid, createtxg) | value | always `-` | Kernel never adds a source key for readonly values: `module/zfs/dsl_prop.c:46-48` (`dodefault` returns ENOENT for readonly, non-setonce props), so `module/zfs/dsl_prop.c:1333-1336` leaves no source. libzfs then leaves `source == NULL`, and `get_source` maps NULL to NONE (`lib/libzfs/libzfs_dataset.c:2256-2262`) |
| Value equal to the registered default and not set locally | default value | `default` | Kernel adds empty source when value == default: `module/zfs/dsl_prop.c:1333-1336`; libzfs maps empty source to DEFAULT: `lib/libzfs/libzfs_dataset.c:2264-2266` |
| Source NONE printed | `-` | `-` | `lib/libzfs/libzfs_util.c:1583-1585` |
| Source strings printed | | `default`, `local`, `temporary`, `inherited from <ds>`, `received` | `lib/libzfs/libzfs_util.c:1587-1606` |
| The word `none` as a source | never printed | | `none` is only a `zfs get -s` filter keyword (`cmd/zfs/zfs_main.c:2313-2315`). NONE prints as `-` (above). The request's `none` source value does not occur in output |
| `-p` (literal) sizes and counters | raw decimal | | `lib/libzfs/libzfs_dataset.c:2855-2860` and `:2905-2910` (literal branches in `zfs_prop_get`); `creation` literal at `:2557-2572` |
| quota / refquota / reservation / refreservation with value 0 | `0` under `-p` (`none` without `-p`) | | `lib/libzfs/libzfs_dataset.c:2683-2690` |
| `defer_destroy`, `userrefs` (readonly index/number) | `on`/`off` and decimal | `-` | `zfs_prop.c:308-312` (`boolean_table` prints `off`/`on`); the man page's "yes \| no" text (`zfs_prop.c:544` description) does not match the printed values |
| `mounted` | `yes` / `no` | `-` | special-cased `lib/libzfs/libzfs_dataset.c:2759-2770`; no source assignment in `get_numeric_property` (`:2152-2153`) |

---

## 2. Property table

Type masks are from `module/zcommon/zfs_prop.c` (`zprop_register_*`). Abbreviations: FS = `ZFS_TYPE_FILESYSTEM`, VOL = `ZFS_TYPE_VOLUME`, SNAP = `ZFS_TYPE_SNAPSHOT`, BM = `ZFS_TYPE_BOOKMARK`, DATASET = FS|VOL|SNAP (`include/sys/fs/zfs.h:68-69`).

"Read" = in the endpoint's `columns()` (table.go:132-138) or `propNames` (props.go:47-59), or an own user property (endpoint.go:44-54).

| Property | Read | Applies to | `-Hp` text: not applicable / unset / range | Source values | Invariants (cite) |
|---|---|---|---|---|---|
| `type` | yes (`type`) | DATASET, BM (`zfs_prop.c:588-590`) | never `-` for a dataset row. Always one of `filesystem`, `volume`, `snapshot`, `bookmark` (`lib/libzfs/libzfs_dataset.c:2738-2752`) | `-` always | `@` present iff snapshot, `#` present iff bookmark (section 3). Type gates which other columns may be non-`-` (section 5 rule 1) |
| `name` | n/a (Row.Name) | DATASET, BM (`zfs_prop.c:746-747`, hidden) | always the full name | `-` | section 3 |
| `volsize` | yes | SNAP, VOL (`zfs_prop.c:695-696`) | FS: `-`. VOL: decimal, > 0. SNAP of an FS: `-`. SNAP of a VOL: the volume's size at snapshot time (`lib/libzfs/libzfs_dataset.c:2033-2036`, head-type check). Unset is not possible for a VOL | `local` (fallback at `libzfs_dataset.c:2146-2148` sets source to dataset name). `default` cannot occur (0 is refused at create). Never `inherited` (PROP_DEFAULT, not inheritable: `zfs_prop.c:1001-1007`) | VOL size > 0 (`module/zfs/zvol.c:282-285`; create refuses zero at `lib/libzfs/libzfs_dataset.c:3712`). VOL size % volblocksize == 0 (`zvol.c:287-289`; `libzfs_dataset.c:3719`). Snapshot volsize is `-` iff its head is a filesystem (cross-row check) |
| `volblocksize` | yes | VOL only (`zfs_prop.c:640-642`) | SNAP, FS, BM: `-`. VOL: decimal, power of two in [512, 16 MiB] on LP64 (`zvol.c:466-469`; LP64 max `zfs_max_recordsize` = 16 MiB at `module/zfs/dsl_dataset.c:78-82`; ILP32 max 1 MiB). Default 16384 (`include/sys/fs/zfs.h:1573`) | `default` when value is 16384; otherwise `-` (no source key, default numeric path: `libzfs_dataset.c:2220-2236`; kernel adds no source for non-default, `dsl_prop.c:1333-1336`). `local` never printed | PROP_ONETIME, create-only (`zfs_prop.c:640-641`; `zfs_prop_readonly` at `:936-943` includes ONETIME). Above 128 KiB needs pool feature `large_blocks` (`zvol.c:446-456`; pool state, not checkable from a row). See also `internal/endpoint/props.go:20-23` (its claim is correct for non-default values) |
| `quota` | no | FS (`zfs_prop.c:690-691`) | `-` for non-FS. Unset: `0` (`-p`), source `default`. Set: decimal > 0 | `default`, `local`, `received`. Never `inherited` (PROP_DEFAULT) | 0 iff `default` (section 5 rule 6). Set-time gate: nonzero quota >= reservation and >= used (`module/zfs/dsl_dir.c:1751-1756`) |
| `refquota` | yes (`refquota` in `columns()`) | FS (`zfs_prop.c:698-699`) | `-` for VOL/SNAP/BM (endpoint then reads 0: `table.go` `Refquota`). Unset: `0`, `default`. Set: decimal > 0 | `default`, `local`, `received`. Never `inherited` | refquota >= refreservation (set-time gate `module/zfs/dsl_dataset.c:4676-4682`). Endpoint uses it only for FS capacity (`table.go` `Capacity()`) |
| `refreservation` | yes | FS, VOL (`zfs_prop.c:701-702`) | `-` for SNAP/BM. Unset: `0`, `default`. Set: decimal. `auto` is resolved to a number before the kernel sees it (`lib/libzfs/libzfs_dataset.c:1520-1540`) | `default`, `local`, `received`. Never `inherited` | refreservation <= refquota when refquota > 0 (set-time gate `dsl_dataset.c:4779`). A thick zvol's refreservation equals `zvol_auto_resv` only while it stays in sync (`lib/libzfs/libzfs_dataset.c:1410-1420`, `:1444-1455`). Treat as soft |
| `reservation` | no | FS, VOL (`zfs_prop.c:692-693`) | as `quota` | as `quota` | reservation <= quota when quota > 0 (set-time gate `module/zfs/dsl_dir.c:1856-1857`) |
| `creation` | yes | DATASET, BM (`zfs_prop.c:789-791`) | never `-` for datasets. Decimal epoch seconds under `-p` (`libzfs_dataset.c:2557-2572`) | `-` always | endpoint parses with `strconv.ParseInt` (table.go) |
| `origin` | yes | FS, VOL (`zfs_prop.c:577-578`) | SNAP, BM: `-`. Not a clone: `-` (`libzfs_dataset.c:2652-2655` maps the missing default to -1). Clone: full snapshot name | `-` always (not set by kernel as a sourced prop: `dsl_dir.c:1125-1128` adds it only for clones, with no source) | non-`-` only on FS/VOL that are clones (`module/zfs/dsl_dir.c:1125-1128`). Value always names a snapshot: `dsl_dir_get_origin` (`dsl_dir.c:1058-1067`) yields a dataset name from the origin object, and clone creation requires an origin with `ds_is_snapshot` (`module/zfs/dsl_dataset.c:3522`). A clone's name never contains `@` (`dsl_dataset.c:3495-3496`) |
| `used` | no | DATASET except BM (`zfs_prop.c:626-627`) | BM: `-`. Decimal bytes | `-` (readonly) | not read by endpoint |
| `available` | no | FS, VOL (`zfs_prop.c:628-630`) | SNAP, BM: `-`. Decimal bytes | `-` (readonly) | not read by endpoint |
| `mountpoint` | yes | FS (`zfs_prop.c:582-583`) | VOL, SNAP, BM: `-`. FS: absolute path, `legacy`, or `none`. Registered default `/` (`zfs_prop.c:582`); the printed path is built by libzfs for inherited values (`libzfs_dataset.c:2577-2645`) | `default`, `local`, `inherited from <ds>`, `received`. Never `-` for FS | FS only. Value is `none` or `legacy` or starts with `/`. Endpoint denies setting it (`props.go` denied map) |
| `mounted` | yes | FS (`zfs_prop.c:540-541`) | non-FS: `-`. FS: `yes` or `no` (`libzfs_dataset.c:2759-2770`) | `-` always | `yes` depends on the live mount table, not on the row. Endpoint reads `Mounted = value == "yes"` (table.go) |
| `sharenfs` | yes | FS (`zfs_prop.c:585-586`) | non-FS: `-`. Unset: `off`, source `default`. Else `on` or an option string | `default`, `local`, `inherited from <ds>`, `received` | FS only. Option string is not validated by libzfs (out of scope). Endpoint treats it as export state (`ShareNFSLocal` via `Local()`) |
| `canmount` | no (denied for writes: `props.go` denied map, `canmount: mounts`) | FS (`zfs_prop.c:535-536`) | non-FS: `-`. Values `on`, `off`, `noauto` (`canmount_table`, `zfs_prop.c:327-331`). Default `on` (`include/sys/fs/zfs.h:595`) | `default`, `local`, `received`. Never `inherited` (PROP_DEFAULT) | FS only |
| `defer_destroy` | yes | SNAP (`zfs_prop.c:543-544`) | non-SNAP: `-`. Snapshots: `on` or `off` (`zfs_prop.c:308-312`) | `-` always | `on` is set only while userrefs > 0, or children > 1, or a long hold is present (`module/zfs/dsl_destroy.c:322-326`). So `on` with userrefs == 0 is valid only while clones exist; verifying that needs the clone index, not this row. Snapshot names only (section 5 rule 3) |
| `userrefs` | no | SNAP (`zfs_prop.c:660-661`) | non-SNAP: `-`. Snapshots: decimal >= 0 | `-` always | not read by endpoint |
| `guid` | no | DATASET, BM (`zfs_prop.c:676-677`) | decimal uint64. Never `-` for datasets | `-` always (`libzfs_dataset.c:2198-2203` assigns no source) | not read by endpoint |
| `compression` | yes (LocalProps) | FS, VOL (`zfs_prop.c:422-424`) | SNAP, BM: `-`. Values from `compress_table` (`zfs_prop.c:109-111`) incl. `gzip-N`, `zstd-N`, `zstd-fast[-N]`. Default `on` (`include/sys/zio.h:139`) | `default`, `local`, `inherited from <ds>`, `received` | `-p` prints the first table name for a value (e.g. `gzip` for level 6). Endpoint canon: `props.go` compression map |
| `recordsize` | yes (LocalProps) | FS (`zfs_prop.c:734-736`) | VOL, SNAP, BM: `-`. Decimal power of two 512..16 MiB (`libzfs_dataset.c:1041-1058`; `zfs_ioctl.c:5447-5459`). Default 131072 | `default`, `local`, `inherited from <ds>`, `received` | > 128 KiB needs pool feature (not checkable from row) |
| `atime` | yes (LocalProps) | FS (`zfs_prop.c:488-489`) | non-FS: `-`. `on` / `off`. Default `on` | `default`, `local`, `inherited from <ds>`, `received`, **`temporary`** (live mount option differs, `libzfs_dataset.c:2119-2131`) | `temporary` is possible for atime, relatime, devices, exec, readonly, setuid, xattr, nbmand only (mount-option-backed) |
| `relatime` | yes (LocalProps) | FS (`zfs_prop.c:490-496`) | non-FS: `-`. `on` / `off`. Default `on` on Linux (`zfs_prop.c:490-495`) | as `atime`, including `temporary` | as `atime` |
| `sync` | yes (LocalProps) | FS, VOL (`zfs_prop.c:407-408`) | SNAP, BM: `-`. `standard`, `always`, `disabled`. Default `standard` (`include/sys/fs/zfs.h:625`) | `default`, `local`, `inherited`, `received` | none beyond enum |
| `logbias` | yes (LocalProps) | FS, VOL (`zfs_prop.c:467-468`) | SNAP, BM: `-`. `latency`, `throughput`. Default `latency` (`zfs.h:600`) | `default`, `local`, `inherited`, `received` | none beyond enum |
| `dedup` | yes (LocalProps) | FS, VOL (`zfs_prop.c:417-418`) | SNAP, BM: `-`. `off`, `on`, `verify`, `sha256[...]` etc. Default `off` | `default`, `local`, `inherited`, `received` | `-p` prints `verify` for verify. Endpoint canon matches (`props.go`) |
| `xattr` | yes (LocalProps) | FS, SNAP (`zfs_prop.c:470-471`) | VOL, BM: `-`. 2.4: `off`, `sa`, `dir` (`on` prints as `sa`). Default `sa`. **2.2 differs, section 6** | `default`, `local`, `inherited`, `received`, `temporary` (`xattr` is in the mount-option case list, `libzfs_dataset.c:2115`) | section 6 |
| `acltype` | yes (LocalProps) | FS, SNAP (`zfs_prop.c:439-446`) | VOL, BM: `-`. `off`, `posix`, `nfsv4`. Legacy input aliases `disabled`, `noacl` print as `off`, `posixacl` as `posix` (`zfs_prop.c:250-258`). Default `off` on Linux (`zfs_prop.c:439-446`, `#ifdef __linux__`) | `default`, `local`, `inherited`, `received` | none beyond enum |
| `snapdir` | yes (LocalProps) | FS (`zfs_prop.c:429-430`) | non-FS: `-`. `hidden`, `visible`, and `disabled` in 2.4 only (`zfs_prop.c:229-233`). Default `hidden` | `default`, `local`, `inherited`, `received` | `disabled` is not a 2.2 value (section 6) |
| `copies` | yes (LocalProps) | FS, VOL (`zfs_prop.c:452-453`) | SNAP, BM: `-`. `1`, `2`, `3`. Default `1` | `default`, `local`, `inherited`, `received` | none beyond enum |
| user props `<p>:*` (own keys: `managed_resource`, `provision_success`, `csi_volume_name`, `csi_volume_content_source_type`, `csi_volume_content_source_id`, `csi_snapshot_name`, `csi_snapshot_source_volume_id`, `nfs_clients`; `endpoint.go:44-54`) | yes | any type, incl. snapshots (`zfs_check_settable` returns before the snapshot check, `module/zfs/zfs_ioctl.c:5306-5310`, `:5375`) | unset: value `-`, source `-` (`cmd/zfs/zfs_main.c:2145`). Set: raw string, may be empty | `local`, `inherited from <ds>`, `received` (`zfs_main.c:2151-2160`) | section 4. Ownership is read only when source is local or received (`table.go:157-165`) |

Notes on the request's list that the endpoint does not read: `quota`, `reservation`, `used`, `available`, `canmount`, `userrefs`, `guid`. They are in the table for completeness. The endpoint's `columns()` (table.go:132-138) does not request them.

---

## 3. Name rules (datasets, snapshots, bookmarks)

All citations are `module/zcommon/zfs_namecheck.c` unless noted.

- **Length.** Total name strictly under 256 bytes, so at most 255 (`ZFS_MAX_DATASET_NAME_LEN` 256, `include/sys/fs/zfs.h:78`), checked at `:182`. Each component is also checked against 256 at `:93` (`zfs_component_namecheck`) and `:386`.
- **Character set.** `valid_char` at `:49-56` allows `A-Z a-z 0-9 - _ . :` and space. Space is allowed by the kernel. The `entity_namecheck` loop at `:219` additionally allows `%` (internal temp-clone names). Userland refuses `%` for modifying operations: `lib/libzfs/libzfs_dataset.c:132-134`. `#` is refused for dataset types by `dataset_namecheck` (`:289-306`).
- **Components.** No empty component (`:260`), no leading `/` (`:190-192`, `NAME_ERR_LEADING_SLASH`), no trailing `/` (`:213`), no component `.` (`:230-236`) or `..` (`:240-247`).
- **Delimiters.** At most one of `@` or `#` in total (`:252`, `NAME_ERR_MULTIPLE_DELIMITERS`). No `/` after the delimiter (`:274-277`). Snapshot names need `@` (`snapshot_namecheck`, `:327-340`). Bookmark names need `#`.
- **Type-specific delimiter rules.** `lib/libzfs/libzfs_dataset.c:98-130` (`zfs_validate_name`): non-snapshot types refuse `@`; `ZFS_TYPE_SNAPSHOT` requires `@`; non-bookmark types refuse `#`; `ZFS_TYPE_BOOKMARK` requires `#`.
- **Nesting.** Depth (number of `/` before the first `@`/`#`) must be below 50 (`:46`, `:152-155`). Enforced at `module/zfs/dmu_objset.c:1194` and `lib/libzfs/libzfs_dataset.c:3586`, `:3630`.
- **Pool (first component).** `pool_namecheck` (`:396-441`): ASCII letter first (`:426-431`), not `mirror`, `raidz`, `draid` (`:434-438`), and under 240 bytes (`:408-409`).
- **Snapshot short name.** Same charset and component rules; must be non-empty (`start == end`, `:260`).

---

## 4. User properties

- **Key.** `zfs_prop_user` (`module/zcommon/zfs_prop.c:838-858`): every byte must pass `zprop_valid_char` (`module/zcommon/zprop_common.c:410-415`: `a-z 0-9 - _ . :`, lowercase only), and at least one `:` must be present. The kernel's ioctl check adds: length strictly under `ZAP_MAXNAMELEN` = 256 (`module/zfs/zfs_ioctl.c:3324`, `include/sys/fs/zfs.h:74`), so at most 255 bytes. Userland repeats the length check (`lib/libzfs/libzfs_dataset.c:866-879`).
  - The code does not reject a leading `-`, an empty module part, or `:` alone. `man/man7/zfsprops.7` (User Properties section, ~line 2415) says "cannot begin with a dash" and "at most 256 characters". Neither is enforced beyond the code above. Parse by the code (255 bytes, leading `-` allowed).
- **Value.** Must be a string (`zfs_ioctl.c:3319-3321`). Length strictly under `ZAP_MAXVALUELEN` = 8192 (`include/sys/fs/zfs.h:76`), so at most **8191 bytes** (`zfs_ioctl.c:3326-3327`, `E2BIG`). The man page's "limited to 8192 bytes" is off by one.
- **Empty value.** Allowed. The CLI parser accepts `k=` (`cmd/zfs/zfs_main.c:623-640`), and nothing in the kernel rejects a zero-length string beyond the length check above. The value is stored as an empty string.
- **Content.** Arbitrary bytes; `man/man7/zfsprops.7` says values "are never validated". Tabs and newlines are possible, and `zfs get -Hp` prints them raw. `internal/zfs/zfs.go:452-460` already rejects duplicate (dataset, property) lines, which is the newline-forgery guard. `internal/zfs/zfs.go:162-185` (`parseGet`) anchors on the first two tabs and the last one for this reason.
- **Source.** Always inherited (man page). Printed as `local` when set on the dataset, `inherited from <ds>` from an ancestor, `received` for received values, `-` with value `-` when unset (`cmd/zfs/zfs_main.c:2151-2160`, `:2145`).
- **Literal `-` value.** `zfs set k=-` is accepted, and `zfs get` prints value `-` with source `local`. The endpoint's `userValue` (`internal/endpoint/endpoint.go:238-244`) maps `-` to "", so a set-to-`-` value is indistinguishable from unset after decoding. This is an ambiguity in the endpoint, not an OpenZFS rule.
- **Snapshots.** Native properties cannot be set on a snapshot (`zfs_ioctl.c:5375`, `EINVAL`). User properties can (`:5306-5310` returns before that check).
- **Clear.** `zfs inherit` removes a user property; if no ancestor defines it, it is gone (man page).

---

## 5. Cross-property invariants

Numbered for reference in section 7.

1. **Type vs delimiter.** `type = snapshot` iff the name has exactly one `@` and no `#`. `type = bookmark` iff exactly one `#` and no `@`. `filesystem` and `volume` have neither. (`zfs_validate_name`, `libzfs_dataset.c:98-130`; `entity_namecheck`, `zfs_namecheck.c:252`.)
2. **Type gates the dash.** `-` (not applicable) is forced by the type masks in section 2. In particular:
   - Volumes: `-` for mountpoint, mounted, sharenfs, canmount, quota, refquota, recordsize, atime, relatime, snapdir, xattr, acltype (the last two are FS|SNAP only). `zfs_prop.c` masks.
   - Snapshots: `-` for origin, available, quota, refquota, reservation, refreservation, volblocksize, mountpoint, mounted, sharenfs, canmount, recordsize, atime, relatime, snapdir, compression, sync, logbias, dedup, copies. `xattr` and `acltype` are not `-` on snapshots (FS|SNAP). `volsize` is `-` iff the head is an FS (`libzfs_dataset.c:2033-2036`), a cross-row check.
   - Non-snapshots: `-` for `defer_destroy` and `userrefs`.
   - Bookmarks: only `type`, `name`, `creation`, `guid`, `createtxg`, `referenced`, `logicalreferenced`, `compressratio` (DATASET|BOOKMARK masks) are non-`-`; every other property is `-`.
3. **Snapshot-only.** `defer_destroy` and `userrefs` are non-`-` iff the row is a snapshot (`zfs_prop.c:543-544`, `:660-661`).
4. **Readonly means source `-`.** `type`, `creation`, `origin`, `used`, `available`, `mounted`, `defer_destroy`, `userrefs`, `guid`, `createtxg` always print source `-` (section 1, `dsl_prop.c:46-48`, `libzfs_dataset.c:2152-2153`, `:2738`, `:2557`).
5. **Inheritance.** `inherited from X` is possible only for PROP_INHERIT props (`zfs_prop.c:1001-1007`, `zprop_inheritable`). `quota`, `refquota`, `reservation`, `refreservation`, `volsize`, `canmount` are PROP_DEFAULT: never `inherited from`. Enforced at set: `libzfs_dataset.c:1871-1872` returns EZFS_PROPNONINHERIT. For inheritable props, X must be an ancestor of the row's dataset (by construction of the parent-chain walk; not re-read).
6. **Zero is none.** For quota, refquota, reservation, refreservation: value `0` iff source `default`. The kernel gives any value equal to the default an empty source (`dsl_prop.c:1333-1336`; used for quota at `dsl_dir.c:1097-1098` and refquota at `dsl_dataset.c:2949-2950`), and libzfs prints `default` for that (`libzfs_dataset.c:2264-2266`). Under `-p` the value is `0`, never `none` (`libzfs_dataset.c:2683-2687`).
7. **Volume geometry.** `volsize > 0` (`zvol.c:282-285`). `volsize % volblocksize == 0` (`zvol.c:287-289`). `volblocksize` is a power of two in [512, `zfs_max_recordsize`] and <= 16 MiB (`zvol.c:466-469`; `dsl_dataset.c:78-82`; ILP32 default 1 MiB). `volblocksize` is create-only (`zfs_prop.c:640-641`).
8. **Origin.** Non-`-` only on FS/VOL clones (`dsl_dir.c:1125-1128`). The value is a snapshot's full name (`dsl_dir.c:1058-1067`; clone creation requires `ds_is_snapshot`, `dsl_dataset.c:3522`). Clone names never contain `@` (`dsl_dataset.c:3495-3496`).
9. **Space bounds (set-time gates).** Reservation <= quota and refreservation <= refquota whenever the bound is nonzero, and refquota >= refreservation (`dsl_dir.c:1751-1756`, `:1856-1857`; `dsl_dataset.c:4676-4682`, `:4779`). Each set is checked, so a violating row cannot come from a normal set.
10. **Defer-destroy.** `defer_destroy = on` requires userrefs > 0, or children > 1, or a long hold, at the time it was marked (`dsl_destroy.c:322-326`). A row with `on` and `userrefs = 0` is only valid while clones exist, which this row cannot show.

---

## 6. Differences between 2.2 and 2.4

Verified with `git diff zfs-2.2.9 zfs-2.4.0` on the files listed. Everything not listed is unchanged in the hunks that matter here.

**Changed behaviour (matters to the endpoint):**

- **xattr.** 2.2.9 `xattr_table` (`module/zcommon/zfs_prop.c:363-369`) maps `on` -> `ZFS_XATTR_DIR`, and `dir` -> `ZFS_XATTR_DIR`, listing `on` first. The default is `ZFS_XATTR_DIR` (`:471`). Because `zprop_index_to_string` returns the first matching name (`module/zcommon/zprop_common.c:341-345`), 2.2 prints DIR as `on`. The 2.4 table (`zfs_prop.c:364-370`) maps `on` -> `ZFS_XATTR_SA` and lists `sa` first, and the default is SA (`zfs_prop.c:470`). 2.4 prints DIR as `dir`.
  - **Impact:** `internal/endpoint/props.go` canon `"on": "sa"`, `"dir": "dir"` assumes 2.4. On 2.2, a `dir` xattr row prints as `on`, so `dir` never matches the canonical value and the compatibility check fails. The endpoint needs a version gate or a 2.2 canon table.
- **snapdir `disabled`.** New in 2.4 (`zfs_prop.c:229-233`; 2.2 table has only `hidden`, `visible`). Impact: the endpoint's canon map accepts `disabled`, which 2.2 rejects.
- **volblocksize and recordsize documentation strings.** 2.2.9: `512 to 128k` and `512 to 1M` (`zfs_prop.c` registration). 2.4: `512 to 16M` for both. Enforcement is unchanged: `zvol_check_volblocksize` (`zvol.c:439-476`) matches in both tags, and `zfs_max_recordsize` is 16 MiB on LP64 in both (`dsl_dataset.c:80` in 2.4, `:88` in 2.2.9). The doc change does not change accepted values.
- **special_small_blocks.** 2.4 adds VOL and widens the range to 0..16 MiB (`zfs_prop.c` diff). Not read by the endpoint.
- **New 2.4 properties (not read):** `direct` (`zfs_prop.c` diff, FS), `volthreading` (VOL), `longname` (FS), `defaultuserquota` and friends (FS|SNAP), `snapdir=disabled`.
- **ZAP_MAXNAMELEN_NEW = 1024** added in 2.4 (`include/sys/fs/zfs.h` diff) for the `longname` feature (file names). User properties and dataset names still use `ZAP_MAXNAMELEN` = 256 (`zfs_ioctl.c:3324` in both tags, `:2779` in 2.2.9).

**Unchanged in 2.2.9 vs 2.4.0 (verified by diff or identical text):**
- `zfs_prop_user`, `zprop_valid_char`, `valid_char`, `entity_namecheck`, `dataset_namecheck`, `pool_namecheck` (only SPDX headers changed).
- `zfs_check_userprops` (`zfs_ioctl.c`), `zvol_check_volsize`, `zvol_check_volblocksize` logic, the `issnap` check in `zfs_check_settable`.
- Quota and refquota set-time checks (`dsl_dir.c` quota and reservation checks, `dsl_dataset.c` refquota and refreservation checks): no hunks in those ranges.
- `zfs_validate_name` and `zfs_get` source printing (`libzfs_util.c`, `zfs_main.c` get path).
- `acltype_table`, `boolean_table`, `canmount_table`, `compress_table`, `dedup_table`.
- Man page `zfsconcepts.7`: only the snapdir wording changed.

---

## 7. Ten invariants most useful for rejecting impossible rows at parse time

1. **Type and delimiter agree.** `snapshot` <=> exactly one `@`, no `#`. `bookmark` <=> exactly one `#`, no `@`. `filesystem`/`volume` <=> neither. (`libzfs_dataset.c:98-130`, `zfs_namecheck.c:252`.)
2. **Name grammar.** Under 256 bytes; chars in `[A-Za-z0-9_.:\- ]` (space included; `%` only internally); no empty component; no leading or trailing `/`; no `.` or `..` component; no `/` after `@`/`#`; pool begins with an ASCII letter and is not `mirror`/`raidz`/`draid`. (`zfs_namecheck.c:49-56`, `:173-280`, `:396-441`.)
3. **Type gates `-`.** Use the section 2 masks: FS-only, VOL-only, SNAP-only, and DATASET-only properties must be `-` on the other types. Specifically `defer_destroy` and `userrefs` are `-` on every non-snapshot, and `volblocksize` is `-` on everything but VOL. (`zfs_prop.c` masks.)
4. **Readonly means source `-`.** `type`, `creation`, `origin`, `used`, `available`, `mounted`, `defer_destroy`, `userrefs`, `guid` never carry `local`, `default`, `inherited from`, `received`, or `temporary`. (`dsl_prop.c:46-48`, `libzfs_dataset.c:2152-2153`, `:2557`, `:2738`.)
5. **No `inherited from` for PROP_DEFAULT props.** `quota`, `refquota`, `reservation`, `refreservation`, `volsize`, `canmount` never show `inherited from`. (`zfs_prop.c:1001-1007`, `libzfs_dataset.c:1871-1872`.)
6. **Zero is `default`.** For quota, refquota, reservation, refreservation: value `0` iff source `default`. A `0` with source `local` or `received` is impossible. (`dsl_prop.c:1333-1336`, `libzfs_dataset.c:2264-2266`, `:2683-2687`.)
7. **Volume geometry.** `volsize > 0`, `volsize % volblocksize == 0`, `volblocksize` is a power of two in [512, 16 MiB] on LP64 (1 MiB on ILP32). (`zvol.c:282-289`, `:466-469`, `dsl_dataset.c:80`.)
8. **Origin is a snapshot of a clone.** Non-`-` only on FS/VOL clones, value is a full snapshot name (one `@`). (`dsl_dir.c:1058-1067`, `:1125-1128`, `dsl_dataset.c:3522`.)
9. **Space bounds.** Nonzero `reservation` <= nonzero `quota`, and nonzero `refreservation` <= nonzero `refquota`, and `refquota >= refreservation`. Every set enforces these. (`dsl_dir.c:1751-1756`, `:1856-1857`; `dsl_dataset.c:4676-4682`, `:4779`.)
10. **User property shape.** Key is `[a-z0-9_.:-]+`, contains `:`, under 256 bytes (leading `-` and empty module part are not rejected by the kernel, so do not reject them). Value is a string of at most 8191 bytes, may be empty, may contain tab/newline (parse by fixed fields and reject duplicate (dataset, property) lines). A value of literal `-` with source `local` is a real value, not "unset". (`zfs_prop.c:838-858`, `zfs_ioctl.c:3319-3327`, `zfs_main.c:623-640`.)

---

## 8. Endpoint-side issues found while reading

These are not OpenZFS rules. They are places where the endpoint's assumptions differ from the source above.

- **xattr canon is 2.4-only** (`internal/endpoint/props.go`, `"on": "sa"`, `"dir": "dir"`). On 2.2.9 a `dir` xattr prints as `on` (section 6). A version gate is needed.
- **snapdir `disabled` is 2.4-only** (`internal/endpoint/props.go` canon). On 2.2 `zfs set snapdir=disabled` is refused.
- **Dataset regex vs kernel** (`internal/zfs/zfs.go:67-68`). `datasetRE` and `snapRE` require the first character to be alphanumeric, so a digit-leading pool passes. The kernel requires a letter for the pool (`zfs_namecheck.c:426-431`). `datasetRE` also refuses spaces, which the kernel allows (`zfs_namecheck.c:49-56`). Both are stricter or looser than the kernel in different directions.
- **volblocksize source.** `props.go:20-23` says zfs never reports volblocksize as locally set, source `-`. That holds for non-default values. The default 16384 prints source `default` (section 2). A strict parser should accept `default` for 16384 and `-` otherwise.
- **`temporary` source.** atime and relatime (both read) can print `temporary` when the live mount option differs (`libzfs_dataset.c:2119-2131`). The endpoint's `own()` only trusts `Local()`, so this is safe, but a strict source allowlist must include `temporary` for those two.
- **User-property `-`.** `userValue` (`internal/endpoint/endpoint.go:238-244`) maps value `-` to "", so a user property literally set to `-` is treated as unset (section 4, Literal `-` value).
- **Man page vs code.** `zfsprops.7` says user-property names are at most 256 characters with no leading dash, and values at most 8192 bytes. Code: 255 bytes, leading `-` allowed, 8191 bytes. Follow the code.

---

## 9. Not verified

- The exact text of every mountpoint path is built by libzfs (`libzfs_dataset.c:2577-2645`); I did not run `zfs get` to confirm the strings for inherited mountpoints.
- Whether a `dsl_prop` inheritance chain always names an ancestor was taken by construction, not re-read in `dsl_prop_get_dd`.
- Snapshot source values for inheritable native props (compression, atime, etc.) were not traced, so no claim is made about what `zfs get` prints as source on snapshots for those.
- `mounted` depends on the live mount table (`libzfs_dataset.c:2102-2105`); the row alone cannot prove or disprove it.
- The 2.2.9 `zfs_max_recordsize` LP64 default was read from the 2.4 source plus the 2.2.9 `dsl_dataset.c:88` line; the 2.2.9 `#ifdef _ILP32` block was seen but not separately run.
- No `zfs` binary was run. Everything above is from reading the source at the cited lines.
