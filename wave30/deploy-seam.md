# Deploy seam review: PVC -> mounted volume (wave30)

Repo HEAD bb66dc8. Read-only. Upstream sources fetched at exact tags into
`scratchpad/wave30/up/` (csi-driver-nfs v4.13.6, node-driver-registrar v2.17.0,
external-provisioner v5.2.0, external-resizer v1.13.1, external-snapshotter v8.2.0,
livenessprobe v2.19.0). `go test ./deploy/ ./cmd/zfs-csi/` passes.

## Seam trace (all verified unless marked)

| Seam | Evidence | Result |
|---|---|---|
| systemd ExecStart flags | service:50-57 vs cmd/zfs-endpoint/main.go:87-105 (`-parent -listen -admin-listen -drain -stop-wait -parent-wait -nfs-server`) | all exist |
| reconcile unit flags | reconcile@.service `-parent -timeout` vs reconcile.go:46,60; dispatch main.go:79 | exist |
| zfs-csi args | controller.yaml `-parent -csi-address` vs cmd/zfs-csi/main.go:54,56 | exist |
| provisioner v5.2.0 | `--csi-address --timeout --extra-create-metadata`: upstream csi-provisioner.go:80,89,96 | exist |
| resizer v1.13.1 | `--handle-volume-inuse-error`: upstream main.go:87 | exists |
| snapshotter v8.2.0 | `--csi-address --timeout`: upstream main.go:68,74 | exist |
| livenessprobe v2.19.0 | `--probe-timeout --http-endpoint`: upstream main.go:46,50 | exist |
| nfsplugin v4.13.6 | `--drivername --nodeid --endpoint --v`: upstream cmd/nfsplugin/main.go:29-32 (klog for --v) | exist |
| registrar v2.17.0 | `--csi-address --kubelet-registration-path`: upstream main.go:51,53; KUBE_NODE_NAME env (dropped vs upstream) not read by v2.17.0 | ok |
| driver name | identity.go:26 = csidriver.yaml:6 = classes.yaml provisioner/driver = node.yaml `--drivername` and plugin dir | match |
| SC params -> parse | classes.yaml `nfs.clients`, `reserve` -> controller.go:309-361; `csi.storage.k8s.io/*` from --extra-create-metadata skipped (:316) | ok (placeholder refused by design) |
| params -> endpoint | controller.go:483-486 `NfsExport{Clients, ReadOnly, Server, Uid, Gid, Mode}` | ok |
| endpoint -> volume_context | endpoint.go:512 `Mountpoint = r.Mount`; controller.go:411-412 `server`, `share`=mountpoint | ok |
| volume_context -> NodePublish | upstream nodeserver.go:85-134 lower-cases keys, needs `server`+`share`, source `server:share`; our extra keys `dataset kind mountpoint` hit no case | match |
| share == export path | nfs_test.go:229,254 pins share == mountpoint and etab serves it (simulator) | ok, see D3 for symlinks |
| node capabilities | nfs plugin has no STAGE_UNSTAGE (upstream nfs.go:128-132) -> kubelet goes straight to NodePublish; CSIDriver attachRequired:false | ok |
| expand | controller.go:640 NodeExpansionRequired false for filesystems | ok |
| socket paths | unit `-listen unix:///run/zfs-endpoint/sock/%i/endpoint.sock` = unixsock.EndpointAddr (unixsock.go:325) = controller.yaml hostPath/mountPath `/run/zfs-endpoint/sock/tank-k8s-vols`; dir not recreated on restart (only the socket) | match |
| socket perms | 0600 root in 0700 root dir; pod `runAsUser: 0` (controller.yaml:69) with caps dropped: owner match, no DAC cap needed; ro mount fine for connect() | ok (see D4 SELinux) |
| node sockets | socket-dir hostPath `/var/lib/kubelet/plugins/zfs-endpoint.wommy.github.io` = `--kubelet-registration-path` dir | match |
| RBAC | rbac.yaml vs upstream provisioner v5.2.0 and snapshotter v8.2.0 ClusterRoles fetched: provisioner identical; snapshotter minus groupsnapshot (gate off); resizer covered (pvc/status patch, PV patch, VAC); pods/leases dropped matching flags | covers |
| NFS version | SC has no mountOptions, so mount.nfs negotiates (v4.2 first); nfs-kernel-server defaults serve v3+v4. `nfs.conf.example` is the endpoint's `-nfs-server` drop-in, not nfs-server config: nothing in repo pins a version either side | consistent |
| client CIDR | node plugin is hostNetwork (node.yaml:523), so the source is the node IP; operator must write the node subnet (`<node-subnet>/24` placeholder refused, TestStorageClassClientsPlaceholder) | ok by design |

## Blocking (an operator following README + deploy/ exactly does not get a pod)

**B1. The zfs-csi image does not exist and nothing in the repo builds one.**
controller.yaml:278 `image: ghcr.io/wommy/zfs-csi:v0.1.0`; its own TODO (:276-277) and
README:18 ("No image or release build yet"). The only instruction is `go build ...
./cmd/zfs-csi` (controller.yaml:21); there is no Dockerfile/Containerfile/ko config
(`git ls-files` shows none). Following the docs exactly gives ImagePullBackOff, so no
CreateVolume ever runs.
Fix: ship a Dockerfile (static `CGO_ENABLED=0`, distroless/static, `USER 0`) plus a
release workflow that pushes the tag and pins the digest; until then change the doc to
"build with this Dockerfile, push, and set image:". Test: a CI job that runs
`docker build` and a deploy_test assert that the image reference is reproducible from a
file in the repo (e.g. a Dockerfile exists and its binary path matches the container's
default entrypoint).

**B2. zfs-endpoint itself is never installed by the docs.** service:50 runs
`/usr/local/sbin/zfs-endpoint`; README and deploy/ give no build/install step for it, no
step that copies the units to `/etc/systemd/system`, and no `daemon-reload`. Only
nfs.conf.example's header (`Install as /etc/systemd/system/...`) says where one file goes.
An operator going strictly by the text has no binary at that path (unit fails 203/EXEC).
Fix: a README "Install" section: `go build -ldflags ... -o /usr/local/sbin/zfs-endpoint
./cmd/zfs-endpoint`, `install -m644 deploy/systemd/* /etc/systemd/system/`, drop-ins,
`daemon-reload`, `apt install nfs-kernel-server`, then the k8s steps. Test: a doc test
that the README names every path the units reference (ExecStart binary, drop-in dir).
(Borderline: an experienced operator infers it; "exactly" does not.)

## Degrading (mounts, but not usefully / fails under common conditions)

**D1. Shipped class gives a volume no pod can write.** classes.yaml sets no
`nfs.uid/gid/mode`; the root is then root:root 0755 (README:17; endpoint.go:360-366
"under root_squash, a root:root 0755 root is writable by no client"). sharenfs is
`sec=sys,rw=...` with default root_squash (export.go:201-205). CSIDriver sets no
`fsGroupPolicy` (csidriver.yaml), so it defaults to ReadWriteOnceWithFSType and the PV
has no fsType (provisioner `--default-fstype` default "": upstream csi-provisioner.go:106)
-> kubelet applies no fsGroup. Result: the pod starts, the volume is mounted, every write
fails EACCES, root included (squashed to nobody). (Not setting `fsGroupPolicy: File` is
correct: kubelet's recursive chown would fail under root_squash and fail the mount.)
Fix: ship the class with `nfs.mode: "2777"` or `nfs.uid`/`nfs.gid` placeholders that do
not parse (same pattern as nfs.clients), or make the controller refuse NFS mode without
an owner/mode. Test: classes_test asserting the shipped class sets nfs.mode or nfs.uid
(or a placeholder the parser refuses), like TestStorageClassClientsPlaceholder.

**D2. Apply order: `kubectl apply -f deploy/k8s/` fails on first run.** Files apply
alphabetically: classes, controller, csidriver, node, rbac; the Namespace `zfs-csi` is
only in rbac.yaml:1-9, so controller.yaml and node.yaml fail "namespaces zfs-csi not
found". classes.yaml's VolumeSnapshotClass also fails without the snapshot CRDs
(classes.yaml:34 notes they are a prerequisite but kubectl still errors). README gives no
order. A second apply succeeds. Fix: rename to `00-namespace-rbac.yaml` or document
`kubectl apply -f rbac.yaml` first; move VolumeSnapshotClass to its own file. Test:
deploy_test asserting the lexically first file defines the Namespace every other
namespaced object uses.

**D3. `share` is the unresolved mountpoint; export verification is at the real path.**
endpoint.go:512 hands `r.Mount`; Publish verifies at `realPath(row.Mount)`
(endpoint.go:712,1297; export.go:577-583). If any component of the parent's mountpoint
is a symlink, etab and the check agree, but an NFSv4 client walks `share` through the
server's pseudo-fs, which holds only real paths, so the mount fails ENOENT (reasoned
from nfsd pseudo-root behaviour, not reproduced). Fix: return `realPath(r.Mount)` as
`share` (or refuse a parent under a symlink at startup, as for crossmnt). Test: endpoint
test with a symlinked parent mountpoint asserting share == EvalSymlinks(mountpoint).

**D4. Environment-dependent, unverified:** (a) SELinux enforcing hosts: the unprivileged
controller (container_t) connecting to a host socket labelled var_run_t is normally
denied; no seLinuxOptions/doc. (b) `priorityClassName: system-cluster-critical`
(controller.yaml:265) outside kube-system is refused on clusters that quota critical
priorities (e.g. GKE). (c) hardening.conf is only exercised by ci/real-zfs-unit.sh
without the NFS drop-in and without an export, so the shipped NFS-mode unit +
hardening has never run an export. (d) Non-standard kubelet root (k3s, microk8s) breaks
node.yaml's `/var/lib/kubelet` paths (same as upstream).

## Cosmetic

- C1. `nfs.conf.example` shares its name with nfs-utils' `/etc/nfs.conf`; the brief
  itself read it as nfs-server config. Rename to `nfs-mode.conf.example`.
- C2. RWOP PVCs fail: zfs-csi does not advertise SINGLE_NODE_MULTI_WRITER, so
  external-provisioner refuses ReadWriteOncePod. Document or advertise it.
- C3. No end-to-end test exists anywhere (no kind/VM job runs NodePublish of a real
  CreateVolume output through csi-driver-nfs; grep for NodePublish/mount.nfs finds only
  a comment). Every seam above is pinned piecewise; the composition is not.

## A-deploy-cannot-mount (ADR 0009)

Mechanism closed. Before: unit without `-nfs-server`, no node plugin, PVC bound and pods
hung (ADR 0009 Context). Now: zfs-csi refuses mount access outside NFS mode
(controller.go:42-45, kindOf; `-enable-local-filesystem` default off, main.go:60); unit
ExecStart ends `$ZFS_ENDPOINT_NFS` (service:57) with nfs.conf.example setting
`-nfs-server`; node.yaml runs nfsplugin v4.13.6 under this driver name; volume_context
carries exactly the `server`/`share` keys upstream NodePublish requires
(nodeserver.go:87-131). The "binds then hangs" path is gone: every misconfiguration now
fails visibly at provisioning. Not demonstrated end to end (C3), and B1 still stops
the documented deploy before provisioning, so I would close the anomaly as "fixed by
design, unverified by an e2e run" rather than "verified".

## Verdict

The wiring is right. Every flag in the manifests and units exists in the pinned
binaries. Driver names, socket paths, uids and RBAC line up. The volume_context
matches what csi-driver-nfs v4.13.6 NodePublish reads key for key. So once the pieces
exist, a PVC from a correctly edited class becomes an NFS mount in the pod. But an
operator following README + deploy/ exactly does not get there. There is no zfs-csi
image and nothing to build one (B1), and no install step for the endpoint binary or
units (B2). A first `kubectl apply -f deploy/k8s/` also fails on namespace order (D2).
Once those are worked around, the shipped StorageClass gives a mounted volume that no
pod can write, because of root_squash on a root:root 0755 root and no fsGroup (D1).
Fix B1, B2 and D1, and add one kind+NFS e2e job (C3), before calling the deploy
working.
