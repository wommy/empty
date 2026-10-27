# wave24: CSI re-check against HEAD 286cf47 (after af1d71c, ADR 0008)

Read-only re-check of the two queued writer tasks from `wave20/csi-seams.md` (F1-F6 + nits). Line numbers are at HEAD `286cf47`.

## Status table

| Item (queue) | wave20 | Now | Brief |
|---|---|---|---|
| Probe Unavailable → crash-loop (q-csi-code) | F2 | **Holds, narrower trigger.** Cold boot is now gated by the `type: Directory` hostPath; endpoint restart/crash overlapping any container start still crash-loops | C1 |
| Expand checks more than the access type (q-csi-code) | F3 | **Holds, and is now reachable.** Before af1d71c the resizer never synced (no pods RBAC), so no expand ever ran; it does now | C2 |
| `nfs.clients` not trimmed (q-csi-code) | F6 | Holds unchanged | C3 |
| Snapshot size follows the source (q-csi-code) | F5 | Holds unchanged | C4 |
| Mount access without NFS (+ ADR) (q-csi-code) | F1(b) | Holds unchanged | C5 |
| Drain vs grace (q-csi-code) | nit | **FIXED** in af1d71c: `controller.yaml:64-66` `terminationGracePeriodSeconds: 45` > `-drain 30s` (`cmd/zfs-csi/main.go:45`); pinned by `deploy/k8s_test.go` `TestPodHardening` (reads the flag default from main.go). Drop it | - |
| csi-driver-nfs node DaemonSet, `-nfs-server` drop-in, `nfs.clients` placeholder (q-csi-deploy) | F1(a) | **Holds.** Nothing node-side or NFS was added; af1d71c only hardened the controller. The PSA `privileged` label it added (`rbac.yaml:16-25`) is what the node DaemonSet needs anyway | D1-D3 |
| csi-sanity in NFS mode, `CSI_SANITY_NFS=1` (q-csi-deploy) | F4 | Holds. Simulator: PR tier. Real pool: nightly first (see CI section) | D4 |

Suggested order: C1, C2, C3 (independent, small) → C5 + ADR 0009 with D1-D3 (they decide the shipped mode together) → D4 → C4.

---

## C1. Probe and ControllerGetCapabilities: endpoint unreachable is "not ready", not an error

- **Where:**
  - `internal/csi/identity.go:61-66`: a failed `Health.Check` is returned as is (the doc comment at :59-60 says so).
  - `internal/csi/controller.go:91-95`: `ControllerGetCapabilities` passes the endpoint's error through.
- **What happens now:** if the endpoint socket is absent, `grpc.NewClient` is non-blocking, so `Check` fails at once with `Unavailable` ("dial unix …: no such file or directory").
  - csi-lib-utils `ProbeForever` retries only on `DeadlineExceeded` or `ready=false`. On any other code, csi-provisioner v5.2.0 and csi-snapshotter v8.2.0 call `os.Exit(1)`.
  - A `ControllerGetCapabilities` error is `klog.Fatalf` in the provisioner, the snapshotter and the resizer. The resizer reads it at startup to decide whether it supports resize.
- **What changed since wave20:**
  - The hostPath is `type: Directory` (`controller.yaml:173-178`). zfs-endpoint creates the socket directory only when it listens, after `-parent-wait` (`cmd/zfs-endpoint/main.go:201`, then `:233` `listenCSI` → `unixsock.Listen` → `SecureDir`), and `/run` is a tmpfs.
  - So on a cold reboot the pod stays in ContainerCreating (FailedMount, which kubelet retries) until the endpoint serves. The containers never start, so they never crash-loop. The original reboot trigger is mostly gone.
  - The directory survives a stop (`RuntimeDirectoryPreserve=yes`; `listener.Close` removes only the socket, `unixsock.go:139-145`). So from the second endpoint start of a boot onward, the gate is open while the socket is absent.
- **Remaining triggers.** Any container start while the endpoint is down after it first ran:
  - `systemctl restart` or an upgrade of `zfs-endpoint@`;
  - a crash in `Restart=on-failure` backoff (up to 60s);
  - exit 78, permanent;
  - overlapping with a controller rollout (`Recreate`), an eviction or drain, or a sidecar OOM kill. The 256Mi limits added in af1d71c make an OOM kill newly possible.
  - Each sidecar then goes into CrashLoopBackOff, up to 5 minutes, and provisioning, snapshots and expands stall that long after the endpoint is back.
- **Spec and k8s:** CSI spec, `ProbeResponse.ready`, case 2: "The plugin is still initializing, but is otherwise perfectly healthy. In this case a successful response SHALL be returned with a readiness value of `false`." zfs-csi itself is healthy: restarting it cannot bring the endpoint back. The sidecar contract (`ProbeForever`) treats any other code as fatal.
- **Minimal fix:**
  - (1) In `Probe`, when `status.Code(err)` is `Unavailable` or `DeadlineExceeded`, return `Ready=false` with a nil error. Other codes still pass through. Update the doc comment.
  - (2) Make `ControllerGetCapabilities` static: the endpoint's list is constant (`internal/server/server.go:117-128`). `FILE_EXPORT` and `nfs_servers` feed no CSI capability. This removes the second fatal path, which (1) alone does not cover for the resizer.
  - Add one line in the README or the doc comment: sidecars read capabilities once, at startup.
- **Failing-first tests** (no ZFS, in `identity_test.go` and `controller_test.go`):
  - `TestProbeEndpointUnreachableIsNotReady`:
    - `conn, _ := grpc.NewClient("unix:///nonexistent/endpoint.sock", insecure)`, `Identity{Health: healthpb.NewHealthClient(conn)}`;
    - want `err == nil && !ready`. Today: `Unavailable`.
  - `TestControllerCapabilitiesWithoutEndpoint`: `Controller{EP: pb.NewEndpointServiceClient(sameConn)}`; want the same capability set `TestCapabilitiesAdvertised` asserts, and a nil error. Today: `Unavailable`.
  - Keep `TestProbeFollowsEndpointHealth` green: NOT_SERVING is still ready=false.
- **Effort:** S, about 45 minutes.

## C2. ControllerExpandVolume: check only the access type

- **Where:** `internal/csi/controller.go:591-605`. When `volume_capability` is set, it calls `capSupported` (`:225-263`). That applies the access-mode rule (`:227-229`), the NFS-export rule (`:252-255`), the read-only export rule (`:256-258`) and the block-disabled rule (`:235-237`).
- **What happens now** (verified on the simulator in wave20; the code is unchanged):
  - a filesystem created before `-nfs-server`, or adopted from democratic-csi, then expanded in NFS mode → `InvalidArgument` "the volume was not created with an NFS export";
  - an RWX volume after the endpoint restarts without `-nfs-server` → `InvalidArgument` "access mode MULTI_NODE_MULTI_WRITER is not supported".
  - csi-resizer always sends the PV's capability.
- **New since wave20:** af1d71c made the resizer work (`--handle-volume-inuse-error=false`, `controller.yaml:132-136`). Before that its pod informer never synced and no PVC was expanded. This path is now live in the shipped manifests.
- **Spec and k8s:**
  - CSI spec, `ControllerExpandVolumeRequest.volume_capability`: "This allows SP to determine if volume is being used as a block device or mounted file system." Its purpose is block vs mount, nothing more. The RPC's error table has no capability-mismatch case.
  - external-resizer treats `InvalidArgument` as final: it is not one of Canceled, DeadlineExceeded, Unavailable, ResourceExhausted or Aborted. With RecoverVolumeExpansionFailure, the PVC is marked `ControllerResizeInfeasible`, on a volume that is mounted and working.
- **Minimal fix:** replace the `capSupported` call in Expand with an access-type-only check:
  - missing access type → InvalidArgument;
  - block requested on a FILESYSTEM volume → InvalidArgument, since it is impossible.
  - Nothing else: no mode, no NFS export, no read-only export, no `EnableBlock` check. The last one is a deployment flag like the others: a zvol made while block was enabled must still grow.
  - Keep the `GetVolume` before the resize. Drop the `modeOf` call.
- **Failing-first tests:**
  - `TestNFS_ExpandPreNFSVolume` (`nfs_test.go`):
    - `f := nfsFixture(t)` (no servers), then `f.create(nfsReq("old", nil))`, then `f.restart(t, srv1)`;
    - Expand with `mountCap(snw, "")` to 2 GiB → want OK and `CapacityBytes == 2<<30`. Today: InvalidArgument.
  - Second case, a mode flip: `nfsFixture(t, srv1)`, create with `mountCap(mnmw, "")` and `nfs.clients`, `f.restart(t)` with no servers, Expand with `mountCap(mnmw, "")` → want OK.
  - Edit `TestExpandValidatesCapability` (`controller_test.go:251-289`):
    - the `"unsupported access mode"` case becomes an OK case (capacity 2 GiB);
    - `"block access on a filesystem volume"` and `"no access type"` stay InvalidArgument;
    - the "resized by a refused expand" assertion stays.
- **Effort:** S, about 45 minutes, including the test edit.

## C3. Trim `nfs.clients` entries

- **Where:** `internal/csi/controller.go:318-320`: `strings.Split(v, ",")`, with no trimming ("split as written").
- **What happens now:** `"10.0.0.0/24, 10.0.1.0/24"`, the natural YAML spelling, gets a final `InvalidArgument` "invalid NFS client \" 10.0.1.0/24\"" from the endpoint. The error is safe and visible, but it is surprising.
- **Spec and k8s:** no spec violation; StorageClass parameters are opaque strings. This is a UX fix. Each `ProvisioningFailed` is final, and the class has to be recreated, because StorageClass parameters are immutable.
- **Minimal fix:** `strings.TrimSpace` each entry after the split. An empty entry (`"a,"` or `"a, ,b"`) still reaches the endpoint and is still refused. Keep the comment's point about that.
- **Failing-first test:** in `TestNFS_N2_ClientsValidated` (`nfs_test.go:160`), add `"10.20.0.0/24, 10.20.1.0/24"` to the good list. Bump the `len(got) != 4` count to 5, and assert that both networks are exported.
- **Effort:** XS, about 15 minutes.

## C4. Record a filesystem snapshot's size when it is taken

- **Where:**
  - `internal/endpoint/endpoint.go:513-528` `snapshotView`: for a filesystem snapshot (`Volsize == 0`), `SizeBytes` is the source's current `Capacity()`, its refquota;
  - `internal/csi/controller.go:427-435` `snapshotOut` passes it through;
  - the plan is `internal/endpoint/plan.go:996-1000` (`OpSnapshot` with `Props`);
  - the scan columns are `internal/endpoint/table.go:345-349`.
- **What happens now:** take a 1 GiB snapshot, then expand the source to 4 GiB. ListSnapshots, and an idempotent CreateSnapshot retry, now report 4 GiB, although the first CreateSnapshot returned 1 GiB.
- **Spec and k8s:**
  - CSI spec, `Snapshot.size_bytes`: "the complete size of the snapshot in bytes … the minimum size of volume required to rehydrate from this snapshot." It must be the size at the time the snapshot was taken.
  - CreateSnapshot idempotency requires the same response for the same request.
  - Kubernetes refuses a restore PVC smaller than `restoreSize`, so an import (pre-provisioned VolumeSnapshotContent) or a snapshotter retry after a resize inflates every restore.
- **Minimal fix:**
  - Add `props.snapshotSize()` = `<prefix>:snapshot_size`. Set it in the CreateSnapshot plan's `Props` (written with the same `zfs snapshot -o`) to `src.Capacity()`.
  - Read it in the scan as a new column.
  - `snapshotView` prefers it. It falls back to the source's capacity for legacy snapshots without the property, which leaves today's behaviour for them.
  - Zvol snapshots keep `Volsize`.
  - The simulator already takes `-o` on snapshot (`clone_source_for` uses it).
  - Regenerate `testdata/plans.golden`. Check `props_roundtrip_test.go` and the prefix-rename paths (`checkLegacyPrefix`) for property lists that must include the new one. The value is a user property, so it is still ZFS-as-source-of-truth (ADR 0003).
- **Failing-first test:** `TestSnapshotSizeStable` (`internal/csi/controller_test.go`, simulator or real pool):
  - create a 1 GiB filesystem, CreateSnapshot, expand it to 4 GiB;
  - want ListSnapshots `size_bytes == 1<<30`, and the CreateSnapshot retry also `1<<30`. Today: 4 GiB.
  - Also add one planner golden row showing the prop on `OpSnapshot`.
- **Effort:** M, 2-3 hours, mostly the golden plans and the prop lists.

## C5. Refuse mount access when the endpoint advertises no NFS server, unless `-enable-local-filesystem` (needs ADR 0009)

- **Where:**
  - `internal/csi/controller.go:203-207`: `kindOf` accepts mount in non-NFS mode;
  - `:241-248`: `capSupported` confirms it;
  - flags at `cmd/zfs-csi/main.go:46` (next to `-enable-block`).
- **What happens now:** with no `-nfs-server` (the shipped unit, `deploy/systemd/zfs-endpoint@.service:50-56`), CreateVolume returns `{dataset, kind, mountpoint}`. The PVC binds, and every pod using it hangs in ContainerCreating:
  - with no node plugin registered for the driver name: "driver name … not found in the list of registered CSI drivers";
  - with csi-driver-nfs: it refuses the missing `server`.
  - No consumer of a non-NFS filesystem volume exists in this repo or upstream.
- **Spec and k8s:**
  - CSI CreateVolume must fail with `INVALID_ARGUMENT` (or `OUT_OF_RANGE`/`UNSUPPORTED`) when the plugin cannot satisfy the requested capability. It must not return a volume that no Node service can publish.
  - In Kubernetes, the binding succeeds and then the kubelet's mount never does: the "PVC binds, pod never starts" class that ADR 0006 already refused for block, and ADR 0004 Q2 for NFS without clients.
- **Minimal fix:**
  - `Controller.EnableLocalFilesystem bool` and the flag `-enable-local-filesystem` (default off).
  - When off and `!m.nfs`:
    - `kindOf` returns `InvalidArgument` for mount access, with a message naming `zfs-endpoint -nfs-server` and the flag;
    - `capSupported` returns the same text, so ValidateVolumeCapabilities never confirms it (mirrors ADR 0006's block rule).
  - **Not** in Expand (C2: a flag flip must not strand an existing volume).
  - Log the flag at startup next to `enable_block` (`main.go:108`).
  - Update the README row for the CSI front end.
- **ADR 0009** ("Non-NFS filesystem volumes are opt-in"), modelled on ADR 0006's block paragraph:
  - **Context:** no node side for a bare dataset.
  - **Decision:** off by default, `-enable-local-filesystem` restores it.
  - **Consequences:**
    - an existing non-NFS StorageClass stops provisioning when this ships;
    - existing volumes are untouched, and Expand and Delete still work;
    - the shipped deployment must be NFS mode (D1-D3), or it provisions nothing, which is a fast `ProvisioningFailed` instead of a hung pod.
  - **Reversal:** a flag.
- **Failing-first test:** `TestMountRefusedWithoutNFS` (`controller_test.go`):
  - `Controller{EP: endpointClient(t)}`, CreateVolume with `capability(false)` → want InvalidArgument. Today: OK.
  - ValidateVolumeCapabilities on a volume created with the flag on, called with the flag off → not confirmed.
  - With `EnableLocalFilesystem: true` → OK.
- **Knock-on edits:**
  - 16 uses of `capability(false)` in `internal/csi/*_test.go` run in non-NFS mode, so set the flag in those controllers.
  - `TestSanity` (`sanity_test.go:77`) sets `EnableLocalFilesystem: at != "block"` when not in NFS mode.
  - The real VM csi-mount shard needs nothing more, because the flag lives in the test's controller.
  - `cmd/zfs-csi/main_test.go`: add a flag-parse case.
- **Effort:** M, 2-3 hours including the ADR. Ship it together with, or after, D1-D3, so that the shipped manifests stay usable.

---

## D1. Ship the csi-driver-nfs node DaemonSet under our driver name

- **Where:**
  - nothing exists: `deploy/k8s/` holds only `classes.yaml`, `controller.yaml`, `csidriver.yaml` and `rbac.yaml`;
  - ADR 0004:14,37 delegates the node side to csi-driver-nfs `--drivername`.
- **What happens now:** no node plugin registers `zfs-endpoint.wommy.github.io`. kubelet fails every NodePublish with "driver name … not found in the list of registered CSI drivers".
- **k8s behaviour:** kubelet resolves the CSI driver per node through the plugin registration socket, which node-driver-registrar provides. `CSIDriver attachRequired: false` skips attach and goes straight to NodePublish, which needs a registered node plugin.
- **Minimal fix:** a new `deploy/k8s/node.yaml` with a DaemonSet `zfs-csi-node` in namespace `zfs-csi`, copied from upstream csi-driver-nfs `deploy/v<tag>/csi-nfs-node.yaml` at a pinned release:
  - Containers: `nfs` (`registry.k8s.io/sig-storage/nfsplugin:v<tag>@sha256:…`), `node-driver-registrar` and `liveness-probe`, all pinned tag+digest from the same k8s.io promoter `images.yaml` used for the sidecars (`controller.yaml:101-106`).
  - `nfs` args: `--drivername=zfs-endpoint.wommy.github.io`, `--nodeid=$(NODE_ID)`, `--endpoint=unix:///csi/csi.sock`. Do **not** apply upstream's `CSIDriver nfs.csi.k8s.io` or its controller Deployment.
  - Registrar: `--kubelet-registration-path=/var/lib/kubelet/plugins/zfs-endpoint.wommy.github.io/csi.sock`.
  - Upstream's node shape: `hostNetwork: true`, `dnsPolicy: ClusterFirstWithHostNet`, `privileged: true` on `nfs`, `mountPropagation: Bidirectional` on `/var/lib/kubelet/pods`, and hostPaths for `plugins` and `plugins_registry`. The namespace is already PSA `privileged` (`rbac.yaml:16-25`).
  - Requests and limits on every container.
  - A ServiceAccount `zfs-csi-node`, with whatever RBAC upstream's `rbac-csi-nfs.yaml` gives the node SA at that tag (none, or only what it lists). Record it like `upstreamRBAC`.
  - Header comments: ADR 0001 D8's F6 controls (pod-egress deny to 2049/111/20048, firewall 2049 to the node subnet, AUTH_SYS warning).
- **Failing-first test** (`deploy/k8s_test.go`), `TestNodeDaemonSet`. A DaemonSet exists, and:
  - its `nfs` container has `--drivername=` equal to `zcsi.DefaultDriverName`;
  - the registrar's `--kubelet-registration-path` contains the driver name;
  - every image matches `pinnedDigest`;
  - every container has requests and a memory limit;
  - no `nfs.csi.k8s.io` CSIDriver is shipped.
  - Fails today: there is no DaemonSet.
- **Test seam to fix first:** `controller()` (`deploy/k8s_test.go:68-87`) and `TestManifests`'s `kinds` map (`deploy/deploy_test.go:189-192`) keep the *last* object of each kind.
  - A second ServiceAccount is harmless.
  - A second ClusterRole would silently replace the controller's in `TestSidecarRBAC`.
  - Select by `metadata.name` (`zfs-csi-controller`).
  - `TestPodHardening`'s "hostPath mounted read-only" stays scoped to the Deployment: the node plugin needs read-write and Bidirectional mounts.
- **Effort:** M-L, about half a day, mostly the pinning and the upstream RBAC transcription.

## D2. A documented `-nfs-server` systemd drop-in

- **Where:** `deploy/systemd/zfs-endpoint@.service:50-56`: the ExecStart has no `-nfs-server`, so the endpoint is in non-NFS mode. The only drop-in is `zfs-endpoint@.service.d/hardening.conf`.
- **What happens now:** the operator must rewrite ExecStart in full to turn NFS mode on. That is error-prone: the socket paths and `-parent %I` must match `TestUnitServesTheConventionalSocket`.
- **Minimal fix (preferred):**
  - In the unit, add `Environment=ZFS_ENDPOINT_NFS=` and end ExecStart with an unbraced `$ZFS_ENDPOINT_NFS`. systemd word-splits an unbraced `$VAR`, and an empty one yields no argument.
  - Ship `deploy/systemd/zfs-endpoint@.service.d/nfs.conf.example`, with `[Service]` and `Environment=ZFS_ENDPOINT_NFS=-nfs-server=<stable VIP or DNS name>`, and comments on:
    - ADR 0004 Q1 (advertise stable names: an address in PVs never changes for them);
    - root being required;
    - D8's F6 controls.
  - Alternative: a drop-in with `ExecStart=` plus a full restatement. It is simpler to read, but `deploy_test` must then parse the drop-in's ExecStart against the convention.
- **Failing-first test** (`deploy/deploy_test.go`):
  - the unit's ExecStart ends in `$ZFS_ENDPOINT_NFS`;
  - the example drop-in sets exactly that variable to a `-nfs-server=` value;
  - `TestUnitLint`'s namespace ban still passes on it.
  - The unit change runs through `ci/real-zfs-unit.sh` in the csi VM job, which already starts the unit, so a broken ExecStart fails there.
- **Effort:** S, 1-1.5 hours.

## D3. `nfs.clients` placeholder in the shipped StorageClass

- **Where:** `deploy/k8s/classes.yaml:1-13`: the parameters are only `reserve: "false"`, and the header lists only reserve and sparse.
- **What happens now:**
  - with D2 applied, every PVC fails with `InvalidArgument` `nfs.clients is required` (ADR 0004 Q2), which is final;
  - without D2, the class works and the pod hangs (C5).
- **Minimal fix:**
  - Add `nfs.clients: "<node-subnet>/24"`, a deliberately unparsable placeholder. The endpoint refuses it with `ProvisioningFailed` naming the value.
  - Do not use a plausible CIDR or TEST-NET (`192.0.2.0/24`): those parse, export to nobody, and bring back the hung pod.
  - Update the header comment with the `nfs.*` keys (`controller.go:39-44`), "comma-separated IPv4 CIDRs with /N, no broader than `-nfs-min-prefix`", and the fact that StorageClass parameters are immutable.
- **Failing-first test:** in `TestManifests`, the StorageClass has `nfs.clients`; its value contains `<`, so it is clearly a placeholder; and the file says to replace it.
- **Effort:** XS, about 20 minutes.

## D4. csi-sanity in NFS mode (`CSI_SANITY_NFS=1`)

- **Where:** `internal/csi/sanity_test.go:58-89`. `endpointConn` (`harness_test.go:35-86`) builds the endpoint with no `FileExport`, `Etab` or `NFSServers`, so both runs (mount and block) are non-NFS. NFS is covered only by the hand-written N-tests (`nfs_test.go`, simulator only).
- **What happens now:** the production mode (D1-D3) never runs the conformance suite. In wave20 it passed 44/44 on the simulator in about 0.3s, on a scratch copy.
- **Spec and k8s:** nothing is violated. This is coverage of the one mode with a node side.
- **Minimal fix:**
  - `endpointConn` takes options: with `CSI_SANITY_NFS=1`, use `FileExport: true` and `NFSServers: {"nfs.example.com"}`, with `Etab` set to the simulator, or to `endpoint.DefaultEtab` on a real pool (root, nfs-server running).
  - `cfg.TestVolumeParameters = {"nfs.clients": "10.0.0.0/24"}`.
  - Refuse `CSI_SANITY_NFS=1` with `CSI_SANITY_ACCESS_TYPE=block` (t.Fatal).
  - Ginkgo runs once per process, so this is a second `go test` invocation.
- **Failing-first test:** the env-gated run itself. Prove it bites: with C3-C5 not yet merged, temporarily break `parseParams` (drop `nfs.clients`) → sanity CreateVolume specs fail.
- **CI placement:** see below. In short: simulator on PR, real pool nightly.
- **Effort:** S (1 hour) for the simulator plus a PR step. M (2-3 hours) for the real-pool shard: guest script, check-skips floor, nightly wiring.

---

## Does real-pool NFS csi-sanity fit the csi VM job within ADR 0005?

**Recommendation: no, not yet.**
- Run it in the PR tier on the **simulator** (the `test` job, off the critical path).
- Run it on the **real pool nightly** (`nightly.yml` `real-zfs-full`, csi job).
- Promote it to the PR csi job only after measuring it, or with a matching saving.

Data: job wall times from the GitHub API for the last four runs. ci-runs.jsonl gives only push-to-green, and the job log blobs are proxy-blocked here, so the step times come from the jobs API.

| Run (sha) | push-to-green | csi job | its test step | endpoint 0/2 job | slowest |
|---|---|---|---|---|---|
| 40540322181 (286cf47) | 43s | 28s | 12s | 39s | endpoint |
| 40539209599 (d232d63) | 47s | 30s | 16s | 43s | endpoint |
| 40537458909 (4a63496) | 51s | 37s | 14s | 37s | tie |
| 40537085486 (3a45760) | 43s | **38s** | 16s | 36s | **csi** |

- **Test phase:**
  - the csi guest runs three shards concurrently on 4 vCPUs (`ci/real-zfs-guest.sh:219-223`): csi-other about 2s, csi-mount about 8.0s, csi-block about 8.6s;
  - the phase is 8.9-9.4s at 78-79% CPU busy (wave16 csi log; csi-sanity "Ran 44 of 103 Specs in 7.8-8.1s").
- **Estimate for a fourth shard** (`csi-nfs`, pool `zcsi3`):
  - it is about the size of csi-mount, plus an `exportfs -ra` and an etab read-back on every create and delete;
  - the CPU goes from about 79% to saturated, so the phase grows by about +2-4s, to about 11-13s.
- **Effect on push-to-green:** the csi job set or tied the pace in 2 of the last 4 runs. Push-to-green is already 43-51s, so it is above ADR 0005's 45s nominal margin in half the runs. ADR 0005 says: "Work that brings the nominal time above 45s needs a matching saving." The +2-4s lands directly on push-to-green in those runs.
- **The simulator run is free:**
  - the `test` job finishes in 27s (its `go test -race ./...` step takes 21s), against 36-43s for the VM jobs;
  - a second step, `CSI_SANITY_NFS=1 go test -race -count=1 -run '^TestSanity$' ./internal/csi`, reuses the build cache and costs about 1-2s, off the critical path.
  - Add it to `ci/check-skips.sh`'s spec floor (44) if that step's log is checked.
- **Nightly wiring:**
  - `ZFSVM_CSI_NFS=1` (set in `nightly.yml` only) adds `run csi-nfs zcsi3 env CSI_SANITY_NFS=1 ./csi.test … -test.run '^TestSanity$'`;
  - `ci/real-zfs-vm.sh:531-534,613-616` copies and checks its log at floor 44.
  - The guest already waits for nfs-server and the nfsd threads.
  - A failure files the usual `needs-triage` issue.
- **Promotion rule:**
  - promote to the PR csi job when 3 consecutive nightly or dispatch runs show the 4-shard csi test phase within +1s of today's (about 9s);
  - or pair it with a saving: for example, once C5 makes non-NFS mount opt-in, the owner may choose to swap the real-pool csi-mount shard for csi-nfs, so the shard count is unchanged.
  - The swap is an owner call under ADR 0005's "never cut measurement" rule, since it moves non-NFS mount sanity to the simulator only.
