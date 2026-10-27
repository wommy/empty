# CSI controller / node seam for NFS export (HEAD bdf89d0, read-only scout)

Scope: how `internal/csi` should reach the endpoint's export RPCs, whether the node side can be
delegated to kubernetes-csi/csi-driver-nfs, and what volume expansion and topology should say.
No repo file was edited. Reference clones are under
`/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/repos/`.
Not verified: kubelet's wait-for-attach behavior when `attachRequired: true` (no kubernetes clone
pinned; GitHits not queried), and NFS client-side `df` after a server-side refquota change.

## 0. Current state

- `internal/csi/controller.go:49-81` advertises no PUBLISH_UNPUBLISH_VOLUME. Its switch covers
  volumes, snapshots, list, capacity, clone, and expand only.
- `internal/csi/controller.go:85-89` still says "No export or attach exists yet, so a volume is
  usable from one node only." That comment is stale: the endpoint now offers exports
  (`internal/server/server.go:85-87` advertises CAPABILITY_FILE_EXPORT when root).
- `internal/csi/identity.go:42-52` advertises CONTROLLER_SERVICE and VOLUME_EXPANSION ONLINE only.
  No node service exists in `cmd/zfs-csi/main.go:52-53` (Identity and Controller registered only).
- `internal/csi/controller_test.go:36-46` pins the exact controller capability list. Enabling
  PUBLISH is a deliberate test change.
- Endpoint export RPCs: `api/zfsendpoint/v1/endpoint.proto:40-51` (per-client, FILE_EXPORT only),
  messages `:209-226`. Each call takes one client, not a list.

## 1. ControllerPublish / ControllerUnpublish mapped onto the export RPCs

### 1a. Where the "client" comes from

- CSI `node_id` is whatever the node plugin returned from NodeGetInfo (spec `csi.proto:825-900`,
  NodeGetInfoResponse `:3185-3215`). The controller receives no address.
- csi-driver-nfs returns `nodeID` from `--nodeid` (`pkg/nfs/nodeserver.go:260-264`), set by the
  DaemonSet to `spec.nodeName` (`deploy/csi-nfs-node.yaml:105-111`). That is a Kubernetes node
  name, not an address. The endpoint accepts hostnames, IPv4, or IPv4 CIDR only
  (`internal/endpoint/export.go:58-100`, `normalizeHost`). IPv6 is rejected.
- Two ways to make node_id an address. (1) Run the node plugin with
  `--nodeid=$(HOST_IP)` from `fieldRef: status.hostIP`, so node_id is an IPv4 address. Caveat:
  the source IP the kubelet uses to mount may differ from hostIP on multi-NIC hosts. (2) Have the
  controller resolve node name to InternalIP through the Kubernetes API (needs a Node informer
  and RBAC, which the controller pod does not have today).
- Security: the endpoint takes any string that passes `normalizeHost`. `node_id = "10.0.0.0/8"`
  is accepted (`export.go:71-81`), and `export_contract_test.go:122` shows CIDRs flowing through
  Unpublish too. The controller must not pass node_id through unvalidated. Require a single IPv4
  address (or, if hostnames are chosen, a hostname that matches the Node object).

### 1b. What goes into publish_context, and what csi-driver-nfs reads

- The endpoint returns only the export path (`internal/server/server.go:91-96` returns
  `row.Mount`). The proto comment at `endpoint.proto:217-218` says `server:export_path`. The
  server test pins a bare path (`internal/server/server_test.go:158`, "/tank/vols/fs"). The comment
  is wrong. Fix the comment, not the code.
- The NFS server address is not in the endpoint's response. The controller needs its own config
  (truenas-csi does the same: `pkg/driver/controller.go:1622-1745`, `NFSServer()`).
- csi-driver-nfs NodePublish reads only volume_context: `server`, `share`, `subdir`,
  `mountoptions`, uid/gid, lowercased keys (`pkg/nfs/nodeserver.go:85-101`), and requires
  `server` and `share` (`:127-131`). It never reads publish_context. This is the central
  incompatibility. Either volume_context carries server and share at CreateVolume (the path
  must then be stable, see section 5), or the node plugin is forked to read publish_context.
- Reading the spec: publish_context is optional and opaque (`csi.proto` ControllerPublishVolumeResponse).
  So nothing forces its use. Keeping server and share in volume_context and leaving publish_context
  empty is spec-conformant.

### 1c. readonly

- Export mode = `readonly || access_mode == SINGLE_NODE_READER_ONLY`. The current code allows
  READER_ONLY (`controller.go:85-89`).
- Never export rw when the request is ro. The reverse (export ro, node mounts rw) gives EROFS,
  which is the safe failure.
- The node side also adds `ro` from NodePublish's readonly flag (`nodeserver.go:75-78`), so both
  layers agree.

### 1d. Idempotency

- Publish: the endpoint returns OK for the same client in the same mode, and ALREADY_EXISTS for
  the other mode (`export.go:318-350`, `endpoint.go:561-600`). That maps to the spec's "volume
  published but incompatible" ALREADY_EXISTS (`spec.md:1409-1422`).
- Unpublish: a non-exported client is OK (`export.go:352-386`).
- SINGLE_NODE_WRITER: the spec says FAILED_PRECONDITION if the volume is published to another node
  (`spec.md:1409-1422`). The endpoint has no list of clients. `Volume` (`endpoint.proto:84-95`)
  has no clients field, and the list lives in a user property (`export.go:19-23`). The controller
  cannot enforce single-node today. This is a real gap.

### 1e. Unpublish with empty node_id

- Spec (`csi.proto:874-893`, `spec.md:1423-1437`): if node_id is unset, the SP MUST unpublish from
  all nodes it is published to.
- Endpoint: `UnpublishFileExport` with an empty client is rejected as InvalidArgument
  (`export.go:66-70`; `export_test.go:55` lists "" among invalid clients). There is no
  "unexport everything" operation and no way to list the current clients.
- Needed: either (a) a `repeated string clients` field on Volume (or a ListFileExports RPC), so
  the controller can loop over unpublish calls, or (b) an explicit unpublish-all semantic on the
  endpoint. Option (a) is additive and needs no new error semantics.

## 2. Can csi-driver-nfs's node plugin pair with a different controller?

Yes, with three conditions.

1. Driver name. The node plugin's GetPluginInfo name is `--drivername`
   (`cmd/nfsplugin/main.go:32`, default `nfs.csi.k8s.io`, `pkg/nfs/nfs.go:75`;
   `pkg/nfs/identityserver.go:33-46`). The node-driver-registrar registers that name with the
   kubelet. Run it with `--drivername=zfs-endpoint.wommy.github.io`. Our CSIDriver object and our
   StorageClass provisioner must use the same name.
2. volume_context keys: `server`, `share` (required), `subdir`, `mountoptions`
   (`nodeserver.go:85-101`). Our controller returns none of these today
   (`internal/csi/controller.go:203-219`, `volumeOut`). They must be added.
3. attachRequired. csi-driver-nfs ships `attachRequired: false`
   (`deploy/v4.13.5/csi-nfs-driverinfo.yaml:5-10`) and its controller has no csi-attacher sidecar
   (`deploy/csi-nfs-controller.yaml`: provisioner, resizer, snapshotter, livenessprobe only). Its
   ControllerPublish/Unpublish return Unimplemented (`pkg/nfs/controllerserver.go:335-341`). The
   node plugin does not need ControllerPublish. It does need NodeGetInfo to be correct if we
   attach.

If we set attachRequired=false (our CSIDriver object), ControllerPublish is never called. Per-client
exports are impossible, and the only authorization point is at create time (section 2a below).
If we set attachRequired=true, we need an external-attacher sidecar in our controller pod, and
the node plugin's node_id must be the value we validate in 1a.

Node-side pieces csi-driver-nfs does not provide to us: NodeStageVolume and NodeExpandVolume are
Unimplemented (`nodeserver.go:358-366`). NodeGetCapabilities advertises no STAGE_UNSTAGE
(`pkg/nfs/nfs.go:128-131`), so kubelet goes straight to NodePublish. That is correct for NFS.

### 2a. Comparison of the two flows

| | attachRequired=false, static export | attachRequired=true, per-node export |
|---|---|---|
| Who gets access | Every client in StorageClass `nfs.hosts` / `nfs.networks` (truenas-csi `controller.go:493-497`) or a CIDR, at CreateVolume | Only the node the attacher publishes to |
| Fencing a dead node | None. Access persists until DeleteVolume | Unpublish on VolumeAttachment deletion removes that node |
| Extra components | None | external-attacher sidecar; CSIDriver attachRequired=true |
| Endpoint changes | CreateVolume-time PublishFileExport with CIDRs, a list call for Delete | Publish/Unpublish wiring, client list for single-node and unpublish-all |
| Matches | democratic-csi NFS: driver-wide `sharenfs` at create, `node_attach_driver` in volume_context (`controller-zfs-generic/index.js:116-150`, `freenas/api.js:297-314`); truenas-csi: hosts at create, ControllerPublish only returns server+path, Unpublish no-op (`controller.go:1622-1758`) | Adversarial design recommendation (`docs/research/gaps/adv-file-export-design.md:72-80`) |

Practice in the surveyed drivers: none uses a per-node client list. democratic-csi and truenas-csi
both authorize at create time. Every driver with PUBLISH_UNPUBLISH in the surveyed set
(truenas-csi) returns a no-op Unpublish. Synology sets attachRequired=true with
ControllerPublish still a TODO (`synology-csi/deploy/helm/templates/csidriver.yaml:9`), which is a
cautionary case: an advertised-looking flag without the RPC.

## 3. ControllerExpandVolume and node_expansion_required

Current code: `controller.go:344-367`. Flag = `kind == BLOCK && volume_capability.GetMount() != nil`.
Test: `controller_test.go:50-95`.

- NFS (FILESYSTEM kind): flag false, already correct. The refquota is server-side and grows at
  once. Clients see it through NFS statfs (not verified in a live mount). No node work exists or
  is needed.
- Raw block (BLOCK, no mount): flag false is correct. The spec allows SP to skip node expansion
  for block (`csi.proto:1585-1588`). Block export does not exist yet (`README.md:16`), so no node
  can publish a zvol today.
- zvol with a filesystem (BLOCK + mount): nothing in this repo can run NodeExpandVolume for our
  driver. The flag is true only when the CO sends a volume_capability. The spec makes that field
  optional (`csi.proto` ControllerExpandVolumeRequest, "This is an OPTIONAL field"), so a CO that
  omits it gets false, and a filesystem that never grows. Both outcomes are wrong.
  `capSupported` (`controller.go:122-142`) also accepts mount access on zvols, with the comment
  "adopted from the baseline", so the case is reachable.
- Recommended answer:
  1. Keep NFS and raw block at false.
  2. For zvol+mount: reject mount access on BLOCK kind in ValidateVolumeCapabilities and in
     publish (FAILED_PRECONDITION with a message), until a node service that can resize exists.
     This reverses the adopted-volume allowance in `capSupported`. Owner decision.
  3. In ControllerExpandVolume, do not infer from a missing capability. Either read kind from the
     endpoint's Volume (already fetched via the expand response) and treat zvol+mount as the
     refused case from step 2, or return an error.

## 4. Topology

- Do not advertise VOLUME_ACCESSIBILITY_CONSTRAINTS. Keep accessible_topology unset in CreateVolume
  and NodeGetInfo. The CO then ignores topology (spec `csi.proto:235-241`, `:428-431`).
  `docs/research/gaps/csi-frontend.md:38` and `:122` reach the same conclusion. csi-driver-nfs's
  NodeGetInfo (`nodeserver.go:260-264`) returns no topology either.
- One storage host means no per-node accessibility differences inside the cluster. If some nodes
  cannot route to the NFS storage network, that is a deployment check, not topology. Topology
  would be the right tool only if the owner wants to pin volumes to a network segment.

## 5. Seams found on the way

1. Stale doc: `endpoint.proto:217-218` (server:export_path) vs `server.go:96` and
   `server_test.go:158` (bare path).
2. Stale comment: `controller.go:85`.
3. Mountpoint stability: the export path is `Volume.mountpoint`, which is inherited. A parent
   mountpoint change re-shares every child at a new path (`docs/research/gaps/adv-file-export-design.md`
   finding 5). csi-driver-nfs stores `share` in volume_context at create time and never refreshes it.
   So for csi-driver-nfs the path must be pinned at create (`mountpoint` set locally), or PVs break
   silently after a parent change. The endpoint's own comment ("read fresh on every call, do not
   store it", `endpoint.proto:217-218`) is the opposite of what volume_context does. Owner decision.
4. RWX. `modeSupported` allows SINGLE_NODE only (`controller.go:85-89`). Per-client exports make
   MULTI_NODE_MULTI_WRITER for filesystems possible; `controller_test.go:131` expects that to be
   rejected today. Owner decision.
5. Sanity. Enabling PUBLISH makes csi-sanity run the Publish/Unpublish specs
   (`docs/research/edges2/sidecar-conformance.md`, sanity capability gates), and the sanity focus
   (`internal/csi/sanity_test.go:26`) would need widening. Node specs also fail on a controller-only
   socket (`docs/research/gaps/adv-csi-frontend.md` B2), so the focus stays controller-only and
   node behavior is tested in csi-driver-nfs's own suite.
6. Authorization. ADR 0001 (`docs/adr/0001-transport-auth.md`) is still "proposed". Its principal
   policy scopes by RPC and volume prefix, not by client address. Until it lands, any caller with
   socket access can export a volume to any CIDR. A front end that mints export clients from node_id
   therefore needs the node_id validation in 1a.
7. Repo documentation: `docs/agents/domain.md` names a root `CONTEXT.md`, which does not exist
   (`ls` at HEAD).
8. Driver name pairing: csi-driver-nfs's `--drivername` must equal `DefaultDriverName` in
   `internal/csi/identity.go:22` (`zfs-endpoint.wommy.github.io`) and our CSIDriver object name.
   Nothing in the repo enforces this.
9. Export-count cap: the sharenfs property length limits clients per volume
   (`docs/research/gaps/adv-file-export-design.md` finding 10). In per-node mode that is a
   per-volume node limit, not a cluster limit. Use CIDR for large clusters.

## 6. Options

- Option A, per-node export via attacher (fencing, most work). Needs: attacher sidecar, CSIDriver
  attachRequired=true, node_id validation, the client list on Volume, and a PUBLISH capability
  that is advertised only when the endpoint says FILE_EXPORT. Node side: csi-driver-nfs with
  drivername override and hostIP node id.
- Option B, static export at create (no fencing, least work). Needs: CreateVolume passes
  StorageClass `nfs.networks` / `nfs.hosts` to PublishFileExport (or a new create-time export
  parameter), DeleteVolume relies on endpoint unexport or destroy. Node side identical to A.
- Option C, own node plugin in zfs-csi. Needed only for block or for a publish_context-driven
  path. Most work. Do not build it for NFS.

## 7. Recommended design

1. Delegate the NFS node side to csi-driver-nfs: run its node DaemonSet with
   `--drivername=zfs-endpoint.wommy.github.io` and `--nodeid=$(HOST_IP)` (IPv4 only), and ship a
   CSIDriver object with that name.
2. Return `server` and `share` in volume_context from CreateVolume (server from controller config,
   share = pinned export path). Leave publish_context empty.
3. Start with Option B: create-time export to a configured CIDR list, which matches democratic-csi
   and truenas-csi. This works with attachRequired=false and needs no sidecar.
4. Before Option A: add the client list to Volume (or a list RPC), and an unpublish-all path.
   Then add ControllerPublish/Unpublish (readonly from access mode or flag, node_id validated as
   IPv4), advertise PUBLISH_UNPUBLISH only when FILE_EXPORT is on, and set attachRequired=true with
   an external-attacher sidecar.
5. Expansion: NFS and raw block return false. Refuse zvol+mount until a node resize exists.
6. Topology: advertise nothing.
7. Fix the stale proto comment and controller.go:85 now.

## 8. Open questions for the owner

- Attach or not? Option A gives per-node fencing and needs a new sidecar and the endpoint client
  list. Option B matches the baseline drivers with no fencing. Which is required before first
  deploy?
- Node identity: `--nodeid=$(HOST_IP)` (IPv4, multi-NIC risk) or API-resolved InternalIP (needs
  RBAC and an informer)? Is IPv6 out of scope?
- Pin the export path (mountpoint set locally at create) so volume_context stays valid, or accept
  the drift risk?
- zvol with a filesystem: refuse, or keep the adopted-volume allowance and accept a stuck resize?
- RWX (MULTI_NODE) for filesystems: in scope now?
- Is csi-driver-nfs's node plugin an acceptable runtime dependency (version pinning, a fork if
  publish_context is needed later)?
- Authorization: is ADR 0001 required before the controller can request exports, given a
  principal can currently export to any CIDR?
- Should the endpoint offer an unexport-all, or should the controller loop over the client list?
