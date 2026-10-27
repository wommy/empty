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
  - There is no client list, database or `/etc/exports` file of our own.
  - Whatever clients a volume is exported to is what `sharenfs` says and what the kernel's etab shows. Both are re-read on every call.
  - Static export at create and per-node publish are therefore one mechanism.
    - Static: set the configured clients when the volume is created.
    - Per node: add or remove one host in `sharenfs`.
    - Unpublish-all: remove every host we added.
  - Static export at create comes first, because it needs no attacher sidecar. Per-node publish can follow behind a flag without new state.
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
