# OpenZFS-first drift review, with democratic-csi as an optional adoption target

Framing (owner decision): OpenZFS is the source of truth. democratic-csi (DC) is not the
behavioral baseline. It is only an optional adoption/migration target. This file therefore
lists (A) OpenZFS 2.2 to 2.4.4 drift that affects our code, (B) where DC diverges from OpenZFS
semantics and should NOT be copied, and (C) the minimal set an opt-in adoption mode must read.

Sources and their limits:
- OpenZFS HEAD: local clone /tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/repos/openzfs, a single 2.4.99-dev commit. Paths below are relative to it.
- OpenZFS tags via GitHits: zfs-2.1.14 (d99134be), zfs-2.2.x (079ba86d), zfs-2.4.4 (71a9f957). Only the cited greps were run against these tags.
- DC: local clone at repos/democratic-csi, HEAD 69c7ebc, no tags. Per-version history is not available, so DC claims are HEAD-only.
- Our code: /home/user/empty at HEAD.

Status legend: VERIFIED = read in source or matched in a tag grep. UNVERIFIED = not checked for the stated version.

## A. OpenZFS 2.2 to 2.4.4 drift (priority order)

| # | seam | evidence | our behavior | risk | severity | suggested fix |
|---|---|---|---|---|---|---|
| A1 | Default volblocksize | VERIFIED: ZVOL_DEFAULT_BLOCKSIZE 8192 at zfs-2.1.14 (d99134be); 16384 at zfs-2.2.9 (079ba86d) and zfs-2.4.4 (71a9f957); 16384 at HEAD (include/sys/fs/zfs.h:1573). The change lies between 2.1 and 2.2, so 2.2 to 2.4.4 has NO default drift | `internal/endpoint/plan.go` Block create passes `volblocksize` only if the caller asked; `endpoint.go` Config.VolBlockSize (16 KiB) is used only for rounding. We depend on the ZFS default | On a 2.1 host we get 8K zvols while our rounding assumes 16K. Requests with volblocksize=16K would then compare wrong. No drift on 2.2 to 2.4.4 | Medium (2.1 only) | Pass `volblocksize` explicitly (default 16K) so the result does not depend on the host's ZFS version. Keep `BlockSz` as source of truth |
| A2 | `zfs create -p` on an existing dataset exits 0 | VERIFIED at zfs-2.2.9 (cmd/zfs/zfs_main.c:876-880) and zfs-2.4.4 (cmd/zfs/zfs_main.c:900-905): `if (zfs_dataset_exists(...)) { ret = 0; goto error; }`. Same at HEAD (zfs_main.c:~897-903) | `internal/zfs/zfs.go:195-200` uses -p only for filesystems path; README rule "never create -p for volumes" | Design rule is correct across 2.2 to 2.4.4. Protects against silent adoption of an existing dataset | Info | Keep the rule. Add a test that a second create of a plain volume name fails rather than succeeding |
| A3 | Classifier error strings | VERIFIED at zfs-2.4.4 (71a9f957): "could not find any snapshots" (cmd/zfs/zfs_main.c:1764), "snapshot is cloned" (lib/libzfs/libzfs_dataset.c:3982), "pool I/O is currently suspended" (lib/libzfs/libzfs_util.c:488, 712), "size is less than current used or reserved space" split across C literals (libzfs_util.c:546). At HEAD all 23 patterns in `internal/zfs/errors.go` match (case-insensitively). UNVERIFIED at zfs-2.2.9: the grep timed out | `internal/zfs/errors.go` classify (case-insensitive substring match) | Patterns are current for 2.4.4. 2.2 wording UNVERIFIED. Any future wording change falls to KindUnknown, which we treat as not-absent (safe) | Medium (fails safe, may fail loud) | Add golden tests: record CLI stderr from a 2.2 and a 2.4.4 host for each KindX and assert classification. Until then the table is a reviewed snapshot |
| A4 | volblocksize maximum | HEAD: zfs_prop.c volblocksize "512 to 16M" with large_blocks gating (module/zfs/zvol.c:439-470: `zvol_check_volblocksize` > SPA_OLD_MAXBLOCKSIZE needs `zfs_max_recordsize`/large_blocks). 2.4.4 value UNVERIFIED (not read) | `internal/endpoint/props.go` `blockSize(512, 128<<10)` | Refuses valid >128K zvols. Conservative, no corruption | Low | Keep the 128K cap unless a use case needs more. If widened, gate on the large_blocks feature |
| A5 | refreservation=auto tracking | HEAD: libzfs/libzfs_dataset.c:1410-1420 zvol_auto_resv depends on the pool handle; zfs_add_synthetic_resv (1420-1460) keeps reservation in step with volsize only while the current value equals the auto value. 2.4.4 and 2.2 behavior UNVERIFIED | `plan.go` PlanExpandVolume sets volsize only and relies on tracking | If pool geometry changes, the auto value changes and tracking stops: an expanded thick zvol is under-reserved. Mechanism read at HEAD only | Medium (UNVERIFIED on tags) | On expand of a thick zvol (Refres > 0) also set refreservation=auto, which libzfs recomputes. Test after a geometry change |
| A6 | zfs get output and -j | HEAD: `zfs get -j` exists (cmd/zfs/zfs_main.c:2217-2245, 3886-3915, 7411-7432). We use `-Hp -o name,property,value,source` (internal/zfs/zfs.go:133, 340, 402) and do not use -j | `parseColumns` splits on tab (SplitN 3 plus LastIndexByte) | Column output is stable at HEAD. 2.2 and 2.4.4 format UNVERIFIED. A user property value containing a tab would mis-split on the first tab only if it appears in the value, which parseColumns handles because source is the last field | Low | Add tests: tab inside a user property value, source "inherited from ...", and "-" for unset. Do not adopt -j unless needed |
| A7 | Property value tables | HEAD matches our canonical maps: compress_table (module/zcommon/zfs_prop.c:109-200), zstd-fast-1 alias = ZIO_ZSTD_LEVEL_FAST_DEFAULT (include/sys/zio_compress.h:86-87), acltype_table (250-258), xattr_table (355-361), snapdir_table (229-234), copies_table (277-283). 2.2 and 2.4.4 UNVERIFIED | `internal/endpoint/props.go` maps | Match at HEAD. Drift would only refuse or misname a value | Low | Golden-test the maps against `zfs get -H` output on a 2.4.4 host |
| A8 | dedup values | HEAD dedup_table (zfs_prop.c:88-103) accepts sha256, sha512, skein, blake3 and `,verify` forms. props.go accepts only on, off, verify. 2.2 and 2.4.4 UNVERIFIED | We refuse the richer forms with InvalidArgument | Refuses valid input, no corruption | Low | Widen to HEAD's set, or document the subset |
| A9 | Source of user properties in `zfs get` | HEAD: `zfs recv` and inheritance produce source "received" and "inherited from" (`internal/zfs/zfs.go:43` Local() treats received as local) | `Local()` counts local and received as owned | A replica produced by `zfs recv` of a managed dataset (send -p or -R) shows as owned and committed to us. OpenZFS reports it as received, so the policy is ours to choose | Medium | Decide policy. Options: count only `local`, or mark received rows read-only. Add a test with a received managed row |
| A10 | Snapshot-name length and charset | HEAD accepts snapshot names per ZFS component rules. Our snapComponent (plan.go:421-430) keeps the name if ValidSnapshot and length fits, else csi-<sha256>. Not a drift, but the 128-byte rule is ours | Consistent with ZFS | None | Info | None |

Net 2.2 to 2.4.4 result: the OpenZFS-visible changes that matter to this code are few. The default volblocksize moved in 2.2, not after it (A1). Create -p semantics, the error strings the classifier needs, and the property tables we rely on are the same at the tags checked (A2, A3 at 2.4.4). Items that remain UNVERIFIED for 2.4.4 are A4, A5, A6, A7, A8.

## B. Where democratic-csi diverges from OpenZFS semantics (do NOT copy)

| # | DC behavior | evidence (DC HEAD) | OpenZFS-correct behavior | why not to copy |
|---|---|---|---|---|
| B1 | `zfs create -p` for volumes, after a racy pre-check | src/driver/controller-zfs/index.js:1149, 1656, 2325 (`parents: true`); pre-check via `zfs.get volsize/refquota` ~700-745 | `-p` on an existing dataset returns 0 (A2), so the pre-check is the only guard | A race lets DC stamp ownership on a dataset another driver just made. Our refusal of -p for volumes is right |
| B2 | Snapshot name cleansed to `[a-z0-9_\-:.+]` before use as the ZFS name | index.js:2257; ZFS name at 2315 `datasetName + "@" + name` | ZFS accepts a wider charset; the name is opaque to ZFS | Lossy: distinct CSI names can collide, and our raw-name approach keeps them distinct. Lookup must go by csi_snapshot_name property |
| B3 | Ownership read from any source (no local check) | DC reads `row[MANAGED]` from `zfs get` with default sources (index.js:355-366 and list loops) | Inherited user props are the parent's, not the dataset's | Treating inherited values as owned is wrong. Our local-only check is correct; keep it, and keep `received` under review (A9) |
| B4 | `detachedSnapshots`: snapshot as a `zfs send | receive` copy into a separate tree | index.js:2166-2175, 2312-2330; send path 965-985; `src/utils/zfs.js:1146-1175` | ZFS snapshots are CoW and native clones are the snapshot-to-volume path | A full copy is a DC design, not ZFS semantics. Keep it out of the endpoint. Detached ids have no `@` and our parser rejects them (endpoint.go parseSnapshotID). DeleteSnapshot returning OK for them is a silent leak (see C.4) |
| B5 | Swallowed send/receive "destination exists" error | index.js:973-985 and 1078-1090 (catch on err containing "destination" and "exists", then continue) | ZFS error should surface | Masks real failures and can leave a dataset without managed props (orphan). Do not copy the swallow |
| B6 | DeleteVolume refuses on ANY managed snapshot, including deferred | index.js:1440-1470 (`managed == "true"` only) | OpenZFS defer-destroy keeps a snapshot until its last clone goes; deleting a volume whose only remaining snapshot is deferred is safe if clones are gone | Our rule (refuse on non-deferred CSI snapshots, plus clone check) is closer to ZFS semantics. Keep ours |
| B7 | Volume id via `idTemplate` (dataset name != CSI name) | src/driver/index.js:459-468 | Dataset names are the object identity in ZFS | Breaks the name-keyed idempotency that ZFS gives us. Adopt only DC volumes whose csi_volume_name equals the requested name (see C.2) |
| B8 | Nested dataset names via '/' in volume id, created by -p | index.js:791 `datasetParentName + "/" + volume_id` plus B1 | Nested datasets are real ZFS children | Our volumeID refuses '/' and '@' (endpoint.go volumeID). Keep refusing |
| B9 | Share context stored as JSON in a user property | index.js:1296-1299 `SHARE_VOLUME_CONTEXT_PROPERTY_NAME` JSON.stringify | Export is outside ZFS | Not a ZFS property semantic. Node keys are a CSI-layer concern: keep them in the export layer, not in dataset properties, unless we need DC interop (C.1) |
| B10 | `zvolBlocksize` default "16K" written explicitly | index.js:1143-1146 | OpenZFS 2.2+ default is 16K (A1) | Not a divergence on 2.2+. Copy the explicit pin (A1 fix), not the DC default logic |

## C. Minimal read set for an opt-in adoption mode

Adoption means: take a DC-created dataset, under the configured Parent, into our table and treat it as ours for lifecycle (ListVolumes, GetVolume, Delete, Expand, snapshots). It does not mean writing DC properties or sharing state with DC.

C.1 Read for volumes (one `zfs get -Hp -o name,property,value,source` scan, same as today):
- ZFS native: type, volsize, volblocksize, refquota, refreservation, origin, creation, mountpoint, defer_destroy (already read in table.go columns()).
- DC user props required for adoption: democratic-csi:managed_resource, democratic-csi:provision_success, democratic-csi:csi_volume_name, democratic-csi:csi_volume_content_source_type, democratic-csi:csi_volume_content_source_id. All already read by table.go (props.go / endpoint.go names).
- Optional for lifecycle, needed only for DC node interop: democratic-csi:csi_share_volume_context. Read it if present (for export parity later); do not require it. Today a DC row without it is still adoptable for lifecycle.
- Do not read for adoption: volume_context_provisioner_driver / _instance_id, *_assets_name, freenas_*_id (share bookkeeping, used only by DC node/export paths).

C.2 Adoption admission rule (opt-in only):
- Adopt a row only if managed_resource == "true" on the dataset itself (local source, A9) AND provision_success == "true" AND csi_volume_name equals the requested CSI name. Refuse otherwise with AlreadyExists, matching the current behavior for unmanaged rows.
- Reject adoption when the DC idTemplate is in use (B7): the dataset name will not match the CSI name.

C.3 Read for snapshots:
- ZFS native: creation, origin, defer_destroy, volsize (for restore size).
- DC user props: managed_resource, csi_snapshot_name, csi_snapshot_source_volume_id. Match by csi_snapshot_name (not the ZFS name), so cleansed DC names still resolve (B2). Our PlanCreateSnapshot already scans SnapshotName across the table (plan.go, the `for r := range t.All()` loop), so this needs no new read.
- Do not adopt detached-mode snapshots (no '@'; B4). Return an explicit unsupported error on delete instead of OK (C.4).

C.4 Gaps the minimal mode must still surface (not silently accept):
- Detached snapshots (DC ids without '@'): reject with a typed error on CreateVolume and DeleteSnapshot. Today DeleteSnapshot returns nil for unparseable ids (endpoint.go DeleteSnapshot), which reports a leak as success.
- Rows with managed=true but without csi_share_volume_context: adopt for lifecycle, mark as "not publishable" in the Volume view when export exists.
- Received (replicated) managed rows: refuse or flag (A9) until policy is decided.

C.5 Writes: none to DC properties in adoption mode. Writing csi_share_volume_context is only needed when the export layer is built and DC node interop is wanted; then write the exact DC JSON keys (NFS: node_attach_driver, server, share; iSCSI: node_attach_driver, portal, portals, interface, iqn, lun; NVMe-oF: node_attach_driver, transport, transports, nqn, nsid; zfs-local: node_attach_driver, zfs_asset_name). Keep iscsi_assets_name / nvmeof_assets_name in sync for delete. These are from controller-zfs-generic/index.js:145-151, 378-390, 603-611 and controller-zfs-local/index.js:166-179.

## D. Other seams (carried from the earlier pass, now lower priority)

- Our sharenfs=off / sharesmb=off on every new filesystem (plan.go FS create) is OpenZFS-correct (explicit local value) and DC does not set it on create. A later export must overwrite it deliberately.
- props.go comment says volblocksize source is "-" because of dsl_prop_nvlist_add_uint64. In HEAD that function (module/zfs/dsl_prop.c:1318-1340) is used for zvol stats (module/zfs/zvol.c:269). The design compares BlockSz, so behavior is correct. Fix the comment. Mechanism UNVERIFIED.
- Dataset layout: DC variants all use Parent plus a detached parent that must not overlap (index.js:575-580). zfs-local examples use `.../v` and `.../s`. Point our Parent at the `.../v` dataset. Detached parent is out of scope.
- TrueNAS API variants write user props via middleware (src/driver/freenas/api.js:3195-3201). Whether they land as source "local" is UNVERIFIED. Check a live dataset before enabling adoption on TrueNAS.
- DC snapshot prefix `volume-source-for-snapshot-` (index.js:17) is not reserved by us (plan.go:388-390 reserves only the clone prefix). Reserve it.

## E. Not done
- Live zfs runs on 2.2 and 2.4.4 (no pool available). Everything is source-level or GitHits-grep-level.
- Per-version DC history (git log -S): impossible with the local clone.
- 2.2.9 classifier-string grep timed out; re-run to close A3 on 2.2.
