# wave20: CSI front-end seams (zfs-csi, read-only triage)

Scope: `cmd/zfs-csi`, `internal/csi`, plus the deploy manifests and sidecar contracts they meet.
Skipped as known elsewhere: endpoint guid re-plan, headroom/copies, unixsock leftovers, sim fidelity, CI speed. Also skipped: "volume with snapshots/clones can't be deleted" (README:41, README:148, detached snapshots are on the roadmap).

**No code bug blocks a CSI conformance run.** The translation layer is thin and correct. The endpoint error table already splits retryable from final codes the way the sidecars want (errors.go:120-140). csi-sanity passes in both modes:
- non-NFS mode: 44/44 Controller+Identity specs;
- NFS mode: 44/44, verified this wave on a scratch copy with `TestVolumeParameters={"nfs.clients":"10.0.0.0/24"}` over `nfsFixture`.

The weak points are where the front end meets the sidecars and the node side, not the RPC translation.

**Node RPCs (NodeStage/NodePublish/NodeExpand, mount safety, staging/target path validation, stale mounts): not applicable.** This repo ships no node service. ADR 0004 delegates the node side to csi-driver-nfs. CSIDriver sets `attachRequired: false`, so there is no ControllerPublish. Every node-side question therefore belongs to csi-driver-nfs, and to whether it is deployed at all (F1).

Evidence was gathered on a /dev/shm copy, since deleted. Upstream sources were read at the pinned tags:
- csi-lib-utils `rpc/common.go` (ProbeForever);
- external-provisioner v5.2.0 (`cmd/csi-provisioner/csi-provisioner.go:233-292`, `pkg/controller/controller.go:296`);
- external-snapshotter v8.2.0 (`cmd/csi-snapshotter/main.go:219`);
- external-resizer v1.13.1 (`--timeout` exists, main.go:67).

---

## F1 (HIGH, deploy seam): as shipped, a PVC binds but no pod can mount it, in either mode

- **Where:**
  - `deploy/systemd/zfs-endpoint@.service:50-56`: no `-nfs-server`, so the endpoint runs in non-NFS mode;
  - `deploy/k8s/classes.yaml`: no `nfs.clients`;
  - `deploy/k8s/*`: no csi-driver-nfs node DaemonSet registered as `zfs-endpoint.wommy.github.io`;
  - `internal/csi/controller.go:203-207`: in non-NFS mode, `kindOf` accepts mount access.
- **Trigger:** apply `deploy/` as written and create a PVC from StorageClass `zfs`. CreateVolume succeeds in non-NFS mode and returns `{dataset, kind, mountpoint}`. The pod then hangs in ContainerCreating:
  - with no node plugin: "driver name zfs-endpoint.wommy.github.io not found in the list of registered CSI drivers";
  - with csi-driver-nfs deployed: its NodePublish refuses the missing `server`.
- **If the operator adds `-nfs-server`:** every PVC from the shipped class fails with InvalidArgument `nfs.clients is required`, which is final for the provisioner.
- **Impact:** this is the "PVC binds, pod never starts" class that ADR 0006 refused for block and wave13 Q2 refused for NFS without clients. A non-NFS filesystem volume has no consumer at all.
- **Minimal fix (two parts, owner decision):**
  - (a) Deploy: ship a csi-driver-nfs node DaemonSet (`--drivername=zfs-endpoint.wommy.github.io`, `--nodeid`). Give `-nfs-server` a documented systemd drop-in, and add an `nfs.clients` placeholder to classes.yaml.
  - (b) Code, mirroring `-enable-block`: when the endpoint advertises no NFS server, refuse mount access in `kindOf` with InvalidArgument, unless the operator passes a new `-enable-local-filesystem` flag.
- **Failing test (sim):** `TestMountRefusedWithoutNFS`. Use a `Controller{EP: endpointClient(t)}`, call CreateVolume with `capability(false)`, and expect InvalidArgument. Today it returns OK.
  - Fix (b) breaks today's non-NFS TestSanity, so move sanity to NFS mode (F4), or set the new flag in it.
  - deploy_test should also assert that a node DaemonSet carries the driver name and that the StorageClass carries `nfs.clients`.

## F2 (MEDIUM-HIGH): Probe errors when the endpoint is unreachable, so the provisioner and snapshotter exit and crash-loop

- **Where:** `internal/csi/identity.go:61-66`. A failed `Health.Check` passes through as an error (comment at :59-60).
- **Sidecar contract:** csi-lib-utils `ProbeForever` retries only on `DeadlineExceeded` or `ready=false`. Any other gRPC code returns an error. On that error:
  - the provisioner calls `os.Exit(1)` (csi-provisioner.go:233-237);
  - the snapshotter calls `os.Exit(1)` (main.go:219-222).
- **Verified:** with the endpoint socket absent, `Probe` returns `Unavailable` ("dial unix ...: no such file or directory") at once. `ControllerGetCapabilities` returns `Unavailable` too, and the provisioner hits `klog.Fatalf` on it (csi-provisioner.go:292).
- **Trigger:** a reboot of the hyperconverged host (ADR 0006). kubelet starts the controller pod before `zfs-endpoint@` is listening: the pool import plus `-parent-wait 60s` lands in the window. Restarting the endpoint unit while the pod restarts has the same effect.
- **Impact:** the sidecars go into CrashLoopBackOff, which backs off up to 5 minutes. Provisioning, snapshots and resizes stall for that long after the endpoint is back. The pod also logs crash noise during routine reboots.
  - The NOT_SERVING path already does the right thing: it returns ready=false and the sidecars wait.
- **Minimal fix:** in `Probe`, map a Check error with code `Unavailable` (or `DeadlineExceeded`) to `Ready=false` with a nil error. Optionally build `ControllerGetCapabilities` from a static list: the endpoint's list is static (server.go:117-133), and that removes the second fatal path.
- **Failing test (no ZFS needed):** `TestProbeEndpointUnreachableIsNotReady`. Use `grpc.NewClient("unix:///nonexistent/endpoint.sock", insecure)` and `Identity{Health: healthpb.NewHealthClient(conn)}`. Assert `err == nil && !ready`. Today it fails with Unavailable.

## F3 (MEDIUM): ControllerExpandVolume applies VVC's NFS-export and access-mode rules, so it refuses resizes of volumes in use

- **Where:** `internal/csi/controller.go:591-605` calls `capSupported`, whose `:252-255` checks the recorded `nfs_server` and whose `:227-229` checks `mode.supports`.
- **Verified on the sim, case 1:** a filesystem volume created before NFS mode, then the endpoint restarted with `-nfs-server`. Expand with a mount capability returns InvalidArgument: "the volume was not created with an NFS export: csi-driver-nfs cannot mount it". The same request without a capability succeeds.
  - csi-resizer always sends the PV's capability.
  - The same refusal applies to adopted democratic-csi volumes. They have no `<prefix>:nfs_server`, and their `sharenfs` is foreign, so `volumeView` (endpoint.go:503) gives them no server.
- **Verified on the sim, case 2:** an RWX volume, then the endpoint restarted without `-nfs-server` (a mode flip, e.g. a lost drop-in). Expand returns InvalidArgument: "access mode MULTI_NODE_MULTI_WRITER is not supported".
- **Impact:** InvalidArgument is final for csi-resizer, and with RecoverVolumeExpansionFailure it is reported as infeasible. The PVC is stuck with a resize error on a volume that is mounted and working. Whether the node side can still mount a volume has no bearing on growing its refquota.
- **Minimal fix:** in Expand, keep only the access-type checks: block requested on a filesystem, block disabled, missing access type. Drop the NFS-export, read-only-export and access-mode checks.
- **Failing test (sim):** `TestNFS_ExpandPreNFSVolume`.
  - Call `nfsFixture(t)`, then `create(nfsReq("old", nil))`, then `restart(t, srv1)`.
  - Expand with `mountCap(snw, "")` and expect OK and 2 GiB.
  - Add a second case that creates RWX in NFS mode, restarts with no servers, expands with `mountCap(mnmw, "")`, and expects OK.

## F4 (MEDIUM, coverage): csi-sanity never runs in NFS mode, the only mode with a node side

- **Where:** `internal/csi/sanity_test.go:59-80`. Both runs, `mount` and `block`, use `endpointConn`, which has no `NFSServers`. NFS behaviour is covered only by the hand-written `nfs_test.go` N1-N16.
- **Verified:** wiring `nfsFixture(t, srv1)` and `cfg.TestVolumeParameters = {"nfs.clients": "10.0.0.0/24"}` behind an env var passes 44/44 in about 0.3s on the sim. Nothing fails today, so this is cheap insurance for the production path.
- **Constraint:** Ginkgo runs once per process, so this needs an env-gated mode (`CSI_SANITY_NFS=1`) as a second `go test` invocation in CI, or a sibling package.
- **Test to add:** the env-gated variant described here, plus the same on the real-ZFS VM, where etab and nfsd are real.

## F5 (LOW): a filesystem snapshot's size_bytes follows the source's current refquota, not its size when the snapshot was taken

- **Where:** endpoint `snapshotView` (endpoint.go:521-526), surfaced unchanged by `snapshotOut` (controller.go:427-435).
- **Verified on the sim:** take a 1 GiB volume, snapshot it, then expand it to 4 GiB.
  - ListSnapshots now reports 4 GiB.
  - An idempotent CreateSnapshot repeat also returns 4 GiB, though the first call returned 1 GiB.
  - Restoring at 1 GiB still works.
- **Trigger:** a pre-provisioned VolumeSnapshotContent import, which reads ListSnapshots, or a snapshotter retry after a resize. `restoreSize` becomes 4 GiB, and Kubernetes then refuses a restore PVC smaller than that, even though the data fits in 1 GiB.
- **Minimal fix:** record the capacity at snapshot time as a marker property on the snapshot, written in the same `zfs snapshot -o`, and report that value.
- **Failing test (sim):** `TestSnapshotSizeStable`. Run create, snapshot, expand, then ListSnapshots, and expect `size_bytes == 1<<30`.

## F6 (LOW, UX): `nfs.clients` is split on "," without trimming

- **Where:** `controller.go:320`.
- **Verified:** `"10.0.0.0/24, 10.0.1.0/24"` gets InvalidArgument from the endpoint: `invalid NFS client " 10.0.1.0/24"`. The error is safe and visible but surprising, and a comma followed by a space is the natural way to write the list in YAML.
- **Fix:** `strings.TrimSpace` each entry. Empty entries still reach the endpoint and are refused, as intended.
- **Test:** add a case to `TestNFS_N2_ClientsValidated` that expects OK and both networks to be exported.

## Nits (no action needed unless convenient)

- **Shutdown drain:** zfs-csi's `-drain 30s` (main.go:112) equals the pod's default `terminationGracePeriodSeconds` (30s), so the drain timeout can never fire before SIGKILL. Either set the pod's grace period to about 45s or lower `-drain`. The sidecars die on the same SIGTERM anyway, which cancels in-flight calls, and the endpoint's crash convergence covers that.
- **No per-call logging:** zfs-csi logs nothing per call. Failure text reaches users only through sidecar events. A unary interceptor that logs method, code and duration as slog JSON would match ADR 0006's "journald first".
- **Stale capabilities:** sidecars read ControllerGetCapabilities once at startup. Since the mode (NFS or not) is decided per call by the endpoint, a mode flip only shows up in per-call refusals (see F3), never in the advertised capabilities. That is acceptable.

## Checked and holding (no finding)

- **Error codes:**
  - Endpoint codes pass through 1:1 (server.go:37).
  - Busy or interrupted destroys map to Unavailable or Aborted, and an unclassified failure after a change maps to Aborted, so a half-applied plan is never stranded by a final code.
  - A failed parent read maps to Unavailable, not NotFound.
  - These match the provisioner's in-progress set (Canceled, DeadlineExceeded, Unavailable, ResourceExhausted, Aborted).
- **Deadlines:** the sidecar deadline (60s) propagates through gRPC to the endpoint, which kills the zfs command and returns DeadlineExceeded (non-final). The endpoint's lock-wait (30s) sits under the sidecar timeout.
- **Idempotency:**
  - CreateVolume and CreateSnapshot are keyed on the stable PV and snapshot names. A size mismatch returns ALREADY_EXISTS (sanity).
  - A different explicit `nfs.server` on a committed volume returns ALREADY_EXISTS (N9). An uncommitted create converges on retry (N9b).
  - Delete of a missing volume or snapshot returns OK (sanity).
  - An empty content-source id is refused before reaching the endpoint (controller.go:471-483).
- **Snapshots:** `ready_to_use` is always true, which is correct for ZFS. CreationTime is set.
- **Parameters:** `csi.storage.k8s.io/` keys from `--extra-create-metadata` are skipped. Unknown keys are refused rather than dropped.
- **Expansion:**
  - NodeExpansionRequired is false for filesystems; csi-driver-nfs has no NodeExpand.
  - The plugin capability is ONLINE expansion.
  - An unlimited filesystem reports the requested capacity, never 0.
- **Access modes:** RWOP is not advertised (no SINGLE_NODE_MULTI_WRITER). All five pre-1.5 modes are accepted in NFS mode.
- **Topology and capacity:** none is advertised, which is correct for NFS. GetCapacity ignores parameters, which is harmless with `--enable-capacity` off.

## Ranked summary

| # | Sev | One line |
|---|-----|----------|
| F1 | High (deploy) | The shipped manifests give no usable mount in either mode. Ship the csi-driver-nfs DaemonSet, `-nfs-server` and `nfs.clients`, and gate non-NFS mount access behind a flag |
| F2 | Med-High | Probe returns Unavailable when the endpoint is down, so the provisioner and snapshotter `os.Exit(1)` and crash-loop after a reboot. Return ready=false instead |
| F3 | Medium | Expand refuses volumes with no recorded `nfs_server` and modes after a flip, and InvalidArgument is final for the resizer. Check only the access type |
| F4 | Medium (coverage) | Add the NFS-mode csi-sanity run: it passes 44/44 on the sim today |
| F5 | Low | Snapshot size drifts with source expansion and inflates restoreSize |
| F6 | Low | Trim spaces in `nfs.clients` |
