# wave21: deploy/k8s review (read-only)

Scope: deploy/k8s/{rbac,controller,classes,csidriver}.yaml, deploy/systemd/zfs-endpoint@.service(+hardening.conf),
cmd/zfs-csi/main.go, internal/csi/controller.go (params), internal/unixsock, internal/server (peercred).
Skipped as queued elsewhere: no -nfs-server / nfs.clients / node DaemonSet, csi-sanity NFS mode, probe crash-loop,
expand capability check, snapshot size.

Upstream references fetched for the pinned versions (raw.githubusercontent.com):
- external-provisioner v5.2.0 deploy/kubernetes/rbac.yaml
- external-resizer v1.13.1 deploy/kubernetes/rbac.yaml, cmd/csi-resizer/main.go, pkg/controller/controller.go
- external-snapshotter v8.2.0 deploy/kubernetes/csi-snapshotter/rbac-csi-snapshotter.yaml, cmd/csi-snapshotter/main.go
- kubernetes/k8s.io registry.k8s.io/images/k8s-staging-sig-storage/images.yaml (main)

Verdict: one functional bug (resizer never starts, so expansion never happens); nothing else is blocking.
The security posture is reasonable for the documented hyperconverged design; the items below are hardening.

---

## 1. HIGH (correctness): csi-resizer lacks `pods` RBAC, so it never syncs and never resizes

- rbac.yaml:46-52 (csi-resizer section) grants no `pods`. Upstream external-resizer v1.13.1 rbac.yaml grants
  `pods: get, list, watch`.
- Why it bites: csi-resizer v1.13.1 `--handle-volume-inuse-error` defaults to **true** (cmd/csi-resizer/main.go:87).
  With it on, the controller builds a pod informer (pkg/controller/controller.go:130-135) and `Run` blocks in
  `cache.WaitForCacheSync(pvSynced, pvcSynced, podListerSynced)` (controller.go:278-283). With pods forbidden,
  the reflector loops on "pods is forbidden" and the sync never completes: no PVC expansion is ever processed,
  even though classes.yaml:12 sets `allowVolumeExpansion: true`. A user's resize stays pending forever
  with no event from the resizer.
- The rbac.yaml:1-3 comment ("as their upstream deploy/kubernetes/rbac.yaml grant them") is false here.
- Minimal fix (least privilege, preferred): add `--handle-volume-inuse-error=false` to the csi-resizer args
  (controller.yaml:105-107). The driver advertises ONLINE expansion (internal/csi/identity.go:49-50) and has
  no node expansion for filesystems, so the "volume in use" handling is irrelevant and no cluster-wide pod read
  is needed. Alternative: add `pods: [get, list, watch]` to the ClusterRole.
- Test (deploy_test.go): `TestSidecarRBACCoversInformers` - for csi-resizer, unless its args contain
  `--handle-volume-inuse-error=false`, require a ClusterRole rule with apiGroup "" / resource "pods" / verbs
  ⊇ {get,list,watch}. More generally, embed a per-sidecar table of the upstream rules for the pinned tag
  (copied from upstream rbac.yaml, keyed by image tag so a bump fails the test until the table is refreshed)
  and assert the ClusterRole is a superset of the required set and a subset of the union (see #3).

## 2. MEDIUM (security): the node-pinning label can be self-applied by any kubelet

- controller.yaml:5-6 (instructions) and :54-55 `nodeSelector: zfs-endpoint.wommy.github.io/host: "true"`.
- NodeRestriction only stops kubelets from setting labels under `kubernetes.io`/`k8s.io` (outside its
  allowlist) and `node-restriction.kubernetes.io/`. A custom-domain label like this one can be set by a kubelet
  on its own Node object. A compromised node can label itself, create `/run/zfs-endpoint/sock/tank-k8s-vols/`
  (so the `type: Directory` hostPath check passes) and serve a fake endpoint socket there. The next time the
  controller pod is rescheduled (real host NotReady -> eviction, a rollout, or the scheduler choosing between
  two labelled nodes) it lands on the attacker node, which then:
  - holds the SA token: cluster-wide PV create/patch/delete, PVC update, VolumeSnapshot/Content patch (rbac.yaml);
  - answers CreateVolume with volume contexts it chooses (e.g. `server`/`share` pointing at an attacker NFS
    server), and answers ListVolumes/DeleteVolume as it likes.
- Same mechanism, no attacker: an operator labelling a second ZFS host (or a host also running
  `zfs-endpoint@tank-k8s-vols`) lets the controller silently switch backends; DeleteVolume of a
  volume the other host doesn't know returns success and PVs are released while data stays on the first host.
  zfs-csi never checks which endpoint/pool it is talking to.
- Minimal fix: use a label kubelets cannot set, e.g.
  `node-restriction.kubernetes.io/zfs-endpoint-host: "true"` (the documented pattern for this). Document
  "label exactly one node". Optional follow-up: have zfs-csi log/check the endpoint's pool GUID at startup.
- Test: in TestManifests, assert every nodeSelector / required nodeAffinity key in the Deployment has the
  prefix `node-restriction.kubernetes.io/`, and that the controller.yaml header's `kubectl label` line uses the
  same key (grep the comment for the key string).

## 3. MEDIUM-LOW (security): VolumeSnapshot over-grant

- rbac.yaml:34-36: `volumesnapshots: get, list, watch, update, patch`.
- Upstream: provisioner v5.2.0 needs `get, list` only; snapshotter v8.2.0 needs **no** volumesnapshots access
  (it works on VolumeSnapshotContents; VolumeSnapshots are snapshot-controller's job). update/patch/watch are
  granted to nobody upstream.
- Risk: the SA (whose token is also in the root, hostPath-mounting zfs-csi container, see #7) can patch any
  VolumeSnapshot in any namespace: strip `snapshot.storage.kubernetes.io/volumesnapshot-as-source-protection`
  / bound-protection finalizers so an in-use snapshot is deleted, or rewrite labels/annotations other tooling
  relies on.
- Minimal fix: `verbs: ["get", "list"]`.
- Test: covered by the subset check in #1's table (ClusterRole rules ⊆ union of upstream rules for the pinned
  tags); at minimum assert volumesnapshots verbs == {get,list}.

Rest of the ClusterRole matches the union of the three upstream runners exactly (PV, PVC, PVC/status, SC,
events, csinodes, nodes, volumeattachments, VAC, VSClass, VSContent, VSContent/status). Correctly omitted:
leases (no `--leader-election`, all three default false), csistoragecapacities/pods/replicasets (no
`--enable-capacity`), groupsnapshot.* (CSIVolumeGroupSnapshot gate off by default in snapshotter v8.2.0).

## 4. LOW-MEDIUM (security): endpoint socket directory mounted read-write

- controller.yaml:80-81 (volumeMount, no `readOnly`) and :132-137 (hostPath on the host's `/run` tmpfs).
- zfs-csi runs as uid 0 and the directory is 0700 root, so the container can write into the host's
  `/run/zfs-endpoint/sock/tank-k8s-vols/`: unlink `endpoint.sock` (controller outage until the endpoint
  restarts), plant `.NNN` dirs that the endpoint's leftover cleanup inspects, or fill the host `/run` tmpfs
  (shared with systemd and every runtime socket) - a host-wide DoS from a compromised controller container.
- zfs-csi only `connect()`s. On Linux a read-only mount refuses writes with EROFS only for regular files,
  directories and symlinks (inode_permission); connect() to a socket on a ro bind mount works.
- Minimal fix: `readOnly: true` on the endpoint-dir volumeMount (optionally also make the hostPath
  read-only via the mount). Verify once on the VM leg (connect over a ro bind).
- Test: in TestManifests, for every container mount whose volume is a hostPath, require `readOnly: true`.

## 5. LOW (availability): no resource requests/limits on any container

- controller.yaml:63, 82, 103, 116: no `resources`. The pod is BestEffort on the storage host, first in line
  for kubelet eviction under memory/disk pressure (the priority class only breaks ties), and unbounded:
  a leaking sidecar (informers over all PVs/PVCs/pods cluster-wide) competes with the ZFS ARC and the
  endpoint on the same host.
- Minimal fix: small requests on all four (e.g. 10m/20Mi zfs-csi, 10m/50Mi per sidecar) and memory limits
  (e.g. 256Mi sidecars, 128Mi zfs-csi); requests==limits for memory if Guaranteed QoS is wanted.
- Test: every container has `resources.requests.memory`, `resources.requests.cpu` and `resources.limits.memory`.

## 6. LOW (deployability): namespace has no Pod Security label; critical priority class

- rbac.yaml:5-8: Namespace `zfs-csi` without `pod-security.kubernetes.io/enforce`. The pod needs a hostPath
  and runs as root, which `baseline` (and `restricted`) refuse. On a cluster whose PSA default is baseline or
  restricted, the ReplicaSet is refused pod creation; the only trace is a FailedCreate event on the RS.
- controller.yaml:53 `priorityClassName: system-cluster-critical`: several distributions (e.g. GKE) install a
  ResourceQuota/admission config limiting the system-* classes to kube-system, which also yields FailedCreate.
  It also lets this pod preempt workloads on the storage host.
- Minimal fix: label the namespace `pod-security.kubernetes.io/enforce: privileged` explicitly (with a comment
  why: hostPath + uid 0), and either document the priority-class quota or ship a dedicated PriorityClass.
- Test: Namespace has the enforce label; if the pod uses hostPath or runAsNonRoot=false, the label must be
  `privileged`.

## 7. LOW (security): SA token mounted into zfs-csi, which calls no Kubernetes API

- controller.yaml:52 serviceAccountName with default automount: the token reaches all four containers,
  including zfs-csi (rbac.yaml:3-4 says it calls no API), the only one with a host mount.
- Minimal fix: `automountServiceAccountToken: false` on the pod plus a projected `serviceAccountToken`
  volume (+ ca.crt / namespace) mounted only into the three sidecars at
  `/var/run/secrets/kubernetes.io/serviceaccount`.
- Test: the zfs-csi container mounts no volume with `projected.sources[].serviceAccountToken`, and pod
  `automountServiceAccountToken` is false.

## 8. LOW (correctness): termination grace equals zfs-csi's drain

- No `terminationGracePeriodSeconds` (default 30s) while zfs-csi `-drain` defaults to 30s (cmd/zfs-csi/main.go:45)
  and the endpoint can hold a call for `-lock-wait` 30s; kubelet SIGKILLs exactly when the drain ends, and the
  sidecars are stopped in parallel anyway. The endpoint finishes the zfs op on its own and calls are
  idempotent, so the cost is a retried call, not corruption.
- Minimal fix: `terminationGracePeriodSeconds: 45` (mirrors the unit's TimeoutStopSec rationale).
- Test: grace period > zfs-csi `-drain` (parsed, default 30s).

## Checked, no finding

- Pod security context: no privileged, no hostNetwork/hostPID/hostIPC, `capabilities.drop: [ALL]`,
  `allowPrivilegeEscalation: false`, `readOnlyRootFilesystem: true` on all four, seccomp RuntimeDefault.
  uid 0 / runAsNonRoot false is the documented ADR 0006 consequence (0600 root sockets). The endpoint
  additionally checks SO_PEERCRED (root or its own uid; internal/server/roles.go:29). Possible later hardening:
  a group-owned 0660 CSI socket would let the three sidecars run non-root (unixsock.ShareWithGroup exists).
- Only one hostPath: the per-instance socket dir (not /run/zfs-endpoint, not admin/); already tested (F1/F2).
  `type: Directory` keeps the pod from starting on a node without the endpoint. The endpoint never removes and
  recreates that directory (bindPrivate binds in place), so the bind mount survives endpoint restarts;
  RuntimeDirectoryPreserve=yes keeps it across instance stops.
- Node pinning: off-host scheduling fails closed (hostPath type Directory) except for the spoofing case in #2.
- Leader election: replicas 1 + Recreate and every sidecar's `--leader-election` defaults false (provisioner
  v5.2.0 csi-provisioner.go:91, snapshotter v8.2.0 main.go:77); no leases needed. Note: a node partition
  leaves the old pod Terminating and the RS does create a replacement, but it can only land on the (same,
  unreachable) labelled node, so no overlap in practice. Suggested test: replicas==1 and strategy Recreate
  unless every sidecar passes `--leader-election` and a leases Role/RoleBinding exists.
- Image pinning: sidecar digests match the k8s.io promotion manifest (images.yaml lines for csi-provisioner
  v5.2.0, csi-resizer v1.13.1, csi-snapshotter v8.2.0; registry.k8s.io itself 403 from here). zfs-csi is
  tag-only by the documented TODO (already tested).
- Classes vs controller: StorageClass `reserve: "false"` is accepted (ParamReserve, ParseBool);
  `--extra-create-metadata` keys are `csi.storage.k8s.io/` and skipped (controller.go:65-67, 301).
  VolumeSnapshotClass has no parameters (CreateSnapshot noParams accepts only the k8s prefix; the snapshotter
  runs without --extra-create-metadata). Suggested test: every StorageClass parameter key is one of
  zcsi.ParamSparse/ParamReserve/ParamNFS* or has prefix `csi.storage.k8s.io/`, sparse/reserve values pass
  strconv.ParseBool; VolumeSnapshotClass parameters only carry the prefix.
- Namespace/SA wiring: Deployment ns `zfs-csi`, SA `zfs-csi-controller` in `zfs-csi`, binding subject and
  roleRef match. Suggested test: serviceAccountName == SA name, SA ns == Deployment ns == binding subject ns,
  roleRef.name == ClusterRole name.
- Probes: none, and no livenessprobe sidecar; left to the queued "probe crash-loop" item.
