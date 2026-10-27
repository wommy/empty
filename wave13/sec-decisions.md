# wave13: adversarial pass on four security decisions (D1, D8, D4, D12)

Read-only. Repo branch `claude/gallant-johnson-1qaxp5`, HEAD `a02c5b0`. Inputs: `wave11/threat-model.md` §7-§9, `wave11/adr-0001-v2.md`, ADRs 0001-0005, `docs/research/gaps/csi-nfs-design.md` (cited "design §N"), `wave13/nfs-decisions.md`.

**Bottom line.** None of the four needs the owner. Three of the architect's ASK-OWNER labels fall once their provenance is checked. The fourth, D8, falls once the operational attack is run: the evidence supports **keeping** ADR 0004's static-first ordering, which reverses nothing and so asks nothing. The architect's per-node default is weaker than it looks and costs more than it says.

## Sources (pinned)

| id | source | commit | used for |
|---|---|---|---|
| EA | kubernetes-csi/external-attacher | `336d691f` (clone in `wave13/sec-dl/ea`) | trivial handler, node-id resolution on detach, ReconcileVA |
| K | kubernetes/kubernetes | `dd410c48` | CSIDriver.attachRequired doc and immutability, ADC force detach |
| NFS | kubernetes-csi/csi-driver-nfs | `30ac2678` | nodeid, hostNetwork, attachRequired, caps |
| DC | democratic-csi | `69c7ebcd` | practice |
| TN | truenas/truenas-csi | `6443642b` | practice |
| TR | NetApp/trident | `ea857409` | practice: per-volume export policies |
| LP | openebs/zfs-localpv | `8759e507` | practice |
| Z | openzfs/zfs | tag zfs-2.4.4 = `71a9f957` | snapdir, zfs_admin_snapshot |

## 0. Provenance anomalies (weighed first)

| # | Anomaly | Evidence | Consequence |
|---|---|---|---|
| A1 | The only source for "several clusters share one parent" is ADR 0001 v1 itself (`docs/adr/0001-transport-auth.md` L10), written by Claude in `2ed1800`. The status is *proposed*, and the ADR was never built. No owner statement exists in the repo or the scratchpad. | `git log` on the ADR; grep of the scratchpad | D1 retires an agent's assumption, not an owner requirement. |
| A2 | The two-person rule first appears in wave 11's own threat model and ADR v2 draft. The wave 11 prompt (`wave11/prompts.md` L4) asks only "who may Reconcile/destroy". | grep | D4 asks the owner whether to defer something nobody asked for. |
| A3 | The owner **was** asked exactly D8's question ("export statically at create, or per node at ControllerPublish?") and answered with a principle, "OpenZFS as source of truth". ADR 0004 (status *accepted*) then fixed "static export at create comes first". | `docs/adr/0004` L15, Decision bullet "Static export at create comes first" | Reversing that would re-ask an answered question. Keeping it needs no ask. |
| A4 | The endpoint never sets `snapdir` at create. It is only an allowlisted StorageClass property (`internal/endpoint/props.go` L118). Volumes therefore **inherit** it from the parent. | code | D12's "keep hidden" is mis-framed. The status quo is "inherit", which already lets the operator set `disabled` per tenant parent. |
| A5 | zfs-csi needs a hostPath to the socket and TCP stays refused (D5). So a tenant's controller runs on the storage host, either as a node of that cluster or as host containers with a kubeconfig. Multi-cluster sharing of **one** parent was therefore never a cheap deployment. | ADR v2 Context item 4 | This makes D1 smaller still. |

---

## D1. Parent dataset = tenancy unit

**Steelman v1 (`volume_prefix` scoping):**
- one instance and one set of datasets for many clusters;
- one quota pool;
- fewer systemd units and sockets;
- the only design that lets two COs share one parent.

**Steelman a third option:** a "namespace" child dataset per tenant under one daemon (`tank/k8s/<tenant>/<vol>`), with depth-2 scans. It needs no new units, and isolation is still a subtree.

**Attack on the recommendation (parent = tenant):**
- **Operational cost.** One unit, socket directory and quota per tenant. That is real but linear and boring: systemd templates already exist (`zfs-endpoint@.service`). The cost the architect left out is the cost of one cluster using two parents (two pools or two tiers). That needs two endpoints, two zfs-csi controllers and two csi-driver-nfs node DaemonSets under two driver names. The cause is that zfs-csi dials exactly one endpoint, and csi-driver-nfs registers one `--drivername` per DaemonSet. v1 has **the same** cost, so it does not discriminate between the options.
- **Failure modes.**
  - Under v1, a planner entry point that forgets the scope fails **open** (v1's own Known risk).
  - Under v1, Reconcile's complete desired set would make cluster A's GC collect cluster B's volumes. v1 contradicts the built Reconcile.
  - Under D1, a mis-mounted hostPath gives one pod another tenant's socket. Directory permissions and the kubelet control that, and F1 fixes the shared directory.
- **Third option attacked:**
  - depth-2 scans and two-level ids change id semantics;
  - the subtree is a code-enforced boundary again, so it fails open like v1;
  - Reconcile's desired set would need a tenant key.

  It is the same split enforcement with extra steps.
- **Migration.** None. D1 *is* what HEAD does: one daemon per parent, flock against nesting, depth-1 scans, relative ids. Adding prefix scoping later is purely additive, because no id encodes tenancy.
- **Practice.**
  - democratic-csi: one `datasetParentName` per driver instance (`DC controller-zfs/index.js`).
  - truenas-csi: one pool or parent per StorageClass, under one driver deployment.
  - zfs-localpv: one pool per StorageClass, local only.

  None of them scopes callers inside one parent. All isolate by dataset subtree plus deployment.

**Judgement.** The evidence settles it. The built system already *is* D1. The rejected alternative contradicts built Reconcile, and its requirement has no owner provenance (A1). Under A5 the multi-cluster case is barely deployable anyway. It is reversible: scoping is additive, and nothing outward changes for a pre-release with no users.

**Verdict: DECISIVE+REVERSIBLE.** Record it in ADR 0001 v2. Write down A1 explicitly ("the shared-parent requirement came from v1's own context, not the owner") so the record shows why the owner was not asked. It becomes ASK-OWNER only if the coordinator knows of an owner statement that two clusters must share one parent.

---

## D8. NFS default: per-node `/32` (ControllerPublish) vs static StorageClass CIDR

**Facts verified in source:**

| # | Fact | Evidence |
|---|---|---|
| E1 | `attachRequired` means "the Kubernetes attach detach controller should call the attach volume interface … and wait". **"This field is immutable."** Update validation enforces it. Flipping modes means deleting and recreating the CSIDriver object. | K `staging/src/k8s.io/api/storage/v1/types.go` L306-319; `pkg/apis/storage/validation/validation.go` L456 |
| E2 | external-attacher uses a **trivial handler**, which marks VAs attached without calling the driver, when the driver lacks PUBLISH_UNPUBLISH_VOLUME. Per-node mode therefore needs our CSIDriver at `attachRequired: true`, the sidecar, and PUBLISH_UNPUBLISH in zfs-csi. | EA `cmd/csi-attacher/main.go` L228-260 |
| E3 | Our CSIDriver object is ours: csi-driver-nfs only ships one for its own name, with `attachRequired: false`, and advertises no PUBLISH_UNPUBLISH. Its node plugin never reads `publish_context`; NodeGetInfo returns the `--nodeid` flag. | NFS `deploy/csi-nfs-driverinfo.yaml` L7; `pkg/nfs/nfs.go` L120-125; `pkg/nfs/nodeserver.go` L262 (no PublishContext read anywhere) |
| E4 | **csi-driver-nfs's shipped `--nodeid` is `spec.nodeName`, a hostname.** ADR 0004 requires `node_id` to be a single IP and design §9.5 says it must be the **source address toward the server**. Every per-node install must therefore re-template the third-party node DaemonSet, for example with `status.hostIP`, or with a computed storage-NIC address on multi-NIC nodes. | NFS `deploy/csi-nfs-node.yaml` L103-111 |
| E5 | The node plugin is `hostNetwork: true`. Mounts leave from the node's address, not the pod network. | NFS `deploy/csi-nfs-node.yaml` L20 |
| E6 | **On detach, external-attacher resolves node_id from the *current* CSINode first** and uses the VA's `csi.alpha.kubernetes.io/node-id` annotation only when CSINode or its driver entry is missing. Suppose a node's address changes, or a node is replaced under the same name: kubelet re-registers with the new IP, and ControllerUnpublish removes the **new** `/32`. The old `/32` is never removed. | EA `pkg/controller/csi_handler.go` L580, L745-775; `util.go` L124 |
| E7 | external-attacher's `ReconcileVA` iterates **VolumeAttachments only**. A published node with no VA (stale) is never unpublished. Kubernetes has no GC for it. | EA `pkg/controller/csi_handler.go` L126-199 |
| E8 | ADC force-detaches from an **unhealthy** node only after `maxWaitForUnmountDuration` (6 min), unless `disableForceDetachOnTimeout` is set. Until then, RWO failover blocks on Multi-Attach. | K `pkg/controller/volume/attachdetach/reconciler/reconciler.go` L214-230 |
| E9 | Practice, ZFS drivers: **static ACL, no per-node export.** democratic-csi has PUBLISH_UNPUBLISH commented out and writes a configured `sharenfs` or allowed networks at create. truenas-csi runs `attachRequired: true`, but its ControllerPublish only returns connection info; the NFS ACL is the StorageClass's `nfs.hosts`/`nfs.networks` at create. zfs-localpv runs `attachRequired: false`. | DC `controller-zfs/index.js` L91-94, `controller-zfs-generic/index.js` L119-149, `freenas/api.js` L297, L313; TN `deploy/truenas-csi-driver.yaml` L134, `pkg/driver/controller.go` L1622-1745, `pkg/driver/nfs.go` L30-31; LP `deploy/zfs-operator.yaml` L2963 |
| E10 | Practice, Trident: per-node export **exists, is opt-in** (`autoExportPolicy bool`, zero value false; only in CSI context), and is **stateful**. ControllerPublish maps node *name* to Trident's own node record holding **all** the node's IPs, filtered by `autoExportCIDRs` (default `0.0.0.0/0, ::/0`). Unpublish rebuilds the rules to "keep only active nodes" from its publication records. That record is exactly what handles E4, E6 and E7, and it is what our stateless design (§12 "no new state") lacks. | TR `storage_drivers/types.go` L138-139; `ontap_common.go` L2470-2474, L2541-2542, L504, L609, L692, L6103-6125; `frontend/csi/controller_server.go` L345-373 |

**Steelman per-node (the architect):**
- it narrows F6 from "every node in the CIDR" to "nodes with the volume attached";
- it gives **real fencing**: ControllerUnpublish after a force-detach (E8) revokes a partitioned node's access, which no static CIDR can do;
- it makes SINGLE_NODE_* enforceable (design §12);
- the endpoint RPCs already exist.

**Steelman static:**
- no attacher sidecar, no VolumeAttachment objects, and no re-templating of csi-driver-nfs (E4);
- works through NAT and with egress IPs;
- no stale-export class of bugs (E6, E7);
- matches every comparable ZFS driver (E9);
- already accepted and designed in detail (ADR 0004, design §2-§11).

**Attack on per-node as the default:**
- **Operational cost.**
  - Our CSIDriver must be `attachRequired: true`, and it is immutable (E1).
  - It adds an external-attacher Deployment plus RBAC.
  - Every pod start adds a VA round-trip.
  - The **third-party** csi-driver-nfs node DaemonSet must be re-templated so that `--nodeid` is an IP (E4). On multi-NIC nodes, `status.hostIP` is the wrong address and an init step must compute `ip route get <server>`.
  - ~430 nodes per volume (design §9.3 F7).
- **Node IP churn and replacement (E6).** When the address changes, the old `/32` stays exported forever. If the cloud or DHCP reuses the old IP for another machine, **that machine gets the volume**. In this one respect per-node is *worse* than a static CIDR covering the same subnet: the leak goes to an arbitrary future holder of the IP and nobody can see it. The only fix is a Kubernetes-aware GC that compares `/32` entries against VolumeAttachments (Trident's model, E10). zfs-csi has no Kubernetes client, and the design forbids stored state.
- **Missed ControllerUnpublish after a node dies.**
  - E8 bounds the normal case at 6 min.
  - A VA whose finalizer an admin strips, or a cluster torn down while volumes are published, leaves a permanent `/32` (E7).
  - Static mode has no such state to leak.
- **NAT, egress IPs.** When nodes reach storage through NAT, every node shares one source address. Per-node then either breaks (node_id ≠ source, so mounts are denied) or degenerates into one `/32` that covers every node. Static mode just works. Egress-gateway CNIs do not affect mounts (E5: hostNetwork), but they do change what F6's attacker pod looks like.
- **hostNetwork pods and AUTH_SYS.** Neither mode stops a hostNetwork pod or root on a covered node. Per-node narrows the *set of nodes* but not the *attacker*: anyone who can create pods can pin one (`nodeName`) to a node where the target volume is attached. So F6 is narrowed, not closed.
- **The cheaper control the architect lists only as docs closes F6 better in both modes.** Deny pod egress to `<nfs_server>:2049`, cluster-wide (AdminNetworkPolicy, Calico GlobalNetworkPolicy, Cilium CCNP). Mounts are made from the host network (E5), so they keep working, while every non-hostNetwork pod loses the AUTH_SYS path entirely. Pod Security "baseline" forbids hostNetwork, which removes the residual for non-privileged tenants. The caveats are real but bounded:
  - policy needs an enforcing CNI (flannel alone does not enforce);
  - per-namespace NetworkPolicy can be deleted by namespace admins, hence the cluster-scoped form.
- **Migration from the current design.**
  - Modes never mix on one driver (design §12).
  - An existing static PV cannot be converted in place: a `/32` inside a MANAGED CIDR is an overlap, so FOREIGN, so FAILED_PRECONDITION.
  - Flipping `attachRequired` means recreating the CSIDriver object (E1).

  Choosing per-node later for new drivers is cheap. Choosing it as the default now forces every first install through E4. Pre-release, no PVs exist, so either default is reversible in practice.

**Judgement.** The evidence settles it against **reversing** ADR 0004:
- E6 and E7 show that per-node, in the stateless form this project chose, adds a stale-export failure that is invisible and can be worse than static;
- E4 makes it non-default-able without re-templating a third-party component;
- E9 and E10 show that no ZFS driver ships it, and the one vendor that does ships it opt-in and stateful;
- the F6 gain it buys is obtainable more cheaply and more completely by pod-egress denial, independent of mode.

Keeping ADR 0004 needs no owner question (A3). The decision is reversible: the mode is a driver-wide flag, and nothing is released.

**Verdict: DECISIVE+REVERSIBLE: reject the reversal and keep ADR 0004's static-first.** The ADR records:
1. **F6's primary control is deployment.** The ADR states it explicitly: cluster-scoped deny of pod egress to the storage host's 2049/tcp (and 111 and 20048 for v3), Pod Security baseline or higher on tenant namespaces, and a host firewall limiting 2049 to the node subnet. The deploy manifests ship an example.
2. Static mode keeps `nfs.clients` **required**, with no default CIDR, plus the AUTH_SYS warning (design §3).
3. Per-node stays "later, behind `nfs-mode=per-node`" with **added prerequisites** from this pass:
   - a `/32` stale-entry GC, either a zfs-csi controller loop against VolumeAttachments or a Reconcile drift kind "published-to-unknown-node", fed a node list;
   - a documented `--nodeid` override for csi-driver-nfs (E4);
   - a refusal of hostname node ids.
4. Revisit trigger: krb5p lands, or a deployment cannot enforce pod-egress policy.

The architect's per-node default would become ASK-OWNER only if the coordinator rejects pod-egress policy as F6's control. In that case the question is:

> "NFS exports use AUTH_SYS, so any pod whose traffic reaches the storage host from an exported node can read any volume it can name. Options: (1, recommended) keep ADR 0004's static per-StorageClass CIDR as the default and require a cluster-wide policy denying pod egress to the storage host's NFS ports (mounts use host networking and are unaffected); (2) make per-node /32 export at ControllerPublish the default, which needs the attacher sidecar, re-templating csi-driver-nfs to use node IPs as node ids, and a garbage collector for stale /32 entries left by node IP changes; (3) both: per-node default plus the policy."

---

## D4. Two-person rule for Reconcile deletions

**Steelman building it now:**
- mass deletion is the one catastrophic RPC;
- an `ApproveReconcile(digest)` by a second principal stops a single compromised or careless operator;
- it fits the digest flow from D3.

**Attack on "build":**
- It needs distinct principals. Today every admin caller is root on a 0600 socket (ADR v2 §2). `SO_PEERCRED` shows `uid=0` for all of them (ADR 0001 Consequences, "Root pods look alike").
- A two-person rule between two root shells on one host is theatre: either one can `zfs destroy` directly.
- A keyed approval token needs key storage and still does not prove a *second* person (ADR v2 Alternatives).
- Approvals in memory die on restart. Persisting them adds a source of truth outside ZFS (ADR 0003 tension).
- **No comparable system does this at this layer.** OpenZFS has `zfs allow` delegation but no dual control. democratic-csi, truenas-csi and zfs-localpv expose no bulk GC at all. Dual control lives in the operator's change process, not the storage daemon.

**Attack on "defer":** the residual is a single root operator running a dry run, echoing the digest and collecting. D3 (digest required), D9 (exported volumes skipped) and D2 (admin socket only) already cut the blind, wrong-tenant and in-use paths. What remains is an intentional root action, which is out of scope by the same logic as D11.

**Migration.** Adding `ApproveReconcile` later is a new RPC plus a precondition: additive. Deferral breaks nothing.

**Judgement.**
- The evidence settles it: the control is meaningless until callers have distinct identities.
- It is reversible: purely additive.
- Its origin is the threat model itself (A2), not an owner process requirement. "Process, outward" does not apply, because no consumer or automation calls Reconcile.

**Verdict: DECISIVE+REVERSIBLE: defer.** The ADR names the trigger: "build it when Reconcile is called by automation, or when two admin principals exist (non-root `-allow-uids` or mTLS)".

---

## D12. Default `snapdir=disabled`

**Facts (OpenZFS 2.4.4):**
- `snapdir=disabled|hidden|visible`, default `hidden` (Z `man/man7/zfsprops.7` L1901).
- `zfs_admin_snapshot=0` by default, so mkdir/rmdir in `.zfs/snapshot` cannot create or destroy snapshots, even over `no_root_squash` exports (Z `man/man4/zfs.4` L1644).
- Snapshots automount on access and expire after 300 s (L1640).
- The endpoint sets nothing, so volumes inherit (A4).

**Steelman disabled:**
- with `hidden`, `.zfs/snapshot/<name>` stays reachable by path over NFS, so data a user deleted (a leaked secret, an erasure request) remains readable to every client of the export until the snapshot goes;
- snapshots taken by foreign tools (sanoid, replication) are exposed the same way;
- clients can trigger server-side automounts, which grows the mount table.

**Attack on disabled-by-default:**
- The exposure is to the **same** principals who could read the live data at snapshot time, and the volume itself is the tenant boundary. It is not a cross-tenant leak.
- It removes user self-service restore, which is user-visible.
- Setting it **locally** at create would also override an operator's parent-level choice, which inverts ADR 0003's "OpenZFS decides" by stamping our opinion onto each volume.

**Attack on "keep hidden and set it":** the same objection applies. Setting `hidden` locally would defeat a parent `snapdir=disabled`.

**Judgement.**
- The OpenZFS-native answer settles it: **set nothing and let the property inherit**. The operator picks per tenant parent with one `zfs set snapdir=disabled <parent>`, and a StorageClass may override it through the existing allowlist.
- That is today's behaviour, so nothing is built and nothing migrates.
- It is reversible: a create-time default could be added later and would affect only new volumes.
- It is not owner-worthy, because the choice is delegated to the operator by OpenZFS's own inheritance.

**Verdict: DECISIVE+REVERSIBLE: no create-time snapdir. Inherit from the parent and document.** The docs say:
- `hidden` ≠ inaccessible;
- set `snapdir=disabled` on the tenant parent when erasure or compliance matters;
- `zfs_admin_snapshot` must stay 0.

Optional cheap check: a Reconcile or startup *info* line reporting the parent's effective snapdir.

---

## Summary

| D | Architect | This pass | Why it flipped |
|---|---|---|---|
| D1 | ASK-OWNER | DECISIVE+REVERSIBLE | Already built. The alternative contradicts Reconcile. The requirement was an agent's (A1). |
| D8 | ASK-OWNER (reverse ADR 0004) | DECISIVE+REVERSIBLE (keep ADR 0004, add pod-egress policy) | Stale `/32` after IP churn (E6, E7); csi-driver-nfs node ids are hostnames (E4); no ZFS driver ships per-node, and Trident's is opt-in and stateful (E9, E10); the owner already answered (A3). |
| D4 | ASK-OWNER | DECISIVE+REVERSIBLE (defer) | Meaningless without principals. The idea came from the threat model itself (A2). Additive later. |
| D12 | ASK-OWNER | DECISIVE+REVERSIBLE (inherit) | The endpoint sets nothing. Parent inheritance is OpenZFS's own delegation (A4). |

```json
[
  {"decision":"D1 tenancy unit = parent dataset (one instance, socket dir, group per parent) instead of ADR 0001 v1 volume_prefix scoping",
   "verdict":"DECISIVE+REVERSIBLE",
   "recommend":"Parent = tenant, as already built. No in-process scoping. ADR 0001 v2 records that the shared-parent requirement came only from v1's own context (commit 2ed1800), not the owner, and that scoping can be added later additively.",
   "irreversible":false,
   "evidence":["docs/adr/0001-transport-auth.md L10 (sole source of 'several clusters share one parent')","wave11/adr-0001-v2.md Context items 3-4","https://github.com/democratic-csi/democratic-csi/blob/69c7ebcd66fe8105e514e3e93e05c555b06d987a/src/driver/controller-zfs/index.js#L85-L100"],
   "question_if_asked":null},
  {"decision":"D8 NFS default: per-node /32 at ControllerPublish vs static StorageClass CIDR at create",
   "verdict":"DECISIVE+REVERSIBLE",
   "recommend":"Keep ADR 0004: static CIDR first and default (nfs.clients required, AUTH_SYS warning). Record that F6's primary control is a cluster-scoped deny of pod egress to the storage host's NFS ports, plus a host firewall to the node subnet (mounts are hostNetwork, so unaffected). Per-node stays later, behind a flag, with new prerequisites: a stale-/32 GC against VolumeAttachments, a documented csi-driver-nfs --nodeid IP override, and refusal of hostname node ids.",
   "irreversible":false,
   "evidence":[
     "https://github.com/kubernetes/kubernetes/blob/dd410c4864ad52093ab74be9f7aa39a8cbc4dc06/staging/src/k8s.io/api/storage/v1/types.go#L306-L319",
     "https://github.com/kubernetes/kubernetes/blob/dd410c4864ad52093ab74be9f7aa39a8cbc4dc06/pkg/apis/storage/validation/validation.go#L456",
     "https://github.com/kubernetes/kubernetes/blob/dd410c4864ad52093ab74be9f7aa39a8cbc4dc06/pkg/controller/volume/attachdetach/reconciler/reconciler.go#L214-L230",
     "https://github.com/kubernetes-csi/external-attacher/blob/336d691fd8bb04b555e82cc9d812bf5e34e9d006/cmd/csi-attacher/main.go#L228-L260",
     "https://github.com/kubernetes-csi/external-attacher/blob/336d691fd8bb04b555e82cc9d812bf5e34e9d006/pkg/controller/csi_handler.go#L745-L775",
     "https://github.com/kubernetes-csi/external-attacher/blob/336d691fd8bb04b555e82cc9d812bf5e34e9d006/pkg/controller/csi_handler.go#L126-L199",
     "https://github.com/kubernetes-csi/csi-driver-nfs/blob/30ac26785aa1c3f7039b7f9a70dff123a6fe9f6a/deploy/csi-nfs-node.yaml#L103-L111",
     "https://github.com/kubernetes-csi/csi-driver-nfs/blob/30ac26785aa1c3f7039b7f9a70dff123a6fe9f6a/deploy/csi-nfs-node.yaml#L20",
     "https://github.com/kubernetes-csi/csi-driver-nfs/blob/30ac26785aa1c3f7039b7f9a70dff123a6fe9f6a/deploy/csi-nfs-driverinfo.yaml#L7",
     "https://github.com/kubernetes-csi/csi-driver-nfs/blob/30ac26785aa1c3f7039b7f9a70dff123a6fe9f6a/pkg/nfs/nodeserver.go#L262",
     "https://github.com/democratic-csi/democratic-csi/blob/69c7ebcd66fe8105e514e3e93e05c555b06d987a/src/driver/controller-zfs/index.js#L91-L94",
     "https://github.com/truenas/truenas-csi/blob/6443642bed8122afd83847c4b81ceb9f50fc1cf8/pkg/driver/controller.go#L1622-L1745",
     "https://github.com/truenas/truenas-csi/blob/6443642bed8122afd83847c4b81ceb9f50fc1cf8/deploy/truenas-csi-driver.yaml#L134",
     "https://github.com/NetApp/trident/blob/ea857409dab71b29721f0fd22a5f240181e62105/storage_drivers/types.go#L138-L139",
     "https://github.com/NetApp/trident/blob/ea857409dab71b29721f0fd22a5f240181e62105/storage_drivers/ontap/ontap_common.go#L6103-L6125",
     "https://github.com/NetApp/trident/blob/ea857409dab71b29721f0fd22a5f240181e62105/frontend/csi/controller_server.go#L345-L373",
     "https://github.com/openebs/zfs-localpv/blob/8759e5071856db4b1b251d4fa294b0e6816cb7df/deploy/zfs-operator.yaml#L2963",
     "docs/adr/0004-support-floor-and-node-side.md (accepted; owner was asked this question)"],
   "question_if_asked":"Only if pod-egress policy is rejected as F6's control: 'NFS uses AUTH_SYS, so any pod whose traffic reaches the storage host from an exported node can read volumes. Options: (1, recommended) keep the static per-StorageClass CIDR default and require a cluster-wide policy denying pod egress to the storage host's NFS ports; (2) make per-node /32 at ControllerPublish the default, which needs the attacher sidecar, csi-driver-nfs re-templated to IP node ids, and a GC for stale /32s left by node IP changes; (3) both.'"},
  {"decision":"D4 two-person rule for Reconcile deletions",
   "verdict":"DECISIVE+REVERSIBLE",
   "recommend":"Defer. Rely on the D3 required plan_digest, D9 skipping exported volumes and the D2 admin-only socket. Build ApproveReconcile when Reconcile is automated or two distinct admin principals exist (non-root -allow-uids or mTLS). Additive.",
   "irreversible":false,
   "evidence":["docs/adr/0001-transport-auth.md Consequences ('Root pods look alike')","wave11/adr-0001-v2.md §5 and Alternatives (HMAC token)","wave11/prompts.md L4 (no owner request for dual control)"],
   "question_if_asked":null},
  {"decision":"D12 default snapdir=disabled for filesystems",
   "verdict":"DECISIVE+REVERSIBLE",
   "recommend":"Set no snapdir at create (today's behaviour). Volumes inherit from the tenant parent. Document that hidden is not inaccessible over NFS, recommend 'zfs set snapdir=disabled <parent>' where erasure or compliance matters, keep the per-StorageClass override in the allowlist, and require zfs_admin_snapshot=0.",
   "irreversible":false,
   "evidence":["https://github.com/openzfs/zfs/blob/71a9f9578616a90c3c14bb59629fb4d31bfd68d1/man/man7/zfsprops.7#L1901","https://github.com/openzfs/zfs/blob/71a9f9578616a90c3c14bb59629fb4d31bfd68d1/man/man4/zfs.4#L1640-L1652","internal/endpoint/props.go L118 (allowlist only; no create-time default)"],
   "question_if_asked":null}
]
```
