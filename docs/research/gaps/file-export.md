# File export (NFS) gap

Pins: openzfs a128b8a0; nfs-utils 5090e0bb (GitHits, not cloned). Brief path `lib/libshare/os/linux/nfs.c` does not exist at this commit; the code is `lib/libzfs/os/linux/libzfs_share_nfs.c`.

## CLAIMS CHECKED
- (a) set/inherit idempotent: BROKEN for inherit. `sharenfs` is PROP_INHERIT (module/zcommon/zfs_prop.c:585-587), so inherit yields an ancestor's value, not off. Baseline deleteShare inherits (democratic-csi index.js:634-641). Set is idempotent in content: the mountpoint line is removed and re-added under flock (lib/libzfs/libzfs_share_nfs.c:244-277).
- (b) survives reboot: HOLDS. zfs-share.service runs `zfs share -a`, Before=nfs-server, After=zfs-mount (etc/systemd/system/zfs-share.service.in:4-17). Caveats: `zfs share -a` truncates zfs.exports first (cmd/zfs/zfs_main.c:7533-7535), and unmountable datasets are skipped (libzfs_mount.c:748).
- (c) exports file and reload: HOLDS. Path /etc/exports.d/zfs.exports (os/linux/libzfs_share_nfs.c:33-35); `exportfs -ra` (:520-526). Race: file rewrites are serialized by flock (lib/libzfs/libzfs_share_nfs.c:244-277). The reload runs after that lock is released (libzfs_mount.c:814-821), but nfs-utils exportfs takes lockf on its etab lock (utils/exportfs/exportfs.c:94-114, 238), so reloads serialize. Unverified: nfs-utils reading exports.d.
- (d) per-client syntax: PARTLY BROKEN. `rw=`/`ro=` take ':'-separated hosts, strip '@', accept `[v6]/len`, and bare `rw` means `*` (libzfs_share_nfs.c:137-210, 317-353). All other options are global: every host line gets the same options (:410-435), so squash cannot differ per client. The man example `root=neo` (man/man8/zfs.8 Example 16) is rejected on Linux, since `root` is not in valid_keys (:303-309). `sec=` is order-sensitive (:137-138).
- README quote "export setup will slot in before the marker": not in the repo. README:54 only says to keep the marker.
- Baseline per-client: none per volume. Only a driver-wide static property string (index.js:120-131).

## DESIGN
Decision: keep export separate from CreateVolume. Folding would make the committed-row check (`compatible`, plan.go) blind to share state, and would hold the host-global share lock during provisioning.

Row (decoded in scan, one `zfs list` plus one read of zfs.exports):
- `ShareNFS string`: stored value.
- `ShareNFSLocal bool`: Prop.Local().
- `Mounted bool`.
- `NFSLive bool`: a line exists for the mountpoint.

Op kinds:
- `OpSetShareNFS{Target, Value}`: `zfs set sharenfs=`. "off" is unexport. OkMissing.
- `OpShareNFS{Target}`: `zfs share <ds>`, used when the property is right but the line is missing. Unverified: that a re-share returns 0.
- No inherit op. Ancestors are invisible to the planner (scan drops the parent row, table.go), so inherit can re-export.

ExportSpec: `Clients []Client{Host, RW}`, `Squash` (Root|All|None, uniform), `AnonUID`, `AnonGID`.
`Canonical() (string, error)` is pure. It rejects empty client lists, a host in both groups, and hosts containing `,:= ` unless bracketed. It sorts hosts and emits `rw=...,ro=...,root_squash` (anonuid/anongid only with all_squash). It never emits `sec`.

Planners (pure):
- `PlanExportVolume(t Table, id string, spec ExportSpec) (Plan, error)`: block kind is InvalidArgument (sharenfs is filesystem-only). Unmanaged is FailedPrecondition. Then: if `ShareNFS != want || !ShareNFSLocal`, emit OpSetShareNFS. Else if `Mounted && !NFSLive`, emit OpShareNFS. Else nil. Re-export is therefore empty.
- `PlanUnexportVolume(t, id) (Plan, error)`: missing is nil. If local "off", nil. Otherwise OpSetShareNFS "off".

Proto: add CAPABILITY_EXPORT_NFS=8; `Client{host, rw}`; `Squash` enum; `NFSExportSpec`; `rpc ExportVolume` / `rpc UnexportVolume`; `string nfs_export` on Volume. Locks: keys `vol:<id>` plus global `share:nfs`.

## TEST PLAN
- Planner table tests: one case per Row state (block, unmounted, inherited, value-right-but-not-live, ancestor-on). Golden Canonical strings, including IPv6 and conflicts.
- Property test: Plan(apply(Plan)) is empty.
- Sim: fake zfs with PROP_INHERIT semantics, exports path parameterised.
- Real-ZFS CI (root, file pool, nfs-kernel-server, gated on nfsd): 20 concurrent exports, `exportfs -v` matches; `zfs share -a` restores after truncation.

## RISKS
- Ancestor `sharenfs=on` exports every volume. Mitigate with a startup check of the parent.
- Boot `zfs share -a` truncates all of zfs.exports.
- Squash cannot vary per client; that needs ganesha.
- Out-of-band `zfs` commands bypass the endpoint lock.
- Destroy unshare path unverified (libzfs_dataset.c has none); unexport before DeleteVolume.
