# Adversarial prior-art check

## BROKEN

**P3 (gobrick is the only single-LUN remover): BROKEN as stated.**
netapp/trident@ea857409 `utils/iscsi/iscsi.go:2037-2053`: `RemoveLUNFromSessions` removes one LUN (`publishInfo.IscsiLunNumber`) from every portal's session record and does not touch the other LUNs. `core/node/detach.go:522-527` calls it during detach. Trident also has `SafeToLogOut` (`iscsi.go:2073`) to decide whether a session can be logged out, so it is a per-LUN removal with conditional logout. gobrick could not be checked: GitHits returned "Repository not found" for github:Netflix/gobrick, and the web search returned nothing relevant. The "only" claim is therefore unsupported by the evidence I could reach.

**P1 (no ZFS-native typed API daemon): partially BROKEN.**
- github:truenas/middleware@3ffe599a is a ZFS-managing daemon with a typed JSON-RPC API (`src/middlewared/middlewared/api/base/server/ws_handler/rpc.py`). It is not gRPC or REST, so the exact wording survives, but the spirit ("a daemon exposing a typed volume API") does not.
- zfsd (github.com/jpeach/zfsd) is described as a ZFS pool daemon with API plugins. The research call timed out, so its gRPC/REST/CSI status is unverified.
- openebs/zfs-localpv@8759e507 exposes typed CRDs (ZFSVolume, ZFSSnapshot, ZFSRestore) rather than a standalone API. This is the closest thing to a volume API for a CSI front end.

## Holds on evidence checked

**P4 (no ZFS CSI driver implements ControllerModifyVolume): holds for OpenEBS zfs-localpv.** `pkg/driver/controller.go:1367-1371` returns `codes.Unimplemented`. No SnapshotMetadata hit in that repo. I did not survey every ZFS CSI driver, so this is only checked against one.

**P2 (no Go project uses libzfs_core via cgo): holds for the projects checked.** lxc/incus@880a6d02 `cmd/incusd/main_forkzfs.go:94` and canonical/lxd@d1aea588 `lxd/main_forkzfs.go:102` shell out to the `zfs` binary via exec.Command. `driver_zfs.go` uses `subprocess.RunCommand("zfs", ...)`. go-libzfs users were not checked.

## Not verified
- zfsd gRPC/REST details (research timed out).
- gobrick source (repo not indexed).
- SnapshotMetadata across other ZFS CSI drivers.
