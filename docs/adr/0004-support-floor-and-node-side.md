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
  - Reconcile reports a below-floor network in a value of ours in `skipped` (`SkipNFSBelowFloor`, naming each network and mode) and never rewrites it (wave14).
- **A foreign value is never written, even when it still serves the client.**
  - An admin's `rw=*` over our list is not rewritten.
  - Unpublish is FAILED_PRECONDITION while a foreign value still serves the host.
  - A refused Publish changes nothing.
- **The NFS server address** is recorded per volume in `<prefix>:nfs_server`, and **NFS mode** refuses a CreateVolume without `nfs.clients`: see the amendment below.
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
  - the reconcile drift reports other than the leftover list, the server, the mountpoint pin and below-floor.

## Client grammar (2026-10-27, wave9 nfs-diff §5)

**Decision.** A client the endpoint stores is always a masked IPv4 network written with an explicit `/N`, `/32` included, in `sharenfs` and wherever it is compared. A bare IPv4 address is accepted only as request input, and only where the API takes one client: it becomes its `/32` before anything is written.

- **Stored form.** `ParseShareNFS` accepts a value as the endpoint's only if every client contains `/` and is spelled as `ParseClient` spells it, and `ShareNFS` of its clients reproduces the value exactly (`internal/endpoint/export.go`). A local `sharenfs` with a bare address is therefore foreign: served, reported, never written.
- **Input.**
  - `PublishFileExport` and `UnpublishFileExport` take an IPv4 address or an IPv4 CIDR. `ParseClient` turns `10.0.0.5` into `10.0.0.5/32` (`TestParseClient`, `internal/endpoint/export_test.go`).
  - CreateVolume's `nfs_export.clients`, and so StorageClass `nfs.clients`, require the `/N`. A bare address is InvalidArgument, naming the `/32` to write (`validateNFS` in `internal/endpoint/plan.go`; `TestValidateNFSExport`, `internal/endpoint/nfs_create_test.go`). These clients are never masked or completed.
- **Why.** nfs-utils treats a client with `/` as a subnet: kept verbatim in etab, never resolved, merged only with the identical string. It treats a bare address as a host name: resolved through getaddrinfo, merged with any name client for the same address, written to etab under that client's name, and ranked before every subnet line by mountd. So only the `/N` form can be verified against etab by exact comparison, and no foreign host-name line can absorb or outrank it unseen. The model (`internal/nfsref`) and the real exportfs agree: `localhost(ro) 127.0.0.1(rw)` merges into one line, `localhost(ro) 127.0.0.1/32(rw)` gives two (`TestClientGrammarBareVsCIDR`, `internal/nfsref/grammar_test.go`).
- **Reversible.** Taking a bare address on Publish is input leniency only; it changes no kernel state. Refusing it later would break callers, not exports.

## Amendment (2026-10-27): the NFS server address and NFS mode (wave13 Q1, Q2)

These two decisions extend the Decision above. Both are reversible. They supersede `docs/research/gaps/csi-nfs-design.md` §6 on `nfs.server`, which called the server "not volume state".

### Q1. `<prefix>:nfs_server` is recorded per volume, local-only

- **Decision.** The NFS server address a volume was created with is a user property, `<prefix>:nfs_server`: one address, no list. It is set only in the same `zfs create`/`zfs clone -o` as `sharenfs` and the managed marker, so it adds no crash point. It is read only when its source is `local`.
- **It is addressing metadata, not export state.** `sharenfs` says who may mount; nfsd answers on every interface, so OpenZFS has no place for the address a client mounts from. "Export state lives only in `sharenfs`" still holds.
- **Why it must be recorded.** CSI requires a volume's `volume_context` to be immutable and the same on every report (spec v1.13.0), and Kubernetes copies it once into the PV's `csi.volumeAttributes`, which cannot change. Taking "the first advertised address" instead would give an identical retry a different server after `-nfs-server` is reordered.
- **Behaviour as built:**
  - `volume_context.server` is the recorded value; a volume created without an export reports no `server` (`volumeView`, `internal/endpoint/endpoint.go`).
  - Uncommitted: a retry converges the property to the request, like `sharenfs`. Committed: a different explicit `nfs.server` is ALREADY_EXISTS naming the recorded one; an omitted one returns the recorded one, even after `-nfs-server` was reordered (`internal/endpoint/plan.go`; `TestNFS_N9_Idempotency`, `TestNFS_N9_UncommittedConverges`, `internal/csi/nfs_test.go`; `TestFileExportCreateServerSurvivesRestart`, `internal/endpoint/export_create_contract_test.go`).
  - A clone records the request's server, never its source's (`TestNFS_N13_CloneTakesRequestExport`).
  - An inherited or received value is ignored and reported by Reconcile (`SkipNFSServerNotLocal`); a recorded server no longer advertised is still returned, because PVs hold it, and is reported (`SkipNFSServerUnadvertised`) (`internal/endpoint/reconcile.go`; `TestNFS_N16_InheritedServerIgnored`, `TestFileExportCreateServerElsewhere`).
- **Operator rule.** Advertise stable names (a VIP or a DNS name). An address already in PVs never changes for them; removing it from `-nfs-server` does not move them.

### Q2. NFS mode refuses a volume without `nfs.clients`

- **Decision.** zfs-csi is in NFS mode iff the endpoint advertises at least one `-nfs-server` (`GetCapabilitiesResponse.nfs_servers`). In that mode every CreateVolume without the StorageClass parameter `nfs.clients`, clones and restores included, is InvalidArgument: "nfs.clients is required: this driver serves volumes over NFS" (`parseParams`, `internal/csi/controller.go`; `TestNFS_N15_ClientsRequired`, `internal/csi/nfs_test.go`).
- **Why.** csi-driver-nfs, the node side, refuses a NodePublish without `server` and `share`, and a volume exported to nobody fails at mount. Either way the PVC binds and the pod hangs. Refusing at provisioning fails fast, as a ProvisioningFailed event naming the parameter.
- **Reversible in the safe direction.** Allowing such volumes later is additive. Per-node publish, when built, will invert the rule behind its own flag (`csi-nfs-design.md` §12).

