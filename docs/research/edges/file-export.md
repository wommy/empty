# File export edge (NFS and SMB)

## EDGES

- Crossing: the ZFS endpoint daemon on the ZFS host turns a typed "export dataset X over protocol P with options O to clients C" request into host state. The CSI front end sees only the resulting share handle and server address.
- Inbound: dataset name, protocol (nfs | smb), client spec, options, and a stable share name.
- Outbound: the dataset's share property (or a generated export file), plus a `mountpoint` path to return in volume context. democratic-csi returns `share: mountpoint` for NFS and `share: generateSmbShareName(dataset)` for SMB (`github:democratic-csi/democratic-csi@69c7ebcd src/driver/controller-zfs-generic/index.js:145-149, 180-184`).
- Host state the edge writes: `/etc/exports.d/zfs.exports` (OpenZFS NFS path, `github:openzfs/zfs@a128b8a0 lib/libzfs/os/linux/libzfs_share_nfs.c:33-35`), dataset `sharenfs`/`sharesmb` properties, `net usershare` entries, or ganesha export blocks.

## SEAMS

1. Share-state seam (protocol-neutral interface): `Export(dataset, proto, spec) / Unexport(dataset, proto)`.
   - Best existing implementation: the ZFS property path. democratic-csi sets `sharenfs` via `zb.zfs.set` and removes it via `zb.zfs.inherit`, tolerating "dataset does not exist" (`index.js:122-133, 634-648`).
2. NFS backend seam.
   - Property backend: `zfs set sharenfs=...`. libzfs writes one line per host: `mp host(sec=...,rw|ro,...,opts)` (`libzfs_share_nfs.c:425-427`). A `sharenfs=on` value means `rw,crossmnt` (`:61-62`).
   - Ganesha backend: godbus calls `org.ganesha.nfsd.exportmgr.AddExport` and `RemoveExport` on `/org/ganesha/nfsd/ExportMgr`, keyed by a uint16 export ID (`github:kubernetes-sigs/nfs-ganesha-server-and-external-provisioner@84d9acf3 pkg/volume/export.go:129-151`). Go dependency: `github.com/godbus/dbus` (`go.mod:6`).
3. SMB backend seam.
   - `sharesmb=on` runs `net -S <host> usershare add <name> <path> "Comment: <path>" "Everyone:F"`, with the name sanitised (`github:openzfs/zfs@a128b8a0 lib/libzfs/os/linux/libzfs_share_smb.c:217-259`).
   - Samba registry or `net conf`: not found in OpenZFS.
   - TrueNAS: generates the whole `smb.conf` from its database (`github:truenas/middleware@3ffe599a src/middlewared/middlewared/plugins/smb_/util_smbconf.py:724-733`). Its Python plugins are the reference for a full-config-render approach.
4. Boot seam: `zfs-share.service` runs `zfs share -a` (`github:openzfs/zfs@a128b8a0 etc/systemd/system/zfs-share.service.in:17`), enabled by `50-zfs.preset`.

## GAPS

- No Go library was found for `exportfs`, `/etc/exports` editing, `net usershare`, or the Samba registry. The only Go code found is the ganesha DBus client above, and it is inside a provisioner, not a reusable package.
- Concurrency: the OpenZFS path takes a lock file (`nfs_exports_lock`, `libzfs_share_nfs.c:31-56`). `nfs_commit_shares` then runs `exportfs -ra` (`:520-526`). I did not verify that the reload runs under the lock, so the reload race is unconfirmed.
- Ganesha's `removeFromFile` guards only with an in-process `sync.Mutex` (`pkg/volume/util.go:107-108`). That does not protect against another writer.
- `nfs_truncate_shares` empties the whole exports file (`libzfs_share_nfs.c:529-535`). Any non-ZFS entries in that file are at risk.
- Idempotency: property set and inherit are idempotent. Ganesha `AddExport` repeated with the same block was not verified in the sources read.
- Not verified: how democratic-csi handles per-client options (only the `setDatasetProperties` path was read), and whether `usershare` entries survive reboot without `zfs share -a`.

## Verdict

The `sharenfs` / `sharesmb` dataset properties give idempotent create and delete (`set`, `inherit`) and survive reboot because `zfs-share.service` re-applies them. Ganesha is the better choice only when NFSv4 features or a daemon-managed export table are needed.
