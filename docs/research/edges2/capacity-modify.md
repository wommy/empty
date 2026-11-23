# Capacity, accounting, mutable parameters (edge: CAPACITY / ACCOUNTING / MUTABLE)

Pins: democratic-csi@69c7ebcd, openebs/zfs-localpv@8759e507, truenas/truenas-csi@6443642b, SynologyOpenSource/synology-csi@02af05d9, openzfs/zfs@a128b8a0.

## 1. GetCapacity semantics

- **democratic-csi (zfs):** returns `zfs get avail` of the parent dataset, raw. `controller-zfs/index.js:1655-1664`. ZFS `available` is already net of children and parent reservations (`zfsprops.7` available, refreservation). No overcommit term. Sparse zvols: when `zvolEnableReservation` is false it sets `refreservation=0` (`index.js:900-903`), so a sparse volume reserves nothing and its volsize is not subtracted. Reported headroom therefore ignores overcommit.
- **openebs/zfs-localpv:** `maxFreePool` over matching pools on topology-matching nodes, using node-agent reported `Free`, returning the single roomiest pool (`pkg/driver/controller.go:1146-1170`). Only thick volumes (non-thin zvol, or `thinprovision: "no"` dataset) reserve space in placement (`docs/scheduler.md:19-23`). Thin volumes are placed regardless of free space. Not minus-reservations; overcommit allowed for thin.
- **truenas/truenas-csi:** `GetAvailableSpace` reads the `available` property of the pool resource (`pkg/client/storage.go:1364-1403`); `GetCapacity` returns it for the `pool` parameter (`pkg/driver/controller.go:1857-1881`). Pool-level, no reservation or thin handling.
- **synology-csi:** not ZFS. Sums `Free` across DSM volumes, optionally by `location` (`pkg/driver/controllerserver.go:407-435`).

## 2. refquota vs quota; refreservation vs volsize; rounding

- **Filesystems:** `refquota` excludes descendants; `quota` includes them (`zfsprops.7` refquota, quota). democratic-csi sets `refquota` and optionally `refreservation` to capacity (`index.js:1162-1171`, `1588-1597`). openebs picks quota vs refquota by `QuotaType` and maps the reservation name (`pkg/zfs/zfs_util.go:414-436`, `1124-1138`). Use refquota for per-volume datasets; use quota only when you need a subtree cap.
- **Zvols:** a non-sparse volume gets refreservation equal to volsize by default; sparse means refreservation below volsize plus metadata (`zfsprops.7` volsize, 1993-2052). democratic-csi goes sparse by zeroing refreservation; openebs uses `zfs create -s` for thin (`pkg/zfs/zfs_util.go:189-191`) and a reservation otherwise.
- **Rounding:** volsize must be a multiple of volblocksize (`zfsprops.7:2005-2007`). democratic-csi calls `generateZvolSize(capacity, volblocksize)` at create and expand (`index.js:767-772`, `1550-1555`; helper `src/utils/zfs.js:184`). Rounding direction not verified past the block-size table. openebs rounds up to whole Gi/Mi (`pkg/driver/controller.go:170-178`), which keeps volsize a multiple of any power-of-two volblocksize up to 1M. Default volblocksize 8k (`pkg/apis/openebs.io/zfs/v1/zfsvolume.go:98-101`).
- **RAID-Z padding:** RAID-Z skip-sector padding inflates on-disk allocation (`openzfs/zfs module/zfs/vdev_raidz.c:555`, `745-747`). None of the drivers accounts for it (no raidz term in their rounding or capacity code). So `capacity_bytes >= requested` holds at the logical level, but a thick reservation can fail on a pool with apparently enough `avail`. Magnitude unverified.

## 3. ControllerModifyVolume and mutable parameters

- **No ZFS driver implements it.** openebs returns `Unimplemented` (`pkg/driver/controller.go:1367-1371`). truenas returns an empty success no-op (`pkg/driver/controller.go:2421-2424`). democratic-csi `src/` has no handler (grep empty); only the CSI proto carries `mutable_parameters` (`csi_proto/csi-v1.11.0.proto:1018-1043`). Synology: no match.
- **openebs runtime property path (not via ControllerModifyVolume):** a ZFSVolume CR update runs `zfs set` for recordsize (datasets only), atime (datasets only), dedup, compression, logbias and user properties (`pkg/zfs/zfs_util.go:373-411`, `716-756`). Capacity is changed via `buildVolumeResizeArgs` (`414-436`). `sync` and `refreservation` directly are not mapped.
- **democratic-csi:** properties only at create, and quota/refreservation/volsize at ControllerExpandVolume (`index.js:1582-1601`).
- **Safe live (apply on set):** volsize, quota, refquota, reservation, refreservation (volsize shrink is hazardous, `zfsprops.7` volsize 2013-2015); sync and logbias act on subsequent sync requests (behavior text in `zfsprops.7`, sync ~1967; exact live semantics unverified).
- **New writes only:** compression (`zfsprops.7:843`), recordsize (affects files created afterward, `1651-1652`), copies (`1030`).
- **Not changeable after creation:** volblocksize (`zfsprops.7:579-584`), encryption (`1189-1197`).

## Implication for the endpoint

Map ModifyVolume onto quota/refquota/volsize/refreservation with shrink guards and an explicit thick/thin transition. Map compression, sync, logbias and atime as live. Reject recordsize, copies, volblocksize and encryption, or accept them with a new-writes-only status.
