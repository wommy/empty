#!/usr/bin/env python3
# Builds align/go-zfs-family.jsonl. Field limits from align/SCHEMA.md:
# how <=20 words, notfound <=12 words, notes <=25 words.
import json
import sys

OUT = "/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/align/go-zfs-family.jsonl"

R1 = "zfs-localpv"
R2 = "mistifyio/go-zfs"
R3 = "podman-storage-zfs"

rows = []


def L(repo, op, impl, how, idem, nf, notes):
    rows.append({
        "repo": repo, "op": op, "impl": impl, "how": how,
        "idempotent": idem, "notfound": nf, "notes": notes,
    })


# ---------------- zfs-localpv: csi.* ----------------
L(R1, "csi.CreateVolume", "pkg/driver/controller.go:583",
  "ZFSVolume CR created in openebs namespace; agent runs zfs create or clone",
  "yes", "CR Get; same-capacity Ready CR returned; other capacity AlreadyExists",
  "Per-process volume lock; nodes tried in order with retry on failure; pool fit is best-effort.")
L(R1, "csi.DeleteVolume", "pkg/driver/controller.go:642",
  "Deletes ZFSVolume CR; agent zfs destroy -r runs via finalizer; annotated if snapshots remain",
  "yes", "CR NotFound or deletionTimestamp set returns success",
  "Refuses while volume not Ready; remaining snapshots defer deletion via annotation rather than failing.")
L(R1, "csi.ControllerExpandVolume", "pkg/driver/controller.go:788",
  "Writes new capacity into ZFSVolume CR spec; zfs resize happens later on node",
  "yes", "CR Get failure returned as Internal, not NotFound",
  "Controller never runs zfs set or volsize; resize occurs only in NodeExpandVolume, so it depends on a node call.")
L(R1, "csi.ControllerGetVolume", "pkg/driver/controller.go:1360",
  "Unimplemented stub", "n/a", "n/a",
  "Returns Unimplemented; ZFSVolume CR state is not exposed through this RPC.")
L(R1, "csi.ControllerPublishVolume", "pkg/driver/controller.go:1102",
  "Unimplemented; node-local volumes need no attach step", "n/a", "n/a",
  "Topology key pins each volume to its owner node instead of attaching it.")
L(R1, "csi.ControllerUnpublishVolume", "pkg/driver/controller.go:1090",
  "Unimplemented; no attach step", "n/a", "n/a",
  "Mirror of ControllerPublishVolume; no controller-side detach exists.")
L(R1, "csi.ListVolumes", "pkg/driver/controller.go:1221",
  "Unimplemented stub", "n/a", "n/a",
  "Returns Unimplemented; ZFSVolume CRs are not listed through CSI.")
L(R1, "csi.GetCapacity", "pkg/driver/controller.go:1114",
  "Sums free pool space from ZFSNode CR cache, filtered by topology and pool parameters",
  "n/a", "Parameter errors return capacity 0 with nil error",
  "Bad storageclass pool parameters silently report zero capacity (controller.go:1142-1143).")
L(R1, "csi.CreateSnapshot", "pkg/driver/controller.go:886",
  "Creates ZFSSnapshot CR; agent runs zfs snapshot; wait parameter polls CR Ready",
  "yes", "CR Get; same-source snapshot reused; other source returns AlreadyExists",
  "Source check (verifySnapshotRequest) runs before the volume lock is taken, so it can race.")
L(R1, "csi.DeleteSnapshot", "pkg/driver/controller.go:988",
  "Deletes ZFSSnapshot CR; agent zfs destroy runs via finalizer; ZV removed if last",
  "yes", "CR NotFound or id without '@' returns success",
  "Malformed snapshot id returns success silently; volume CR removed only when last snapshot and eligible.")
L(R1, "csi.ListSnapshots", "pkg/driver/controller.go:1078",
  "Unimplemented stub", "n/a", "n/a",
  "Returns Unimplemented; snapshot listing is internal (label-selected CR list only).")
L(R1, "csi.ControllerModifyVolume", "pkg/driver/controller.go:1367",
  "Unimplemented stub", "n/a", "n/a",
  "Returns Unimplemented; volume properties change only through StorageClass and CR updates.")
L(R1, "csi.NodeStageVolume", "pkg/driver/agent.go:314",
  "Unimplemented; NodePublishVolume mounts directly", "n/a", "n/a",
  "No staging path; each target is mounted at publish time.")
L(R1, "csi.NodeUnstageVolume", "pkg/driver/agent.go:326",
  "Unimplemented; NodeUnpublishVolume unmounts directly", "n/a", "n/a",
  "No staging path to unstage; matches NodeStageVolume.")
L(R1, "csi.NodePublishVolume", "pkg/driver/agent.go:141",
  "Reads ZFSVolume CR, then formats and mounts zvol, mounts dataset, or bind-mounts block device",
  "partial", "Already-mounted check (verifyMountRequest) returns success",
  "Zvol formatted on first mount; block path (mount.go:293) skips the already-mounted check.")
L(R1, "csi.NodeUnpublishVolume", "pkg/driver/agent.go:178",
  "Unmounts target, restores legacy mountpoint, removes target path",
  "yes", "Not-mounted target returns nil; missing ZFSVolume CR fails Internal",
  "Requires the CR even when the target is already unmounted, so cleanup can stall if the CR is gone.")
L(R1, "csi.NodeExpandVolume", "pkg/driver/agent.go:345",
  "zfs set quota or volsize via CLI, then resize2fs or xfs_growfs when path is a directory",
  "partial", "Missing CR returns NotFound; failed set reverts to previous reservation",
  "ResourceExhausted is chosen by matching 'size is greater than available space' in error text.")
L(R1, "csi.NodeGetVolumeStats", "pkg/driver/agent.go:408",
  "statfs on mount path after mount-point check; bytes and inodes returned, no zfs call",
  "n/a", "Non-mount path returns NotFound",
  "Reports filesystem usage; ZFS quota and available properties are not consulted.")
L(R1, "csi.NodeGetInfo", "pkg/driver/agent.go:217",
  "Reads Kubernetes Node object for labels; adds ZFS topology key",
  "n/a", "Missing Node object returns raw error",
  "Topology segment set to node name when key absent (agent.go:264-265).")
L(R1, "csi.GetPluginCapabilities", "pkg/driver/identity.go:90",
  "Advertises CONTROLLER_SERVICE and VOLUME_ACCESSIBILITY_CONSTRAINTS",
  "n/a", "n/a",
  "Node RPC capabilities (GET_VOLUME_STATS, EXPAND_VOLUME) are declared separately in agent.go:282.")
L(R1, "csi.Probe", "pkg/driver/identity.go:74",
  "Always returns empty ProbeResponse", "n/a", "n/a",
  "No zfs, pool or agent health check; always reports ready.")

# ---------------- zfs-localpv: zfs.* ----------------
L(R1, "zfs.dataset.create", "pkg/zfs/zfs_util.go:544",
  "zfs create -o mountpoint=legacy via buildDatasetCreateArgs after zfs list precheck",
  "yes", "zfs list precheck; any list error treated as absent",
  "CLI not lzc_create; no -p; legacy mountpoint set at create (zfs_util.go:366).")
L(R1, "zfs.zvol.create", "pkg/zfs/zfs_util.go:182",
  "zfs create [-s] -V size -b blocksize -o props, after zfs list precheck (run from CreateVolume)",
  "yes", "zfs list precheck; any list error treated as absent",
  "CLI not lzc_create(DMU_OST_ZVOL); -s sparse when thin; user props passed as -o pairs.")
L(R1, "zfs.destroy.recursive", "pkg/zfs/zfs_util.go:527",
  "zfs destroy -r pool/vol after parent and volume zfs list checks",
  "yes", "Missing volume returns success; missing parent returns error",
  "CLI -r also destroys child snapshots; EZFS_BUSY surfaces as ZFSError when clones depend on the volume.")
L(R1, "zfs.snapshot.create", "pkg/zfs/zfs_util.go:814",
  "zfs snapshot pool/vol@snap with -o userprops, after zfs list precheck",
  "yes", "Existing snapshot returns success after zfs list",
  "CLI not lzc_snapshot; one snapshot per call, no atomic multi-dataset snapshot.")
L(R1, "zfs.snapshot.destroy", "pkg/zfs/zfs_util.go:839",
  "zfs destroy pool/vol@snap after parent and snapshot zfs list checks",
  "yes", "Missing snapshot returns success; missing parent returns error",
  "CLI not lzc_destroy_snaps; dependent clones cause EZFS_BUSY and the CR keeps its finalizer.")
L(R1, "zfs.clone", "pkg/zfs/zfs_util.go:572",
  "zfs clone -o mountpoint=legacy plus props snap vol; zvol-source snapshot created first",
  "yes", "zfs list precheck on target; existing clone reused",
  "CLI not lzc_clone; xfs and btrfs UUID regenerated after clone; encryption not set, inherited from snapshot.")
L(R1, "zfs.prop.set", "pkg/zfs/zfs_util.go:716",
  "zfs set name=value props on volume via buildVolumeSetArgs; only when volume Ready",
  "yes", "n/a",
  "CLI not libzfs zfs_prop_set; full property set is re-sent on every CR update, not diffed.")
L(R1, "zfs.prop.get", "pkg/zfs/zfs_util.go:697",
  "zfs get -pH -o value prop vol; trailing byte stripped as newline",
  "yes", "Non-zero exit returns ZFSError with stderr",
  "CLI zfs get -pH matches anchor zfs get -Hp; output assumed to end with a newline.")
L(R1, "zfs.userprop.set", "pkg/zfs/zfs_util.go:404",
  "zfs set openebs.io:* name=value on update; -o pairs at create",
  "yes", "n/a",
  "CLI, no libzfs equivalent used; userprops are written but never read back, CR is authoritative.")
L(R1, "zfs.volsize.resize", "pkg/zfs/zfs_util.go:414",
  "zfs set volsize=N pool/vol, run from ResizeZFSVolume on the node",
  "yes", "Reads current reservation first; error returns before any set",
  "CLI not libzfs; thick volumes also set reservation in same call; quota reverted if set fails.")
L(R1, "zfs.quota", "pkg/zfs/zfs_util.go:414",
  "zfs set quota=N, or refquota per quotatype, on dataset from node resize",
  "yes", "n/a",
  "CLI; ZFS_PROP_QUOTA or REFQUOTA picked by quotatype; reservation is set in the same call.")
L(R1, "zfs.reservation", "pkg/zfs/zfs_util.go:1125",
  "zfs set reservation= or refreservation= when thinprovision is no",
  "yes", "n/a",
  "Restore path hardcodes reservation= even for refquota (zfs_util.go:492), diverging from create and resize.")
L(R1, "zfs.send", "pkg/zfs/zfs_util.go:440",
  "zfs send [-i prev] pool/vol@snap piped into nc -w 3 host port via runPipe",
  "partial", "Snapshot created with existence precheck; pipe failures returned as ZFSError",
  "CLI pipe not lzc_send; incremental only when previous snapshot set; no raw or encryption flags; nc has no auth.")
L(R1, "zfs.receive", "pkg/zfs/zfs_util.go:467",
  "nc host port piped into zfs recv -o props -F pool/vol via runPipe",
  "partial", "No precheck; pipe failures returned as ZFSError",
  "CLI not lzc_receive; -F forces rollback of the target; a failed restore cannot resume.")
L(R1, "zfs.pool.list", "pkg/zfs/zfs_util.go:1073",
  "zfs list -d 1 -s name -o name,guid,available,used -H -p; pools picked by missing slash",
  "yes", "n/a",
  "CLI zfs list, not zpool list; pool identified by lack of '/', which excludes poolname child datasets.")
L(R1, "zfs.space.available", "pkg/zfs/zfs_util.go:1073",
  "available and used columns parsed into pool Free and Used quantities",
  "yes", "n/a",
  "Pool-level only; per-dataset availability is not queried; one bad numeric field aborts the whole list.")
L(R1, "zfs.mount", "pkg/zfs/zfs_util.go:640",
  "Sets mountpoint, then zfs mount only when mounted=no; legacy datasets use mount(8) in mount.go:223",
  "yes", "mounted property read; value 'no' triggers zfs mount",
  "CLI zfs mount for mountpoint datasets; legacy datasets bypass zfs mount via mount(8) -t zfs.")
L(R1, "zfs.unmount", "pkg/zfs/mount.go:84",
  "k8s mount utils Unmount on target, then mountpoint=legacy restored",
  "yes", "Not-mounted or missing path returns nil",
  "Not zfs umount: umount(2) via mount utils; failure to restore legacy mountpoint is only logged.")
L(R1, "zfs.job.wait", "pkg/zfs/volume.go:139",
  "Polls ZFSVolume CR state each second until Ready or Failed, 10s timeout",
  "n/a", "CR Get error returned as Internal",
  "Waits on CR status, not a ZFS job; no zfs job or txg handle is tracked.")

# ---------------- zfs-localpv: node.* ----------------
L(R1, "node.device.resolve", "pkg/zfs/zfs_util.go:876",
  "filepath.EvalSymlinks of /dev/zvol/pool/vol; datasets return the dataset name",
  "yes", "EvalSymlinks error returned, no retry",
  "Symlink lookup only, no serial or WWN matching; device polling (getDevice, zfs_util.go:1002) is restore-only.")
L(R1, "node.fs.format", "pkg/zfs/mount.go:61",
  "k8s SafeFormatAndMount mkfs with per-fstype format options; ext4 default",
  "partial", "Filesystem probe happens inside SafeFormatAndMount, not in this code",
  "Only zvol volumes are formatted; dataset volumes are never formatted (formatoptions.go:14).")
L(R1, "node.fs.mount", "pkg/zfs/mount.go:198",
  "format-and-mount /dev/zvol path to target with fstype and mount options",
  "yes", "verifyMountRequest finds existing mount and returns success",
  "FormatAndMount error replaced by generic Internal message, dropping the underlying cause.")
L(R1, "node.fs.unmount", "pkg/zfs/mount.go:84",
  "k8s mount utils Unmount on target path, then legacy mountpoint restored",
  "yes", "Not-mounted or missing path returns nil",
  "Same unmount path as zfs.unmount; a missing ZFSVolume CR is not tolerated here.")
L(R1, "node.fs.resize", "pkg/zfs/resize.go:53",
  "After zfs set, resize2fs for ext or xfs_growfs for xfs on the mounted path",
  "yes", "Mount table scan; path absent from table means no resize",
  "Resize runs only when the path is mounted; resize failure is not reverted, unlike the quota set.")
L(R1, "node.block.publish", "pkg/zfs/mount.go:293",
  "Creates target file, then bind-mounts the /dev/zvol device onto it",
  "no", "Mount failure removes target file; no already-mounted check",
  "Unlike MountZvol there is no already-mounted check; a repeated publish may stack bind mounts.")
L(R1, "node.fs.stats", "pkg/driver/agent.go:408",
  "statfs on mount path after mount-point check; bytes and inodes returned",
  "n/a", "Non-mount path returns NotFound",
  "Reports filesystem usage; ZFS quota and available properties are not consulted.")

# ---------------- zfs-localpv: meta.* ----------------
L(R1, "meta.state.store", "pkg/zfs/volume.go:261",
  "ZFSVolume and ZFSSnapshot CRs in openebs namespace are authoritative; zfs userprops mirror metadata",
  "n/a", "k8s NotFound from GetZFSVolume",
  "CR status Pending, Ready or Failed gates every op; zfs holds data plus openebs.io: userprops only.")
L(R1, "meta.id.scheme", "pkg/driver/controller.go:916",
  "volume_id is lowercased CSI name (pvc-uid); snapshot_id is volID@snapName",
  "n/a", "n/a",
  "Volume name becomes the zfs leaf name, so ids must be lowercase; '@' split assumes no '@' in volume ids.")
L(R1, "meta.lock", "pkg/driver/volume_lock.go:19",
  "In-process per-ID mutex built on sync.Cond; snapshot ops lock volume and snapshot IDs",
  "n/a", "n/a",
  "Not cross-process; multi-replica controllers rely on leader election (helm adds it when replicas > 1).")
L(R1, "meta.idempotency", "pkg/driver/controller.go:226",
  "CR existence and zfs list prechecks per operation; capacity mismatch returns AlreadyExists",
  "partial", "CR NotFound and zfs list absence checked per operation",
  "Errors from zfs list or non-NotFound CR Get fall through to create; no retry guard on the CLI side.")

# ---------------- go-zfs: zfs.* ----------------
L(R2, "zfs.dataset.create", "zfs.go:351",
  "zfs create [-o k=v] name, then GetDataset to return typed Dataset",
  "no", "No precheck; 'dataset already exists' stderr surfaces raw",
  "CLI not lzc_create; no -p; error is *Error with Stderr string only.")
L(R2, "zfs.zvol.create", "zfs.go:257",
  "zfs create -p -V size [-o props] name, then GetDataset",
  "no", "No precheck; raw stderr returned",
  "CLI not lzc_create(DMU_OST_ZVOL); -p silently creates missing parents, unlike zfs-localpv.")
L(R2, "zfs.destroy", "zfs.go:276",
  "zfs destroy with flags from DestroyFlag; DestroyDefault adds no flag",
  "no", "No existence check; caller must match stderr text",
  "CLI not lzc_destroy; no mapping of EZFS_NOENT to success.")
L(R2, "zfs.destroy.recursive", "zfs.go:276",
  "DestroyRecursive adds -r; DestroyRecursiveClones adds -R for dependent clones",
  "no", "Same as zfs.destroy; no not-found mapping",
  "CLI -r and -R; no lzc equivalent used; a single flag word mixes both recursion modes.")
L(R2, "zfs.destroy.defer", "zfs.go:276",
  "DestroyDeferDeletion adds -d; snapshot marked for deferred deletion",
  "no", "Same as zfs.destroy",
  "Anchor is lzc_destroy_snaps(defer=B_TRUE); this uses CLI -d instead.")
L(R2, "zfs.snapshot.create", "zfs.go:368",
  "zfs snapshot [-r] ds@name, then GetDataset of the snapshot",
  "no", "No precheck; duplicate name fails with raw stderr",
  "CLI not lzc_snapshot; -r is CLI-only recursion; one snapshot name per call.")
L(R2, "zfs.snapshot.list", "zfs.go:129",
  "zfs list -rHp -t snapshot [filter] parsed into typed Dataset slice",
  "n/a", "Empty result gives nil slice and no error; missing filter target errors",
  "CLI listing; typed uint64 fields; parse failure aborts the whole list.")
L(R2, "zfs.snapshot.destroy", "zfs.go:276",
  "Generic Destroy called on a snapshot name; no snapshot-specific code path",
  "no", "Same as zfs.destroy",
  "Same function as zfs.destroy; does not check the receiver is a snapshot.")
L(R2, "zfs.clone", "zfs.go:165",
  "zfs clone -p [-o props] snap dest, receiver must be snapshot type, then GetDataset",
  "no", "Non-snapshot receiver rejected locally before CLI call",
  "CLI not lzc_clone; -p creates parents; no check whether the destination exists.")
L(R2, "zfs.rename", "zfs.go:324",
  "zfs rename [-p] [-r] src dst, then GetDataset(name)",
  "no", "No precheck; raw stderr returned",
  "CLI not lzc_rename; on error returns the receiver instead of nil, unlike other methods.")
L(R2, "zfs.prop.get", "zfs.go:314",
  "zfs get -Hp key ds; returns out[0][2] as a plain string",
  "yes", "Missing dataset returns the CLI Error",
  "Returns untyped string, not parsed; indexing out[0] panics if output is empty.")
L(R2, "zfs.prop.set", "zfs.go:304",
  "zfs set key=val ds; also used for user properties",
  "yes", "No precheck; CLI error returned raw",
  "CLI not libzfs zfs_prop_set; no value validation before the call.")
L(R2, "zfs.space.available", "utils.go:134",
  "parseLine maps 'available' column into Dataset.Avail uint64",
  "n/a", "n/a",
  "Typed uint64; '-' parsed as 0 by setUint (utils.go:121); column count must match dsPropList exactly.")
L(R2, "zfs.list.children", "zfs.go:404",
  "zfs list -d N or -r -t all -Hp; lines grouped into Dataset values",
  "n/a", "Empty output panics on datasets[1:] slice",
  "Drops the first entry (the receiver) by slicing; no error when the receiver is missing.")
L(R2, "zfs.send", "zfs.go:232",
  "zfs send name streamed into io.Writer via command.Stdout",
  "n/a", "Non-snapshot receiver rejected locally; CLI errors raw",
  "CLI not lzc_send; full sends only; incremental is separate (zfs.go:244) and adds -i.")
L(R2, "zfs.receive", "zfs.go:222",
  "zfs receive name reading io.Reader on stdin, then GetDataset",
  "no", "No -F; fails when target exists, raw stderr",
  "CLI not lzc_receive; no overwrite or rollback option exposed.")
L(R2, "zfs.mount", "zfs.go:200",
  "zfs mount [-O] [-o opts] ds, then GetDataset",
  "no", "Snapshot receiver rejected locally; CLI errors raw",
  "CLI zfs mount, no lzc or libzfs equivalent; options joined with commas.")
L(R2, "zfs.unmount", "zfs.go:183",
  "zfs umount [-f] ds, then GetDataset",
  "no", "Snapshot receiver rejected locally; CLI errors raw",
  "CLI zfs umount, not unmount(2); force flag is the only option.")
L(R2, "zfs.rollback", "zfs.go:386",
  "zfs rollback [-r] snap; receiver must be snapshot type; returns only error",
  "no", "Non-snapshot receiver rejected locally",
  "Id taken from the anchor list, not the op list; CLI not lzc_rollback_to; -r destroys newer snapshots.")
L(R2, "zfs.pool.list", "zpool.go:99",
  "zpool list -Ho name, then GetZpool per name (one zpool CLI call per pool)",
  "n/a", "Any GetZpool error aborts the whole list",
  "zpool CLI, not zfs; returns typed Zpool with uint64 Size, Free, Allocated fields.")
L(R2, "x.zfs.diff", "zfs.go:438",
  "zfs diff -FH snap ds parsed into InodeChange list",
  "n/a", "Raw stderr from CLI",
  "Not in vocab, so x. prefix; CLI zfs diff with no anchor; typed InodeChange with ChangeType and InodeType.")
L(R2, "x.zfs.dataset.list", "utils.go:324",
  "zfs list -rHp -t type [filter] into typed Dataset slice (Datasets, Filesystems, Volumes)",
  "n/a", "Empty result gives nil slice and no error",
  "Not in vocab, so x. prefix; typed uint64 fields; '-' parsed as zero; shared by Filesystems and Volumes.")
L(R2, "x.zfs.dataset.get", "zfs.go:147",
  "zfs list -Hp -o all-props name, parsed into one typed Dataset",
  "n/a", "Missing dataset returns the CLI Error",
  "Not in vocab, so x. prefix; single-dataset typed read; used by callers to re-check existence.")
L(R2, "x.zpool.create", "zpool.go:77",
  "zpool create [-o props] name args..., returns Zpool with only Name set",
  "no", "No precheck; raw stderr",
  "Not in vocab, so x. prefix; CLI zpool create; returned struct carries no pool state.")
L(R2, "x.zpool.destroy", "zpool.go:93",
  "zpool destroy name",
  "no", "No precheck; raw stderr",
  "Not in vocab, so x. prefix; CLI zpool destroy with no force flag.")

# ---------------- podman-storage-zfs: zfs.* ----------------
L(R3, "zfs.dataset.create", "zfs.go:317",
  "CreateFilesystem with mountpoint=legacy, then mount(2) type zfs and chown of root",
  "no", "Matches *zfs.Error stderr suffix 'dataset already exists' (zfs.go:277)",
  "On EEXIST it runs destroy -R of the stale dataset (zfs.go:286) and retries; destructive recovery.")
L(R3, "zfs.clone", "zfs.go:235",
  "Clone snapshot as new dataset with mountpoint=legacy; source snapshot created per call",
  "no", "No precheck; clone failure triggers snapshot cleanup then returns error",
  "CLI not lzc_clone; snapshot name is Nanosecond-only (zfs.go:228), so repeats are possible across seconds.")
L(R3, "zfs.snapshot.create", "zfs.go:230",
  "Parent.Snapshot(nanosecond name) via zfs snapshot, no -r",
  "no", "Raw stderr; no precheck",
  "Name from time.Now().Nanosecond(), not unique across seconds; a collision makes the clone path fail.")
L(R3, "zfs.destroy.defer", "zfs.go:248",
  "snapshot.Destroy(DestroyDeferDeletion) runs zfs destroy -d",
  "no", "Failure on clone path only logged as warning (zfs.go:243)",
  "Snapshot is removed once its last clone goes; CLI -d, not lzc_destroy_snaps(defer=B_TRUE).")
L(R3, "zfs.destroy.recursive", "zfs.go:395",
  "Remove calls dataset.Destroy(DestroyRecursive), i.e. zfs destroy -r on layer dataset",
  "partial", "Re-queries GetDataset; missing dataset treated as already removed",
  "Tolerates a missing dataset but still errors if it exists; -r also drops snapshots under the layer.")
L(R3, "zfs.quota", "zfs.go:388",
  "setQuota: GetDataset then SetProperty quota=size via zfs set",
  "yes", "GetDataset error returned; size 0 skipped",
  "CLI zfs set quota; only storage option 'size' accepted (parseStorageOpt, zfs.go:366).")
L(R3, "zfs.prop.get", "zfs.go:384",
  "GetDataset runs zfs list -Hp and parses typed fields before quota is set",
  "yes", "Lookup error returned to caller",
  "Typed uint64 read (Quota, Avail); used only as a precheck in this driver.")
L(R3, "zfs.space.available", "zfs.go:91",
  "Filesystems(fsName) at Init caches Dataset with Avail and Used fields",
  "n/a", "Root missing from listing returns error at Init (zfs.go:105)",
  "Values are cached at Init; Status() does not refresh them, so space figures go stale.")
L(R3, "zfs.pool.list", "zfs.go:192",
  "GetZpool on pool name from dataset root, reads Health for Status()",
  "n/a", "Error embedded as text in Status value, not returned",
  "Single pool only via zpool CLI; error text leaks into status output (zfs.go:199).")
L(R3, "zfs.mount", "zfs.go:462",
  "containers/storage mount.Mount(type zfs) on legacy mountpoint; read-only via remount",
  "partial", "Mount error wrapped; in-process RefCounter guards double mount",
  "Uses mount(2), not zfs mount CLI; ref-counted only within this process.")
L(R3, "zfs.unmount", "zfs.go:477",
  "Put: detachUnmount with MNT_DETACH and rmdir of mountpoint at refcount zero",
  "yes", "Unmount failure logged as warning, not returned",
  "Uses unix.Unmount MNT_DETACH, not zfs umount; errors are swallowed.")
L(R3, "x.zfs.dataset.list", "zfs.go:91",
  "Filesystems(fsName) lists root and children at Init; Exists answers from cache",
  "n/a", "Error returned from Init if listing fails",
  "Not in vocab, so x. prefix; Exists() reads in-memory cache, not ZFS, so external destroys are not seen.")

# ---------------- validation ----------------
LIMITS = {"how": 20, "notfound": 12, "notes": 25}
IDEM = {"yes", "no", "partial", "n/a"}
errors = []
for i, r in enumerate(rows):
    for k, lim in LIMITS.items():
        n = len(r[k].split())
        if n > lim:
            errors.append((i, r["op"], k, n))
    if r["idempotent"] not in IDEM:
        errors.append((i, r["op"], "idempotent", r["idempotent"]))
    if not (r["op"].startswith(("csi.", "zfs.", "node.", "meta.", "x.", "share."))):
        errors.append((i, r["op"], "op-prefix", r["op"]))

if errors:
    for e in errors:
        print("LIMIT", e, file=sys.stderr)
    sys.exit(1)

with open(OUT, "w", encoding="utf-8") as f:
    for r in rows:
        f.write(json.dumps(r, ensure_ascii=True) + "\n")

print("wrote", len(rows), "lines to", OUT)
