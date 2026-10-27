# Privilege and multi-tenancy edge

## EDGES (boundary and what crosses it)

- **Kernel checks `zfs allow` grants.** `zfs_secpolicy_write_perms` (github:openzfs/zfs module/zfs/zfs_ioctl.c:521-550) lets root in the global zone bypass; everyone else needs the grant.
- **Mount is coupled to most write ops.** Kernel policy requires `mount` alongside `create` (zfs_ioctl.c:1242-1284 create_clone), `destroy` (899-922), `rename` (968-1009), `promote` (1019-1064), and `receive` (1066-1091). `snapshot` needs only `snapshot` (1111-1126). The man page says `create` and `destroy` need "the mount ability" (man/man8/zfs-allow.8:194-195).
- **Mount is root-only in userspace on Linux.** `zfs-allow.8:100-109` says `mount`, `unmount`, `mountpoint`, `canmount`, `rename` and `share` "cannot be delegated" on Linux. The code does not enforce that: `mount` sits in the allow table without a Linux filter (cmd/zfs/zfs_main.c:5373; module/zcommon/zfs_deleg.c:43). `zfsprops.7` (zoned_uid example, ~line 2250) even grants `mount`. But libzfs refuses non-root mounts (lib/libzfs/os/linux/libzfs_mount_os.c:556-559), and `zfs create` prints "may only be mounted by root" (cmd/zfs/zfs_main.c:791-795). Practical rule: create with `canmount=noauto` or `mountpoint=none`. Doc-vs-code conflict is unverified on a real host.
- **sharenfs.** The kernel `share` ioctl returns ENOTSUP (zfs_ioctl.c:863-867). The Linux export shells out to `/usr/sbin/exportfs -ra` as the calling process (lib/libzfs/os/linux/libzfs_share_nfs.c:514-527). Expected to need root; unverified. Setting the `sharenfs` property is delegable.
- **zvols.** `volsize` goes through `zfs_secpolicy_setprop` (zfs_ioctl.c:666-734), so it can be delegated. Creating a non-sparse zvol also needs `refreservation` (zfs-allow.8:194). The LIO/nvmet backstore resize and initiator rescan sit outside ZFS and are unverified.
- **LIO/nvmet configfs.** Not checked against kernel source. Assume root (or a root helper) until verified.

## SEAMS (privilege split, with existing implementations)

- **Incus** (github:lxc/incus, license not checked): root `incusd`. `forkzfs` runs ZFS in a cleaned mount namespace and refuses non-root (cmd/incusd/main_forkzfs.go:38-60). `forkmount` does the same for mounts (main_forkmount.go:553-566). Containers get datasets via `zfs.delegate` (doc/reference/storage_zfs.md:51-56, ZFS 2.2+).
- **zrepl** (github:zrepl/zrepl, license not checked): runs unprivileged via ZFS delegation (docs/installation/user-privileges.rst:6-7). It is a ZFS-only worker and has no export or configfs step.
- **democratic-csi** (MIT, local LICENSE): wraps zfs in sudo when not root (src/driver/controller-zfs/index.js:506-507; src/utils/zfs.js:1150-1165). That is command-level sudo, not delegation.
- **targetd** (github:open-iscsi/targetd, license not checked): everything runs as root, LVM and LIO together, on one pool `vg-targetd/thin_pool` (README:54-64).
- **systemd** (github:systemd/systemd, license not checked): `CapabilityBoundingSet=` and `AmbientCapabilities=` (man/systemd.exec.xml:902-912). `systemd-networkd.service.in:23-25` is an example that grants `CAP_SYS_ADMIN`. Proposed split: a root helper unit with only the caps it needs (mount, exportfs, configfs), and an unprivileged ZFS worker holding `zfs allow` grants.

## Multi-tenancy

- **zoned_uid** (zfsprops.7:2162-2250, Linux-only, in the HEAD doc dated June 2026, so verify against a release): a per-uid delegation root with three layers. The layers are uid match, dsl_deleg grants, and a capability tier (CAP_FOWNER for create/snapshot/set; CAP_SYS_ADMIN for destroy/rename/clone). The root cannot be destroyed from inside, and `filesystem_limit`/`snapshot_limit` on the root act as ceilings (code: zfs_ioctl.c:289-335, 554-560). It targets user namespaces (rootless containers), not service tenants.
- **Quotas on the parent** are root-set; children cannot exceed them (standard ZFS semantics, not re-verified here).
- **Drivers:** no surveyed driver uses a per-tenant parent dataset with delegated auth.
  - zfs-localpv (Apache-2.0, local LICENSE): one `poolname` per StorageClass (deploy/sample/fio.yaml:15).
  - democratic-csi: one `datasetParentName` per driver instance (controller-zfs/index.js:175-178).
  - LXD: `storage.project.{name}.images_volume` gives a per-project volume for images and backups, not a per-tenant parent (canonical/lxd doc/api-extensions.md:3133-3137).

## GAPS

- No mature driver does per-tenant parent datasets with delegated auth on Linux. `zoned_uid` is the only in-tree primitive, and it targets user namespaces.
- The mount-delegation conflict needs a test on a real host: a user with `create`, `destroy`, `snapshot`, `mount` grants and `canmount=noauto`. Also check whether `create`/`destroy` pass without `mount`.
- No reference root helper for exports and configfs with a narrow capability set was found. The endpoint should build one: a small root process that accepts only typed ops for datasets under the tenant prefix.
- Per-front-end authorization scoped to a dataset prefix: none of democratic-csi, zfs-localpv or targetd enforce it. The endpoint can check name prefixes before any Runner call.
