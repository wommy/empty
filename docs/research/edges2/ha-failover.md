# HA failover edge: ZFS pool, exports and targets across two heads

## EDGES
- Boundary: the pool (shared JBOD) moves between heads. Crossing it: pool import/export, LIO configfs objects (iSCSI target, TPG, LUN, backstore), NFS export state (exportfs / ganesha RecoveryDir), and client-visible identities (IQN, NQN, LUN serial/WWN).
- Pacemaker resource agents (github:clusterlabs/resource-agents@1cf6c3c8):
  - heartbeat/ZFS: import uses `zpool import [-f] -o cachefile=none`; `-f` only if `importforce` is set (ZFS:274-299). Export is `zpool export -f`, with the comment "Always make sure the pool export is last in your failover logic" (ZFS:305-315).
  - heartbeat/iSCSILogicalUnit: lio-t path creates the backstore, then adds it to the TPG as a LUN; `lio_iblock` must be distinct per LUN to avoid a tcm_core mkdir race on node failover (iSCSILogicalUnit.in:294-300, 481-491).
  - heartbeat/exportfs: `wait_for_leasetime_on_stop` rides out the NFSv4 lease (exportfs:127-137).
  - heartbeat/ganesha-nfs: RecoveryDir must sit on the replicated filesystem or clients lose locks (ganesha-nfs:273-276).
- democratic-csi baseline (github:democratic-csi/democratic-csi@69c7ebcd, src/driver/controller-zfs-generic/index.js): the `pcs` iSCSI strategy issues `pcs resource create --future --force target-<asset> ocf:heartbeat:iSCSITarget implementation="lio-t" iqn="<basename>:<asset>"`, then `lun-<asset>` (iSCSILogicalUnit) with `path=/dev/<disk>`, grouped under `pcs_group` with `--wait` (lines 307-348). Delete mirrors this (lines 759-786). It manages LIO through Pacemaker, but I did not see pool import/export ordering in this file.
- Not researched (rate limit / time): TrueNAS HA middleware, ewwhite/zfs-ha, RSF-1. Treat as unverified.

## SEAMS
- Pool ownership: ZFS resource agent (or own `zpool import`/`export` behind the Runner seam), with Pacemaker ordering `pool -> daemon -> IP/exports`, and export last on stop.
- LIO/NFS: reuse ocf:heartbeat:iSCSITarget/iSCSILogicalUnit (lio-t) or drive configfs directly; reuse exportfs for NFS. License: resource-agents is GPL-2.0 (not verified in this session).
- Client identity: derive IQN deterministically from `basename + asset name` (as the pcs strategy does) and store the rest as ZFS user properties on the zvol.

## GAPS / ANSWERS
1. Moving the pool. Pacemaker agents are the mature pattern. The gap is that none of the agents shown rebuild the endpoint's own state (typed volume/snapshot records) from ZFS properties. The endpoint must do that.

2. Fencing and multihost.
   - Guarantees (zpoolprops.7:597-626): with `multihost=on`, `zpool import` runs an activity check and refuses to import a pool that looks active on another host unless `-f` is given. Each host must set a unique hostid. Periodic MMP writes show the pool is in use.
   - MMP timing (zfs.4): the check lasts about `zfs_multihost_interval x zfs_multihost_import_intervals` (default 1000 ms x 20), with a 1 s floor. An importing host's own writer suspends the pool after `zfs_multihost_fail_intervals` (default 10) missed writes.
   - Not guaranteed (zpoolprops.7:606-608): protection on import only, not during operation. It does not stop a device being used by two pools. The activity check is timing-based. The old head's LIO/NFS can keep serving clients while the pool moves. `zpool import -f` (zpool-import.8:270-285) bypasses the check.
   - So multihost is not fencing. You still need STONITH or equivalent fencing before import, and the LIO/NFS services must be stopped on the old head.

3. Endpoint daemon.
   - Placement: run it on both heads as a standby, or as a Pacemaker resource ordered after the pool import. A pool-following daemon is simpler. Either way, do not let it serve before the pool is imported and the state is reconciled.
   - Rebuild: on start, read the ZFS user properties (democratic-csi:* names) for every dataset/zvol, then converge LIO/NFS configfs idempotently. The config should be name-keyed, as already decided.
   - Stable identities: IQN from `basename:<name>` (deterministic, per pcs strategy). NQN and LUN serial/WWN are not in the code I read, so store them as ZFS properties on the zvol so both heads agree. Unverified in the sources I checked.
   - NFS: keep RecoveryDir and sharenfs state on the pool so the export state moves with it.
