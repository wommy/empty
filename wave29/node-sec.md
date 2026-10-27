# Node side (NFS) adversarial security review, wave29

HEAD f389474 (merge 6475fea csi-local), read-only. Upstream fetched from GitHub raw:
`kubernetes-csi/csi-driver-nfs` `deploy/v4.13.6/csi-nfs-node.yaml` (byte-identical at tag v4.13.6 and on master),
`csi-nfs-driverinfo.yaml`, `rbac-csi-nfs.yaml`, `pkg/nfs/nodeserver.go`, `pkg/nfs/nfs.go`, `cmd/nfsplugin/main.go` at v4.13.6;
`node-driver-registrar` v2.17.0 `main.go`; `kubernetes/k8s.io` `registry.k8s.io/images/k8s-staging-sig-storage/images.yaml` at 56620372.
Copies in `scratchpad/wave29/up/` and `up2/`.

**Verdict: nothing blocking.** The DaemonSet is a faithful, slightly hardened copy of upstream; digests match
the promoter manifest; the node never fights the endpoint's uid/gid/mode. The shipped manifests provision nothing
until the operator sets `-nfs-server`, `nfs.clients` and the F6 controls. Four findings should be fixed or written
down before serving tenants (F-1 to F-4). None is a regression from upstream csi-driver-nfs.

## 1. node.yaml against upstream, line by line

| node.yaml | Upstream | Deviation | Verdict |
|---|---|---|---|
| 38-43 ServiceAccount, `automountServiceAccountToken: false` | SA `csi-nfs-node-sa`, token mounted; `rbac-csi-nfs.yaml` binds it to nothing | no token | **safer** (nfsplugin v4.13.6 has no kube client: no `--kubeconfig` flag, no client-go in `nfs.go`/`main.go`) |
| 48-49 ns `zfs-csi` | `kube-system` | namespace | neutral (PSA `privileged` on zfs-csi, rbac.yaml:24; its comment still names only the controller's hostPath: stale, info) |
| 51-57 RollingUpdate maxUnavailable 1 | same | none | = |
| 65-66 hostNetwork, `ClusterFirstWithHostNet` | same | none | = but see **F-1** (the DNS policy is risky for this driver's server names) |
| 67-68 `automountServiceAccountToken: false` on the pod | absent | added | **safer** |
| 69 `system-node-critical` | same | none | = |
| 70-72 pod seccomp RuntimeDefault | same | none | = (the privileged nfs container runs **unconfined** anyway: `privileged: true` disables seccomp for that container) |
| 73-77 nodeSelector os=linux, tolerations `Exists` | same | none | = but see **F-3** (lands on the ZFS host and control plane) |
| 79-80 nfs image `nfsplugin:v4.13.6@sha256:40663419...` | tag only | digest pin | **safer**; digest equals images.yaml:354 |
| 82 `--v=2` | `-v=5` | less logging | neutral (V(5) gRPC logs are protosanitized; nothing secret lost or gained) |
| 83 `--drivername=zfs-endpoint.wommy.github.io` | default `nfs.csi.k8s.io` | rename | required; neutral |
| 85 `--endpoint=unix:///csi/csi.sock` literal | via `$(CSI_ENDPOINT)` env | inlined | neutral |
| 93-94 `privileged: true` only | `privileged: true`, `capabilities.add SYS_ADMIN`, `drop ALL`, `allowPrivilegeEscalation: true` | dropped the no-op caps lines | neutral (privileged grants all caps; the API refuses `privileged` + `allowPrivilegeEscalation: false`) |
| 95-103 livenessProbe port 29663 | 29653 | port | neutral/availability (avoids upstream collision; 29663 itself is not checked against other drivers' ports) |
| (absent) `imagePullPolicy: IfNotPresent` | explicit | default | neutral (non-`latest` tag + digest defaults to IfNotPresent) |
| 107-112 socket-dir rw, pods dir rw **Bidirectional** | same | none | = (gives the container every pod volume, token and secret on the node: inherent to a mounting CSI node plugin) |
| 113-133 registrar: digest, `--kubelet-registration-path` literal, no `KUBE_NODE_NAME` env | env-substituted path, `KUBE_NODE_NAME` env | path inlined; env dropped | neutral (registrar v2.17.0 `main.go` never reads `KUBE_NODE_NAME`) |
| 120-124, 141-145 sidecars `allowPrivilegeEscalation: false`, `readOnlyRootFilesystem: true`, drop ALL | drop ALL only | added | **safer** |
| 129-131, 149-151 socket-dir `readOnly: true` in sidecars | rw | read-only | **safer** (connect() on a socket ignores ro mounts, ADR 0008, verified on 6.18) |
| 132-133 registration-dir rw | rw | none | = (registrar can register any driver name on the node; upstream same) |
| 114, 135 sidecar digests | tags | digest | **safer**; f9de845b = images.yaml:78 (registrar v2.17.0), 06da0d5b = images.yaml:295 (livenessprobe v2.19.0) |
| 153-165 hostPaths | `/var/lib/kubelet/plugins/csi-nfsplugin`, `/var/lib/kubelet/pods`, `/var/lib/kubelet/plugins_registry` | plugin dir renamed to `.../plugins/zfs-endpoint.wommy.github.io` | required; matches `--kubelet-registration-path` (pinned by TestNodeDaemonSet) |
| — hostPID / hostIPC / /dev / /sys | none | none | = |
| — container order | liveness, registrar, nfs | nfs first | neutral |

No upstream security setting was weakened. hostPaths: exactly three, all under `/var/lib/kubelet`; only the nfs container
writes the pods dir and the socket dir, only the registrar writes `plugins_registry`. No custom kubelet root support
(e.g. k0s/microk8s `/var/snap/microk8s/common/var/lib/kubelet`): functional, not security.

## 2. CSIDriver (csidriver.yaml:8-12) and node publish semantics

- `attachRequired: false`: correct for static export (no ControllerPublish). Consequence: no VolumeAttachment, so the
  cluster has **no record of which node mounts what**; the server has no per-node record either (F-2).
- `podInfoOnMount: false`: fine; csi-driver-nfs ignores pod info.
- `volumeLifecycleModes: [Persistent]`: fine (upstream same; no inline/ephemeral).
- `fsGroupPolicy` **omitted** (default `ReadWriteOnceWithFSType`) vs upstream `File`. Safer here: under `root_squash`
  kubelet's recursive chown/chmod for `fsGroup` runs as root on the node, is squashed, fails, and kubelet returns an
  uncertain-mount error: with `File`, every pod with `fsGroup` would fail to start. Today it is skipped only because the
  PV has no fsType; a class that sets `csi.storage.k8s.io/fstype` would turn it on for RWO volumes. See **F-5**.
- csidriver.yaml:2 "Controller only" is now stale (a node plugin ships). Info.
- **uid/gid/mode**: the endpoint applies `nfs.uid/gid/mode` server-side before commit (ADR 0004 "Root ownership").
  csi-driver-nfs v4.13.6 `NodePublishVolume` chowns only when volume_context has `uid`/`gid`, and chmods only when
  `mountPermissions` > 0 (flag default 0, context key absent). zfs-csi's volume_context is `dataset, kind, mountpoint,
  server, share` (controller.go:83-92): none collide with nfsplugin's case-insensitive keys (`subdir`, `mountoptions`,
  `mountpermissions`, `uid`, `gid`, `ondelete`). So the node never rewrites what the endpoint set. **Matches.**
  Consequence for users: `fsGroup` has no effect; non-root pods need `nfs.gid` + a setgid `nfs.mode` (e.g. 2775)
  matching their supplemental group. Worth one line in classes.yaml.

## 3. NFS wire and export semantics

- Export options (export.go:205, 319-345): `sec=sys`, `root_squash`, `secure`, `no_subtree_check`, `nocrossmnt`;
  `rw=` list of CIDRs, floor `-nfs-min-prefix` default /16 (main.go:100), refused at 0.
- Mount options: classes.yaml sets **no `mountOptions`**. The client negotiates (normally v4.2, `hard`, `sec=sys`)
  and may fall back to v3, which needs rpcbind 111 and mountd 20048 open. Client mounts are `suid,dev` (server-side
  F12 `setuid/exec/devices=off` do not reach NFS clients, by design: ADR 0001 v2 around line 197). See **F-4**.
- `root_squash` protects only uid 0. AUTH_SYS lets any client in the CIDR claim every other uid/gid.

## 4. Findings, by severity

### F-1 (Medium; High where tenants choose namespace names). Cluster DNS resolves the NFS server name: a tenant can hijack it
- **Where**: node.yaml:66 `dnsPolicy: ClusterFirstWithHostNet`; nfs.conf.example:74-77 recommends "a stable VIP or DNS
  name"; `ValidNFSServer` (internal/endpoint/endpoint.go:300) accepts 1-3-label names without a trailing dot.
- **Risk**: mount.nfs runs inside the nfs container with the pod resolv.conf (CoreDNS, `ndots:5`, search
  `zfs-csi.svc.cluster.local svc.cluster.local cluster.local ...`). Any server name with fewer than 5 dots goes
  through the search list first:
  - `nas.lan` is tried as `nas.lan.svc.cluster.local`, which is Service `nas` in namespace `lan`;
  - `nas.example.com` is tried as `nas.example.com.svc.cluster.local`, which is pod hostname `nas`, headless
    Service `example`, in namespace `com`.

  A tenant who can create (or already owns) that namespace makes **every node** mount its NFS server for **every
  volume**. It can then read everything victims write and serve them forged data. Node-to-pod traffic is not covered
  by the F6 egress deny. The name is recorded in every PV forever (ADR 0004 Q1), so it cannot be fixed after the fact.
- **Minimal fix**: `dnsPolicy: Default` on the DaemonSet. The server is a host-level name, not a cluster Service, so
  this is a safer deviation from upstream. Also say "an IPv4 address, or an FQDN with a trailing dot" in
  nfs.conf.example and node.yaml. Optionally, `ValidNFSServer` warns on a name without the trailing dot.
- **Deploy test**: TestNodeDaemonSet asserts `dnsPolicy == "Default"` (today it pins `ClusterFirstWithHostNet`,
  k8s_test.go ~290). A test on nfs.conf.example asserts the placeholder doc names the trailing-dot rule.

### F-2 (Medium: design residual, only half acknowledged). One node in the CIDR reads and writes every tenant volume
- **Where**: classes.yaml:28 `nfs.clients`; node.yaml:25-34; ADR 0001 v2 D8 (lines 135-150); ADR 0004; ADR 0009.
- **Quantified**: the export is per CIDR, not per node. In the normal deployment (one class, `nfs.clients` = the node
  subnet), root on any one node, or any host in that subnet, can mount **100% of NFS volumes** read-write:
  - It is not limited to volumes of pods scheduled there.
  - Paths are `<parent mountpoint>/<id>`, and the NFSv4 pseudo-root lists every exported path, so it needs no API
    access.
  - It acts as any uid/gid except 0, so it reads, modifies and deletes every tenant file not owned by root.
  - It can also write volume roots whose `nfs.mode` grants a group it claims.
  - It leaves no trace: no VolumeAttachment, no ControllerPublish, no per-node export.

  This voids the Node authorizer's isolation for storage. The same holds for every **hostNetwork pod in any
  namespace**:
  - NetworkPolicy does not apply to host-network pods, and baseline PSA is required only on tenant namespaces
    (monitoring, CNI and ingress agents are the usual ones).
  - A userspace NFS client (libnfs) needs only a source port below 1024 to pass `secure`, and default container caps
    include NET_BIND_SERVICE.
  - The `/16` floor allows exports to 65k hosts.
  - The storage-host firewall in D8 limits 2049 "to the node subnet", the same CIDR, so it narrows nothing.
- **Acknowledged?** In part. node.yaml:26-27 says "any host in those networks ... can mount a volume and claim any
  uid", and D8 defers per-node /32. But no ADR states the consequence: a node, or hostNetwork pod, compromise equals
  all-tenant data compromise, with no audit trail. ADR 0004 and 0009 are silent, and the F6 controls exempt exactly
  these principals.
- **Minimal fix (docs)**: add one consequence bullet to ADR 0009 (or a D8 amendment). Every node and every
  hostNetwork workload is trusted with all NFS volumes until per-node mode or krb5p. Keep `nfs.clients` and the
  storage-host firewall to the exact node IPs (an nftables set of /32s), not the subnet. Optional hardening on each
  node: an nftables output rule that drops 2049/111/20048 from sockets in the `kubepods` cgroup
  (`socket cgroupv2 level 1 "kubepods.slice"`). This leaves the kernel's sunrpc sockets alone; check on the VM leg
  that kernel-created sockets carry no pod cgroup.
- **Deploy test**: a docs test that ADR 0009 Consequences (or D8) contains the "all NFS volumes" or "hostNetwork"
  trust statement, in the TestStorageClassClientsPlaceholder style. A classes test that the example comment tells
  operators to keep the CIDR to the node subnet and the firewall to node IPs.

### F-3 (Medium). The privileged node plugin also runs on the ZFS host
- **Where**: node.yaml:73-77 (`tolerations: - operator: Exists`, no affinity). ADR 0006/0008 make the ZFS host a
  Kubernetes node (`node-restriction.kubernetes.io/zfs-endpoint-host=true`).
- **Risk**: a privileged, unconfined pod with Bidirectional propagation, on the host that holds every pool and loaded
  key. A compromised `nfsplugin` image or a container escape there is block-level root over all tenants. That bypasses
  root_squash, the F1/F2 socket split and ADR 0008's hardening of the controller pod. `Exists` also tolerates
  `NoSchedule` and `NoExecute` taints, so tainting the ZFS host does not keep the plugin off it. Pods that land there
  make loopback NFS mounts (a writeback deadlock risk under memory pressure). If the host's source address (for
  example a VIP) is outside `nfs.clients`, pods there hang at mount. The intent ("on every node a pod may use a volume
  from", node.yaml:75) is not stated for the ZFS host by any ADR.
- **Minimal fix**: decide and write it down. Either:
  - (a) keep tenant pods off the ZFS host: taint it, and give the DaemonSet nodeAffinity
    `node-restriction.kubernetes.io/zfs-endpoint-host DoesNotExist`, or replace `Exists` with explicit tolerations;
  - or (b) state in ADR 0009 that the ZFS host runs the node plugin and serves tenant pods, and accept loopback NFS.
- **Deploy test**: TestNodeDaemonSet pins whichever choice is made, either the affinity term or a comment marker
  checked against the ADR text.

### F-4 (Low). No StorageClass mountOptions: NFS version and suid/dev left to negotiation
- **Where**: classes.yaml:24-33 (no `mountOptions:`).
- **Risk**: the v3 fallback widens the attack surface (rpcbind/mountd, and F6 has to cover 111 and 20048 too). Volumes
  are mounted `suid,dev` in pods: a setuid binary written by one uid elevates inside any pod that mounts the volume.
  Root-owned setuid files cannot be planted through root_squash, but can come from the server side (a clone source).
- **Minimal fix**: `mountOptions: [nfsvers=4.2, hard, nosuid, nodev]` in the shipped class, not `noexec`. With v4.2
  pinned, the operator can disable v3 on the server and the F6 port list shrinks to 2049.
- **Deploy test**: classes_test asserts that the `zfs` class has `nfsvers=4.x`, `nosuid` and `nodev`.

### F-5 (Low). `fsGroupPolicy` is implicit
- **Where**: csidriver.yaml:8-12.
- **Risk**: correct today only because PVs carry no fsType. A class that sets `csi.storage.k8s.io/fstype` turns on
  kubelet's recursive chown for RWO volumes. That fails under root_squash (pods stuck), or, if someone loosens the
  squash, re-owns volume roots against `nfs.uid/gid`.
- **Minimal fix**: `fsGroupPolicy: None`, with a comment that ownership is `nfs.uid/gid/mode` (server side, root_squash).
- **Deploy test**: k8s_test asserts `fsGroupPolicy == "None"`.

### F-6 (Low). Digest pins are format-checked, not value-checked
- **Where**: node.yaml:18-23, 80, 114, 135; k8s_test `pinnedDigest` checks only the shape.
- **Verified now**: all three equal the promoter manifest at 56620372 (images.yaml:78, 295, 354). They are index
  digests, so multi-arch is fine. A later bump could paste a wrong digest unnoticed.
- **Minimal fix and test**: a `testdata` table of `image:tag -> digest`, copied from images.yaml with the commit hash.
  The test fails on any image not in it. Same for controller.yaml.

### F-7 (Info). An upstream-diff test would pin every deviation
Vendor `csi-nfs-node.yaml` v4.13.6 into `deploy/testdata/`. Diff it structurally against node.yaml, and assert that
the set of differences equals an allowlist: the rows of the table in section 1. Any future copy drift (a new
hostPath, hostPID, a re-added token or capability) then fails CI. Also assert there are no `hostPID`/`hostIPC`, that
only the three named hostPaths exist, and that seccomp is RuntimeDefault. TestNodeDaemonSet checks none of these
today.

### Also checked, no finding
- The registrar's rw `plugins_registry` is the same as upstream.
- The liveness endpoint is bound to localhost.
- The sidecars run as uid 0 with no caps; the socket owner is root, so connect works without DAC_OVERRIDE.
- nfsplugin also serves its Controller service on the node socket, which only root on the node can reach.
- A static PV with a `mountOptions`/`subDir` context needs PV-create rights (admin), which is upstream behavior.
- Server-side `setuid/exec/devices=off` (plan.go:416-420) protect the storage host as intended.
