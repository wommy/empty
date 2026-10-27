# 4. OpenZFS 2.4 only; exports live in `sharenfs`; the node side is csi-driver-nfs

Status: accepted · Date: 2026-10-27 · Related: ADR 0003 (OpenZFS is the source of truth).

## Context

- **ZFS versions.** CI tested on two systems:
  - an Ubuntu 26.10 VM running OpenZFS 2.4.x, the TrueNAS 27 target;
  - the Ubuntu 24.04 runner, which pairs 2.2.2 userland with a 2.3.4 kernel module, so it is no real version at all.
- **Costs of supporting 2.2:**
  - It has no `zfs get -j`, so property values are parsed from tab-separated text. A newline in a user property value can forge lines (`docs/research`, scratchpad `adv4/zfs-json.md`).
  - Values read back differently (`xattr=dir` reads back as `on`).
  - It lacks `snapdir=disabled`.
- **The CSI node side.** The CSI front end has no node plugin. `kubernetes-csi/csi-driver-nfs` runs its node plugin under any driver name (`--drivername`). It mounts the `server` and `share` it finds in `volume_context`.
- **The NFS question.** The question was: export statically at create, or per node at ControllerPublish? The owner answered "OpenZFS as source of truth".

## Decision

- **OpenZFS 2.4 is the only supported version.**
  - Parsing may use `zfs get -j -p` (JSON, present since 2.3.0) and must parse it strictly.
  - Behaviour, error strings and value spellings are those of 2.4.x.
  - CI's real-ZFS gate is the 2.4 VM, which asserts that both userland and kmod are `zfs-2.4.*`. The 24.04 host job is dropped.
- **Export state lives in OpenZFS, in the dataset's `sharenfs` property, and nowhere else.**
  - There is no client list, database or `/etc/exports` file of our own. The `<prefix>:nfs_clients` user property shipped with file export is exactly such a list: `sharenfs` is computed from it. It is removed in a follow-up, and reconcile reports a leftover one.
  - Whatever clients a volume is exported to is what `sharenfs` says and what the kernel's etab shows. Both are re-read on every call.
  - **Ownership of the value, not of entries.**
    - A local `sharenfs` that parses strictly as our grammar is ours to rewrite as a whole, whoever typed it. The grammar is `rw=`/`ro=` lists of masked IPv4 CIDRs, each written with an explicit `/N`, at or above the configured floor (default /16), with no overlaps.
    - Any other local value is someone else's: served untouched, reported, never written.
    - An inherited value is the parent's.
    - Writing every client as `/N` makes nfs-utils treat it as a subnet: no DNS lookup, no merging with hostname clients.
  - **Static export and per-node publish are one mechanism.**
    - Static: CreateVolume sets the StorageClass's CIDR list with `zfs create -o sharenfs=...`, then verifies nfsd and etab before committing.
    - Per node (later, behind a flag): add or remove one `<node>/32` in the value.
    - Static export at create comes first, because it needs no attacher sidecar.
  - **Root squash stays on.** The endpoint sets the new dataset root's owner and mode from StorageClass parameters before committing the volume.
  - The full design and its attack are in `docs/research/gaps/csi-nfs-design.md`.
- **The node side is delegated to csi-driver-nfs**, run with our driver name. CreateVolume returns `server` and `share` in `volume_context`, and this repo writes no node plugin for NFS.
- **License: Apache-2.0.**

## Consequences

- The 2.2-only work is dropped:
  - the hardened text parser for 2.2;
  - 2.2 read-back compatibility;
  - a 2.2 CI matrix leg.
- The forged-line hole closes by switching the scan to JSON, rather than cross-checking names.
- `snapdir=disabled` is a valid property value.
- Stock Ubuntu 24.04 and Debian 12 hosts are unsupported until they ship 2.4.
- A `node_id` used as an export client must be validated as a single host, never a CIDR.
- Block (zvol) volumes still need a node story; csi-driver-nfs covers only NFS.

## Implementation notes (2026-10-27, branch `nfs-csi`)

These notes record how the decision was built. They do not change it.

- **`nfs_clients` is gone from the export path.**
  - sharenfs alone is read and written.
  - Reconcile reports a leftover property in `skipped`. It is never cleared, so it stays on disk as evidence: `zfs inherit` clears it.
  - Every list the property could hold round-trips through sharenfs (fuzzed), so no state is lost.
  - Roll back only as far as the bridge commit "a canonical local sharenfs is ours without nfs_clients". An older build computes sharenfs from a stale list and revokes clients.
- **The floor is not in the ownership grammar**, a deviation from "at or above the configured floor" above.
  - The floor bounds new writes: Publish and CreateVolume.
  - Ownership accepts any `/N` from 1 to 32. Otherwise, raising `-nfs-min-prefix` would turn existing exports foreign, and they could never be rewritten or unpublished.
  - Reconcile is the place to report below-floor values.
- **A foreign value is never written, even when it still serves the client.**
  - An admin's `rw=*` over our list is not rewritten.
  - Unpublish is FAILED_PRECONDITION while a foreign value still serves the host.
  - A refused Publish changes nothing.
- **The NFS server address is addressing metadata, not export state.**
  - CSI requires it to be fixed per volume, and OpenZFS has no place for it. So `<prefix>:nfs_server` is recorded once, in the same `zfs create`/`clone -o` as sharenfs, and read local-only (wave13 Q1).
  - Who may mount is still sharenfs alone.
- **NFS mode** is on iff the endpoint advertises `-nfs-server`. In that mode every CSI CreateVolume needs `nfs.clients` (wave13 Q2).
- **Root ownership ("Root squash stays on"; wave14, branch `nfs-owner`).**
  - `NFSExport` gains `optional uint32 uid = 4, gid = 5, mode = 6`, from StorageClass `nfs.uid`, `nfs.gid` and `nfs.mode` (decimal ids up to 2^31-1, octal mode up to 7777; InvalidArgument otherwise, in zfs-csi and in the endpoint).
  - The endpoint applies them to the mounted root itself (`set-root`, `LocalRoot`): openat2 with `RESOLVE_NO_SYMLINKS`, the open directory checked against the mount table (zfs, this dataset, root `/`, same device), then fchown and fchmod on that descriptor.
  - It runs in CreateVolume's resume, on every pass, before the NFS verification and the commit. Nothing records that it ran: it is idempotent, so a crash on either side of it converges by running it again.
  - **Defaults: unset leaves an attribute as the create made it**, root:root 0755 for a new volume and the source's for a clone or restore. The endpoint never widens a root on its own; the StorageClass author chooses.
  - They are file metadata, not volume state: never reported, and a repeat of a committed create is not compared on them (a tenant who owns the root may chmod it).
- **The mountpoint pin (wave14).**
  - Every new filesystem volume is created with `mountpoint=<parent's mountpoint>/<id>` set locally, in the same `zfs create`/`clone -o` as F12's mount options. `share` and `mountpoint` in volume_context then stay put if the parent's mountpoint moves.
  - A parent with mountpoint `none` or `legacy` pins nothing.
  - **Volumes made before the pin are reported, not rewritten.** Reconcile lists a committed filesystem with an inherited path mountpoint in `skipped`. `zfs set mountpoint` unmounts and remounts the volume, and its export, under whoever uses it, so the operator picks the moment.
- **Not built yet:**
  - the reconcile drift reports other than the leftover list and the server.
