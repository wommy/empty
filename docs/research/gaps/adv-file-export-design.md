# Adversarial review: file-export (NFS) design

Target: `scratchpad/align/gaps/file-export.md`. Read against `/home/user/empty` (README.md, internal/endpoint/{table,plan,apply,endpoint}.go, api/zfsendpoint/v1/endpoint.proto), `docs/research/edges/file-export.md`, and `docs/research/edges2/privilege-tenancy.md`. OpenZFS pin a128b8a0 (via GitHits).

## Recommendation (summary)

The data-oriented shape holds up: Row columns, pure planners, ops with replay tolerance. The API shape and the unexport and liveness predicates are wrong and need fixing before implementation.

1. Change the client-list API from a whole-list replace to per-client publish and unpublish, mapped to CSI ControllerPublish and ControllerUnpublish.
2. Stamp `sharenfs=off` locally at create and clone time.
3. Make unexport check liveness, not only the property.
4. Return FAILED_PRECONDITION for unmounted volumes.
5. Gate the capability on root.

## Findings, ranked

### 1. HIGH: Unexport reports OK while the volume is still exported
`PlanUnexportVolume` returns nil when sharenfs is locally "off" and never looks at `NFSLive`. `zfs set sharenfs=off` commits the property in the kernel before libzfs unshares (changelist postfix). If the unshare step fails, the property is already off but the line and the kernel export stay. The unshare step fails when the endpoint runs as non-root with a delegated `sharenfs` grant (it cannot write /etc/exports.d or run `exportfs -ra`), or when `exportfs` fails. The first call errors. The retry sees local "off", returns an empty plan, and reports OK while clients still have access.
- Mirror case: a failed ExportVolume leaves the property set. Nothing is live now, but `zfs-share.service` exports it at the next boot, after the caller saw an error and maybe gave up.
- **Fix:** unexport plans `OpUnshareNFS` (`zfs unshare <ds>`, OkMissing) whenever `NFSLive` is true, whatever the property says. Add a property test that `NFSLive` is false after Unexport.

### 2. HIGH: Whole-list `ExportSpec` has no owner. Last writer wins, and CSI cannot drive it.
`sharenfs` is one string. `ExportVolume(id, spec)` replaces it, so the second front end, or the second concurrent ControllerPublish from the same front end, silently drops the first one's clients. Their live mounts then get EACCES or ESTALE on the next export-cache refresh. CSI ControllerPublish carries one `node_id`, so a whole-list API forces the front end to hold the client set and read-modify-write it. That puts state in the "thin translator" and races across front-end replicas.
- **Fix:** replace the RPCs with `PublishExport(volume_id, client, readonly)` and `UnpublishExport(volume_id, client)`. The planner parses the current `rw=`/`ro=` lists from the Row, adds or removes one host, and re-emits `Canonical()`. All of this runs under `vol:<id>`, so the read-modify-write is serialized by the endpoint.
- CSI idempotency: the same host with the same mode gives an empty plan. The same host with the other mode is ALREADY_EXISTS. Unpublishing a host that is not in the list is OK. When the last host is removed, the value becomes local "off", not inherit.
- This needs a `Parse(sharenfs) (ExportSpec, error)` as the inverse of `Canonical`. An unparseable adopted value (for example democratic-csi's driver-wide string) is FAILED_PRECONDITION, not a silent overwrite.
- Squash and anon IDs stay volume-wide (per-client squash is impossible, per the design's finding (d)). Set them once, for example from StorageClass parameters at the first publish. A later publish that conflicts is ALREADY_EXISTS.

### 3. HIGH: Ancestor `sharenfs` exports new and restored volumes before any Export call
Volumes are created and cloned with only ownership props (plan.go `own`), so `sharenfs` is inherited from the parent chain. `zfs clone` does not copy the origin's local `sharenfs`. The clone takes inheritable props from its new parent, so a restore does not pick up the source's client list. But if any ancestor has `sharenfs=on`, every CreateVolume and every snapshot or volume restore is exported `rw,crossmnt` to `*` at mount time. That is a restored backup reachable by the world, with no ExportVolume call.
- The design's "startup check of the parent" misses an admin change made after startup, and misses grandparents. scan drops the parent row (table.go `r.Name == e.cfg.Parent`), so the planner cannot see it either.
- **Fix:** add `sharenfs: off` to the `own` map for `OpCreateFS` and `OpClone` (filesystems only; a zvol rejects it). The property is atomic with creation, needs no new op, and leaves the `compatible()` check untouched.
- For adopted volumes with an inherited value, the existing `!ShareNFSLocal → set` rule already pins them on the first Export or Unexport. Also report the effective value and its source in `Volume.nfs_export`, so an inherited export is visible.
- Answer to the question: yes, an inherited sharenfs is "exported". The kernel exports it, and the API must say so.

### 4. MEDIUM-HIGH: Export returns OK on an unmounted volume with nothing served
The plan is "if value wrong: set; else if `Mounted && !NFSLive`: share; else nil". With `canmount=off`/`noauto` on the volume, `mountpoint=none|legacy` (inherited from the parent), an unloaded encryption key, or a non-root endpoint (it cannot mount, per privilege-tenancy), libzfs skips the share (`libzfs_mount.c:748`). The plan then converges to empty and returns OK, and the client's mount fails.
- `canmount=off` on the *parent* is harmless, because canmount is not inherited.
- **Fix:** after the value check, `!Mounted` is FAILED_PRECONDITION ("volume not mounted on the storage host"). Add `Mounted` and `Mount` to the planner test matrix with `none` and `legacy`.

### 5. MEDIUM: The exported path is not stable, and the design hands it out once
`Volume.mountpoint` is the `mountpoint` property value, inherited as `<parent mp>/<id>`. If an admin changes the parent's mountpoint, ZFS's changelist remounts and re-shares every child at the new path. Any `server:path` persisted at CreateVolume (PV volumeAttributes, as democratic-csi does with `share: mountpoint`) is now wrong for every new node mount. Existing mounts may survive because knfsd uses fsid/uuid, but that is unverified.
- **Fix:** return the export path from a fresh scan in the publish response, which becomes CSI `publish_context`, so each attach gets the current path. Do not make the PV carry it.
- Optional hardening: create with a locally set `mountpoint`, so parent changes do not move volumes. That costs democratic-csi parity, so do not do it by default.

### 6. MEDIUM: The export path is impossible without root
Delegating `sharenfs` lets a non-root endpoint set the property. The share itself, writing `/etc/exports.d/zfs.exports` and running `/usr/sbin/exportfs -ra` (`libzfs_share_nfs.c:514-527`), runs as the caller and needs root. Mounting is root-only in libzfs on Linux (`libzfs_mount_os.c:556-559`), so a non-root endpoint's volumes are never mounted and finding 4 always fires. Combined with finding 1, non-root is unsafe, not only non-functional.
- **Fix:** advertise `CAPABILITY_EXPORT_NFS` only when euid==0, or when the root helper proposed in privilege-tenancy.md is present. Otherwise the RPCs return UNIMPLEMENTED.

### 7. MEDIUM: `NFSLive` reads the file, not the kernel
A line in zfs.exports does not mean nfsd exports it. nfs-server may be stopped, `exportfs -ra` may have failed after the line was written, or a boot `zfs share -a` truncation may have happened mid-run. The plan is then empty and the call returns OK.
- **Fix:** rename the column to `NFSLine` and document it as a configuration-intent check. In apply, surface a non-zero `zfs share` exit; libzfs propagates the exportfs result.
- Optionally read `/var/lib/nfs/etab`. It is still one read, and it reflects the last successful exportfs.
- Note: this read makes the "single scan" two sources. The `zfs get -Hp` scan can carry `sharenfs` (with source, so `Prop.Local()` works) and `mounted` as plain columns. The file read must happen once per operation, not once per `scanFor` sub-scan, or rows decode against different file snapshots.

### 8. LOW-MEDIUM: The global `share:nfs` lock buys nothing and adds contention
The design's own claim (c) says file rewrites are already serialized by libzfs flock, and reloads by exportfs lockf. CreateVolume (clones under a sharing ancestor) and DeleteVolume (destroy unshares, see finding 9) also mutate zfs.exports without taking `share:nfs`, so the lock does not even give exclusion. What it does do is serialize every publish host-wide behind an `exportfs -ra`, whose cost grows with the export count. With a 10 s provisioner timeout and the 1 s lock margin, a burst of publishes turns into ABORTED retries.
- **Fix:** drop it and keep only `vol:<id>`.

### 9. LOW (design risk resolved): Destroy does unshare
`zfs destroy` follows this path: `destroy_callback` → `zfs_unmount` (`cmd/zfs/zfs_main.c:1534`) → `zfs_unshare(..., share_all_proto)` + `zfs_commit_shares` (`lib/libzfs/libzfs_mount.c:646-650`), but only when the filesystem is mounted. The "unexport before DeleteVolume" step is unnecessary, and CSI already orders ControllerUnpublish before DeleteVolume.
- Real hazard: if NFSv4 clients still hold opens or locks, the unmount fails EBUSY. libzfs then re-shares (`libzfs_mount.c:652-655`), and destroy fails. That maps to `ErrBusy` → FAILED_PRECONDITION with state unchanged. Retry converges, which is acceptable.
- **Fix:** delete the risk line. Add a real-ZFS CI case: destroy while exported, then assert the exports line is gone.

### 10. LOW: Property length caps per-node client lists
String property values are capped near `ZFS_MAXPROPLEN` (about 4 KiB; verify against `zfs_valid_proplist`). At about 16 bytes per IPv4 host, that is around 250 nodes, fewer with IPv6.
- **Fix:** Canonical returns RESOURCE_EXHAUSTED when it exceeds the cap, and the docs point large clusters to the CIDR (static) mode below.

## CSI mapping: CreateVolume or ControllerPublish?

- **democratic-csi:** exports at CreateVolume, with one driver-wide static `sharenfs` string (index.js:120-131). It has no per-node publish.
- **csi-driver-nfs:** exports nothing itself. It carves subdirectories of a pre-existing export.
- **TrueNAS drivers:** create the share at CreateVolume, with networks and hosts from StorageClass config. This is from memory and was not re-verified in this review.

None of these drivers derives clients from node IDs.

Recommendation for this endpoint: **ControllerPublish**, with a static-CIDR fallback.
- The endpoint's value over the baseline is a least-privilege client list that ControllerUnpublish also revokes. Force-detach of a dead node removes its access, which is real fencing.
- Use the publish and unpublish RPCs from finding 2. The front end advertises `PUBLISH_UNPUBLISH_VOLUME`.
- The CSI `node_id` is opaque, so NodeGetInfo must return, or map to, a stable storage-network address, not a hostname that libzfs would resolve at exportfs time.
- Keep a "static" mode for parity and for large clusters: StorageClass CIDRs applied by one `PublishExport` at create time, with no attacher. Both modes write the same property through the same planner.
