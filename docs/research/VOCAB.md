# Canonical operation vocabulary (map every repo onto these ids; do not invent new ids
# unless nothing fits -- then use prefix "x." and explain in notes)

## csi.* (CSI spec RPCs)
csi.CreateVolume csi.DeleteVolume csi.ControllerExpandVolume csi.ControllerGetVolume
csi.ControllerPublishVolume csi.ControllerUnpublishVolume csi.ListVolumes csi.GetCapacity
csi.CreateSnapshot csi.DeleteSnapshot csi.ListSnapshots csi.ControllerModifyVolume
csi.NodeStageVolume csi.NodeUnstageVolume csi.NodePublishVolume csi.NodeUnpublishVolume
csi.NodeExpandVolume csi.NodeGetVolumeStats csi.NodeGetInfo csi.GetPluginCapabilities csi.Probe

## zfs.* (storage primitives, however executed: zfs CLI, libzfs, REST, JSON-RPC)
zfs.dataset.create zfs.zvol.create zfs.destroy zfs.destroy.recursive zfs.destroy.defer
zfs.snapshot.create zfs.snapshot.destroy zfs.snapshot.list zfs.clone zfs.promote zfs.rename
zfs.prop.get zfs.prop.set zfs.prop.inherit zfs.userprop.get zfs.userprop.set
zfs.quota zfs.refquota zfs.reservation zfs.refreservation zfs.volsize.resize
zfs.list.children zfs.send zfs.receive zfs.hold zfs.release zfs.mount zfs.unmount
zfs.space.available zfs.pool.list zfs.job.wait (async job/task polling)

## share.* (export side)
share.nfs.create share.nfs.delete share.nfs.lookup
share.smb.create share.smb.delete share.smb.lookup
share.iscsi.target.create share.iscsi.target.delete share.iscsi.lun.create share.iscsi.lun.delete
share.iscsi.lun.map share.iscsi.lun.unmap share.iscsi.lun.resize share.iscsi.auth (CHAP)
share.nvmeof.subsys.create share.nvmeof.subsys.delete share.nvmeof.ns.create share.nvmeof.ns.delete
share.nvmeof.port.bind share.service.reload

## node.* (consumer side)
node.iscsi.discover node.iscsi.login node.iscsi.logout node.iscsi.rescan node.iscsi.session.persist
node.nvme.connect node.nvme.disconnect node.device.resolve (find /dev by serial/wwn/nqn)
node.multipath node.fs.format node.fs.mount node.fs.unmount node.fs.resize node.fs.stats
node.bind.mount node.nfs.mount node.smb.mount node.block.publish (raw block)

## meta.*
meta.state.store (where volume/snapshot metadata lives: zfs userprops / CRDs / API object / none)
meta.id.scheme (how volume_id / snapshot_id are formed)
meta.lock (concurrency control)
meta.idempotency (how repeated create/delete is made safe)

## OpenZFS anchors (ground truth from openzfs/zfs include/libzfs_core.h, libzfs.h, sys/fs/zfs.h)
zfs.dataset.create   -> lzc_create(DMU_OST_ZFS) / zfs create
zfs.zvol.create      -> lzc_create(DMU_OST_ZVOL, volsize, volblocksize) / zfs create -V [-s]
zfs.destroy          -> lzc_destroy / zfs destroy
zfs.destroy.defer    -> lzc_destroy_snaps(defer=B_TRUE) / zfs destroy -d
zfs.snapshot.create  -> lzc_snapshot (atomic, multi) / zfs snapshot
zfs.snapshot.destroy -> lzc_destroy_snaps
zfs.clone            -> lzc_clone / zfs clone
zfs.promote          -> lzc_promote / zfs promote
zfs.rename           -> lzc_rename / zfs rename
zfs.rollback         -> lzc_rollback_to
zfs.hold/release     -> lzc_hold / lzc_release / lzc_get_holds
zfs.send/receive     -> lzc_send* / lzc_receive*
zfs.exists           -> lzc_exists
zfs.prop.get         -> lzc_get_props (snapshots/bookmarks) ; zfs_prop_get (libzfs) ; zfs get -Hp
zfs.prop.set/inherit -> zfs_prop_set / zfs_prop_inherit (libzfs; no lzc equivalent)
zfs.userprop.*       -> zfs_prop_get_userprop ("ns:name" user props, ZAP)
zfs.quota/refquota/reservation/refreservation/volsize -> ZFS_PROP_QUOTA/REFQUOTA/RESERVATION/REFRESERVATION/VOLSIZE
zfs.space.available  -> ZFS_PROP_AVAILABLE / ZFS_PROP_USED / ZFS_PROP_LOGICALUSED
errors: EZFS_EXISTS (idempotent create), EZFS_NOENT (idempotent delete), EZFS_BUSY (dependent clones / in use)
