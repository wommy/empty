# 8. Controller pod: pinned by a node-restriction label, endpoint socket read-only, no token in zfs-csi

Status: accepted (decisive, reversible) · Date: 2026-10-27 · Related: ADR 0006 (first deployment), ADR 0001 (transport auth), `deploy/k8s/`.

## Context

- **Pinning.** The controller pod follows a nodeSelector to the ZFS host (ADR 0006). It was `zfs-endpoint.wommy.github.io/host`, a label NodeRestriction lets a kubelet set on its own Node. A compromised node could label itself, create the socket directory (the `type: Directory` hostPath check then passes), serve a fake endpoint, and receive the pod and its service account token on the next reschedule. A second labelled node does the same without an attacker: zfs-csi never checks which pool it talks to.
- **Socket directory.** zfs-csi runs as uid 0 with the endpoint's socket directory, on the host's `/run` tmpfs, mounted read-write. It only dials the socket. On Linux, connecting to a unix socket needs write permission on the socket inode (unix(7)); the read-only check (EROFS) covers regular files, directories and symlinks, not sockets. Checked on Linux 6.18: `connect()` through a `ro` bind mount succeeds while `touch` there fails with EROFS.
- **Token.** Automounting put the service account token (cluster-wide PV, PVC and snapshot writes) into all four containers, including zfs-csi, which calls no Kubernetes API and is the one with a host mount.

## Decision

- Pin with `node-restriction.kubernetes.io/zfs-endpoint-host=true`. NodeRestriction refuses that prefix to kubelets; only a cluster admin sets it. Label exactly one node.
- Mount the endpoint socket directory `readOnly: true` in zfs-csi.
- `automountServiceAccountToken: false` on the pod; the three sidecars mount a projected volume (bound token, `kube-root-ca.crt`, namespace) at `/var/run/secrets/kubernetes.io/serviceaccount`, what kubelet would mount for them.
- `deploy/k8s_test.go` pins all three (`TestNodePinLabel`, `TestPodHardening`).

## Consequences

- Upgrading an existing cluster: label the host with the new key before applying, or the pod stays Pending. The old label can then be removed.
- A compromised zfs-csi container can no longer unlink the endpoint socket, plant entries the endpoint's leftover cleanup inspects, or fill the host's `/run`; it can still send any call the endpoint's controller role accepts.
- Still open: zfs-csi does not check the endpoint's pool identity at startup; the label is the only guard against a wrong backend.
- Reversal is a manifest change; nothing outside `deploy/k8s` depends on it.

## Addendum: endpoint health as readiness, not liveness

The sidecars call Probe only at startup, so an endpoint that turns NOT_SERVING later (suspended or exported pool, every zfs slot held by a hung zfs, a drain) was invisible from Kubernetes. The controller pod now runs the upstream livenessprobe sidecar (the version and digest `node.yaml` pins, hardened like the other sidecars and given no token), serving Probe on the pod IP at `:29664/healthz` (not `node.yaml`'s host-network 29663), and zfs-csi has a `readinessProbe` on it: 10 s period, 5 s timeout, 3 failures, so an endpoint restart of a few seconds passes unseen and a stuck endpoint shows as NotReady within 30 s and for as long as it lasts. It is deliberately not a `livenessProbe`. Probe cannot tell a wedged zfs-csi from a sick endpoint, restarting zfs-csi cannot fix the endpoint, and csi-provisioner and csi-snapshotter exit when the CSI socket goes away, so a long pool outage would become a restart loop of all five containers that hides the cause behind CrashLoopBackOff. No Service selects the pod, so readiness changes no traffic; it only reports, through `kubectl get pod`, the pod's Ready condition and any alert on it. `deploy/k8s_test.go` `TestControllerHealthProbe` pins the sidecar, the port, the readiness window and the absence of a liveness probe. If a zfs-csi hang that a restart would fix is ever seen, add a liveness check that does not call the endpoint, rather than reusing this one.
