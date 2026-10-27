# Adversarial review: CSI controller front end (merge 5072187)

Scope: internal/csi/*, cmd/zfs-csi/main.go, the plan.go NotFound change, plus the
endpoint paths the front end passes straight through. The front end returns
endpoint errors unchanged, so endpoint behaviour reached through a CSI call
counts here.

Evidence: throwaway tests in the repo copy at
`/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/adv3/repo/internal/csi/adv_test.go`
and `adv2_test.go`. They run on the simulator through the real gRPC
server and the CSI Controller, with `go test -run 'TestAdv' -v ./internal/csi/`.
The output quoted below comes from those runs.

## Findings, ranked

### 1. MEDIUM: clone or restore from an unmanaged dataset or snapshot succeeds, and it mutates that dataset
- Where: internal/endpoint/plan.go:181-182 (snapshot source: checks `Type` and `Deferred` but not `Managed`) and plan.go:206-207 (volume source: no `Managed` check). The front end passes the source through at internal/csi/controller.go:266-276.
- Scenario: `tank/vols/foreign` is a dataset under the parent that this endpoint did not create, with a hand-made snapshot `@manual`.
  - `CreateVolume{name:"steal", source volume "foreign"}` returns OK. It snapshots the unmanaged dataset (`foreign@volume-source-for-volume-steal` now exists) and clones it.
  - `CreateVolume{name:"steal2", source snapshot "foreign@manual"}` returns OK.
  - For the same id, `ValidateVolumeCapabilities{volume_id:"foreign"}` returns NotFound (GetVolume requires Managed, endpoint.go:390). CreateSnapshot (plan.go:406) and DeleteVolume (plan.go:321) also refuse unmanaged datasets.
- Effect: the endpoint copies data it does not own into a CSI volume. It also leaves a snapshot and a dependent clone on the foreign dataset, so that dataset's owner can no longer `zfs destroy` it. The endpoint's own NotFound contract for the same id contradicts this.
- Fix: require `src.Managed && src.Committed` for a volume source, and `snap.Managed` (with its source volume managed) for a snapshot source. Otherwise return NotFound.

### 2. MEDIUM: restore or clone returns a volume larger than limit_bytes with OK (spec MUST)
- Where: plan.go:107 checks only the rounded *requested* size against the limit. sizeOps (plan.go:268-289) never shrinks, so a clone keeps the source's size: plan.go:194 for a snapshot source, :231-232 for a volume source. The front end forwards limit_bytes at controller.go:263 but does not check the returned capacity.
- Scenario: a 10 GiB block volume, snapshot s1. `CreateVolume{block, required 1 GiB, limit 2 GiB, source s1}` returns OK with `capacity_bytes=10737418240`. A clone from the volume gives the same result. Observed in the test run.
- Spec: limit_bytes "Volume MUST not be bigger than this" (spec.md:1047). The error table gives OUT_OF_RANGE for "creating volumes larger than the source" cases (spec.md:1298).
- Kubernetes never sets limit_bytes, so only non-k8s COs and direct callers hit this.
- Fix: in PlanCreateVolume, when a source exists and `LimitBytes > 0 && sourceCapacity > LimitBytes`, return OutOfRange. Alternatively, have the front end refuse any response whose capacity exceeds limit (but then the volume has already been created).

### 3. MEDIUM: zfs-csi's CSI socket is not owner-only, which re-exposes the owner-only endpoint
- Where: cmd/zfs-csi/main.go:79 (`MkdirAll 0755`) and :89 (`net.Listen("unix")` under the inherited umask). Compare cmd/zfs-endpoint/main.go:107 (0700) and :119-125 (`Umask(0o177)` around Listen).
- Scenario: the endpoint socket is 0600 because transport auth does not exist yet (README: "socket is mode 0600 until then"). zfs-csi runs as root and forwards every Create, Delete, Snapshot and Expand call. Its socket mode is `0777 &^ umask`. Under umask 0 (some container runtimes or init setups), or on a group-writable umask with a shared /csi hostPath, any local user or container that can reach the socket can delete or clone volumes. Even with umask 022 the protection depends on the environment, not on the code.
- Fix: copy the endpoint's listener: create the directory with 0700 (or 0750 for a sidecar group) and set `Umask(0o177)` around `net.Listen`. Otherwise `Chmod` the socket before Serve, but that leaves a window.

### 4. LOW-MEDIUM: DeleteVolume of an id this driver could not have issued returns INVALID_ARGUMENT, but the spec says it MUST be OK
- Where: endpoint.go:336 (`volumeID` check, returns InvalidArgument), passed through at controller.go:287.
- Scenario: a static or imported PV with `volumeHandle: tank/vols/pvc-1` (a full dataset path), or a handle over 128 bytes, and reclaimPolicy Delete. `DeleteVolume` returns `InvalidArgument: invalid volume name "tank/vols/pvc-1"` (observed). external-provisioner retries forever and the PV never goes away.
- The spec (DeleteVolume) says that when the volume does not exist, the plugin MUST reply 0 OK. DeleteSnapshot already treats a malformed id as OK (endpoint.go DeleteSnapshot, plan.go:435).
- Fix: in DeleteVolume, return nil for an id that fails `volumeID`, matching DeleteSnapshot.

### 5. LOW: the NotFound change is half-applied: malformed ids in Validate, Expand and CreateSnapshot still return INVALID_ARGUMENT
- Where: endpoint.go:382 (GetVolume, used by ValidateVolumeCapabilities at controller.go:300), endpoint.go:358 (ExpandVolume), plan.go:381 (PlanCreateSnapshot source).
- Scenario: `volume_id:"a/b"` returns `InvalidArgument: invalid volume name "a/b"` for all three (observed). The spec error tables list NOT_FOUND for "Volume does not exist" in these RPCs. Commit 5072187 adopted the rule that ids are opaque, so an id the endpoint could never have issued names nothing. It applied that rule only to the CreateVolume source ids. For Expand, external-resizer treats INVALID_ARGUMENT as infeasible, which is a different and final state.
- Fix: return NotFound for ids that fail parsing in these three paths, the same as plan.go:179/204.

### 6. LOW: GetCapacity ignores volume_capabilities and parameters
- Where: controller.go:331-337.
- Scenario: `GetCapacity{caps:[MULTI_NODE_MULTI_WRITER], parameters:{bogus:x}}` returns the full 1 TiB (observed). CreateVolume with the same input fails with INVALID_ARGUMENT. The spec says the plugin SHALL return capacity usable for volumes satisfying ALL given capabilities, which is 0 here. With CSIStorageCapacity, the scheduler would believe capacity exists for an unprovisionable class.
- Fix: run `kindOf` and `parseParams` on the request. If either fails, return `available_capacity: 0`.

### 7. LOW: ValidateVolumeCapabilities confirms parameters and volume_context it never checked
- Where: controller.go:309-316.
- Scenario: a `filesystem` volume with `parameters{sparse:true}` and `volume_context{kind:block, dataset:other/pool}` comes back Confirmed and echoes both (observed). A thick zvol with `sparse=true` is also Confirmed. The Confirmed fields are defined as what "the plugin validated" (spec.md:1517-1527). sparse on a filesystem is INVALID_ARGUMENT at create.
- Fix: check the parameters against the volume's kind and its sparse or reserve state, and compare volume_context with `volumeOut(got)`. Alternatively, leave `Confirmed.parameters` and `Confirmed.volume_context` empty.

### 8. LOW: Expand of an unlimited filesystem returns capacity_bytes=0 and ignores limit_bytes
- Where: plan.go:358-360 (refquota 0 is a no-op) and controller.go:362-364.
- Scenario: a filesystem created without a capacity_range. `ControllerExpandVolume{required 5 GiB, limit 6 GiB}` returns OK with `capacity_bytes=0` (observed). capacity_bytes is REQUIRED in the response and means "capacity after expansion". If external-resizer gets 0 it writes 0 into the PV and PVC status, so the PVC keeps looking unresized. Kubernetes always sends required_bytes at create, so this needs a non-k8s create or an adopted volume.
- Fix: when refquota is 0, either apply refquota=want (honouring limit) or return OUT_OF_RANGE or INVALID_ARGUMENT. Never return 0.

### 9. LOW: no reconnect tuning to the endpoint, so recovery after an endpoint restart can lag by up to 120 s
- Where: cmd/zfs-csi/main.go:40 (`grpc.NewClient` with defaults: backoff MaxDelay 120 s, grpc backoff/backoff.go:51; no keepalive; calls are not WaitForReady).
- Scenario: the endpoint is down for a few minutes, for example during an upgrade, and the subchannel backoff grows to 120 s. After the endpoint is back, every CSI call, Probe included, fails fast with UNAVAILABLE until the backoff timer fires. livenessprobe can restart the pod during that window. These are retryable codes, so no data is wrong, only slow.
- Fix: `grpc.WithConnectParams(grpc.ConnectParams{Backoff: backoff.Config{BaseDelay: 200ms, Multiplier: 1.6, MaxDelay: 5s}, MinConnectTimeout: 2s})`.

### 10. LOW: shutdown is weaker than the endpoint's
- Where: cmd/zfs-csi/main.go:55-68.
- Scenario: there is no second-signal escape (compare zfs-endpoint main.go:84-96). An operator's second Ctrl-C or SIGTERM during a 30 s drain does nothing.
- Fix: copy the `again` channel from zfs-endpoint.

### 11. LOW: listen() removes whatever file sits at -csi-address
- Where: cmd/zfs-csi/main.go:86. The same pattern is in zfs-endpoint.
- Scenario: a mistyped `-csi-address unix:///etc/foo.conf` running as root unlinks a regular file.
- Fix: `Lstat` first and remove only if `Mode()&os.ModeSocket != 0`.

### 12. LOW (design divergence): StorageClass ZFS properties are refused, though the endpoint supports them
- Where: controller.go:150-172. The endpoint advertises CAPABILITY_PROPERTIES and has the `properties` field (endpoint.proto:43-44, :95-106). The design's mapping table says "parameters → properties" (docs/research/gaps/csi-frontend.md §4).
- Scenario: a StorageClass `compression: zstd` gets `InvalidArgument: unsupported parameter "compression"` (observed). This is default-deny and safe, but the feature the endpoint was extended for cannot be reached from CSI. A green csi-sanity run does not show this (TestVolumeParameters is empty).
- Fix: forward the parameters that are not sparse, not reserve and not `csi.storage.k8s.io/` as `properties`. The endpoint allowlist already does default-deny and value validation.

### 13. LOW (forward-looking): no topology, though volumes are host-local
- Where: identity.go:42-51 (no VOLUME_ACCESSIBILITY_CONSTRAINTS), controller.go:85 ("usable from one node only"), and volume_context devicePath and mountpoint "on the storage host" (controller.go:214-219).
- Scenario: once a Node plugin exists on more than one node, the CO can schedule a pod anywhere. Nothing tells it which node can reach `/dev/zvol/...`. The context also freezes host-local paths into the immutable PV.
- Fix: before any Node service ships, report `accessible_topology` (the storage host's segment) and advertise the plugin capability.

## Checked and clean
- **Error passthrough.** ABORTED, UNAVAILABLE and DEADLINE_EXCEEDED reach the sidecar unchanged; the front end returns the endpoint's status as-is. Deadlines propagate sidecar → zfs-csi → endpoint through gRPC. Each hop's server deadline is no earlier than its caller's, so the sidecar always times out first and sees its own DEADLINE_EXCEEDED. The endpoint's 1 s lock margin then yields ABORTED.
- **Idempotency through translation.** `sparse` values true/1/TRUE/t all canonicalize to the same request and the repeats return OK (observed). `csi.storage.k8s.io/*` keys (extra-create-metadata) are dropped before the endpoint, so a PVC name in the parameters cannot change the request. fs_type "" and "zfs" both map to FILESYSTEM. A block repeat as mount is ALREADY_EXISTS.
- **Rounding.** Block `required = limit = 1 GiB+1` gives OUT_OF_RANGE ("1073758208 (rounded) exceeds limit") (observed). Negative ranges and required > limit give INVALID_ARGUMENT.
- **Parameters.** Unknown keys and non-bool values give INVALID_ARGUMENT (default-deny). sparse on mount and reserve on block are refused by the endpoint.
- **Secrets.** These are never read or logged, and no logging interceptor exists.
- **Node stub** (sanity_test.go:33-41). csi-sanity's cleanup (resources.go:248-272) calls only NodeUnpublishVolume and NodeGetCapabilities. Every Node spec is focused out, so the stub masks no controller behaviour.
- **csi-sanity coverage.** On the simulator it ran 41 of 92 specs. Every runtime skip is a ControllerPublish or ModifyVolume spec, which is legitimate because those capabilities are not advertised. Both CI paths (noble: ci.yml:84-91; VM: ci/real-zfs-vm.sh:68-71) set ZFS_TEST_POOL under sudo for mount and block. So real ZFS is exercised, not skipped. A gap remains: the sanity config passes no parameters, so sparse and reserve are never exercised by sanity.
