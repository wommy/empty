# 9. Non-NFS filesystem volumes are opt-in; the shipped deployment is NFS mode

Status: accepted (decisive, reversible) · Date: 2026-10-27 · Related: ADR 0004 (node side), ADR 0006 (block opt-in), ADR 0001 v2 D8 (F6 controls), `deploy/`.

## Context

- **No node side for a bare dataset.** Without `-nfs-server` the endpoint is not in NFS mode, and CreateVolume for mount access returned `{dataset, kind, mountpoint}`: a ZFS filesystem on the storage host, with no export. Nothing can publish it on a node:
  - with no node plugin registered for the driver name, kubelet fails every NodePublish ("driver name … not found in the list of registered CSI drivers");
  - csi-driver-nfs, the node side this repo delegates to (ADR 0004), refuses a volume without `server` and `share`.
  - No consumer of a non-NFS filesystem volume exists in this repo or upstream.
- **The shipped unit ran in that mode.** `zfs-endpoint@.service` passed no `-nfs-server`, and `deploy/k8s` shipped no node plugin. A PVC from the shipped StorageClass bound, and every pod using it hung in ContainerCreating.
- **CSI.** CreateVolume must fail with `INVALID_ARGUMENT` when the plugin cannot satisfy the requested capability, not return a volume no Node service can publish. This is the "PVC binds, pod never starts" class ADR 0006 already refused for block and ADR 0004 Q2 for NFS without clients.

## Decision

- **Mount access outside NFS mode is opt-in.** `zfs-csi -enable-local-filesystem` is off by default. Off, and with no NFS server advertised by the endpoint, CreateVolume refuses mount access with InvalidArgument naming `zfs-endpoint -nfs-server` and the flag, and ValidateVolumeCapabilities never confirms mount access, on any volume (the same shape as ADR 0006's block rule).
- **Expand and Delete are exempt.** ControllerExpandVolume checks only the access type, as before: a flag must not strand a volume that already exists.
- **The shipped deployment is NFS mode,** so it provisions something usable:
  - `deploy/k8s/node.yaml`: csi-driver-nfs's node DaemonSet, pinned by tag and digest, run with `--drivername` set to this driver's name, with no RBAC (upstream binds its node service account to nothing) and no token;
  - `deploy/systemd/zfs-endpoint@.service` ends its ExecStart with `$ZFS_ENDPOINT_NFS`, empty by default; the example drop-in `nfs.conf.example` sets it to `-nfs-server=<address>`;
  - the shipped StorageClass sets `nfs.clients` to a placeholder that does not parse (`<node-subnet>/24`), so every PVC is refused until the operator writes the node network. A plausible CIDR would parse, export to nobody and bring back the hung pod.
- **The node side, hardened against upstream (wave29 node review):**
  - `dnsPolicy: Default`, not `ClusterFirstWithHostNet`: the NFS server name resolves through the node's resolver, so a tenant Service (with `ndots:5`, `nas.lan` is tried as Service `nas` in namespace `lan` first) cannot answer for it. The node pod needs nothing from cluster DNS: it talks to kubelet over the plugin socket and calls no Kubernetes API. Servers are advertised as an IPv4 address or an FQDN with a trailing dot (`nfs.conf.example`).
  - No blanket `operator: Exists` toleration: the privileged node pod runs on untainted nodes only, never on a tainted control plane by default; `node.yaml` carries a commented example toleration.
  - **The ZFS host stays eligible for the node plugin and tenant pods** (decisive, reversible): the deployment is hyperconverged (ADR 0006), so it is a node like the others. An operator who wants it storage-only taints it; the DaemonSet then skips it too. Reversal is a nodeAffinity `node-restriction.kubernetes.io/zfs-endpoint-host DoesNotExist` on the DaemonSet.
  - The shipped StorageClass mounts with `nfsvers=4.2, hard, nosuid, nodev`: no v3 fallback (so no rpcbind or mountd to expose), and no setuid binary or device node on a volume works in a pod. The ZFS host's nfsd must serve 4.2; the real-ZFS VM gates on it (`/proc/fs/nfsd/versions`).
  - The CSIDriver sets `fsGroupPolicy: None`: ownership is the endpoint's, server-side, from `nfs.uid`/`nfs.gid`/`nfs.mode` (ADR 0004), and kubelet's recursive chown would fail under root_squash. A cluster that refuses the change on an existing CSIDriver (the field was immutable in older Kubernetes) deletes and recreates it; no volume is affected.

## Consequences

- An existing non-NFS mount StorageClass stops provisioning when this ships: a fast `ProvisioningFailed` naming both ways out, instead of a hung pod. `-enable-local-filesystem` restores the old behaviour.
- Volumes already provisioned are untouched: they still list, expand, snapshot and delete. They are not confirmed by ValidateVolumeCapabilities with the flag off, as nothing could mount them anyway.
- The shipped manifests provision nothing until the operator sets three things: the drop-in's `-nfs-server`, the class's `nfs.clients`, and the F6 controls (ADR 0001 v2 D8: pod egress to the storage host's 2049, 111 and 20048 denied, Pod Security baseline or stricter on tenant namespaces, a host firewall limiting 2049 to the node addresses, ADR 0004 Consequences). Each refusal before that is a visible, final error, never a hang.
- csi-sanity's mount mode runs with the flag on (it tests the controller, not a node). Its pinned passed specs are unchanged.
- Reversal is a flag: `-enable-local-filesystem` on by default once a node plugin can mount a dataset local to its node.
