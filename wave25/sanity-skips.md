# csi-sanity: the 59 specs TestSanity does not pass (csi-test v5.6.0)

How it was measured: `go test -c ./internal/csi`, then
`CSI_SANITY_ACCESS_TYPE={mount,block} ./csi.test -test.run '^TestSanity$' -ginkgo.v -ginkgo.json-report=...`
(simulator). Both access types give the same result, spec for spec:
`Ran 44 of 103 Specs -- 44 Passed | 0 Failed | 1 Pending | 58 Skipped`.
Raw lists: `specs-mount.txt`, `specs-block.txt`, logs `san-*.log`, JSON reports `r-*.json`, all in this directory.

How the harness configures csi-sanity (`internal/csi/sanity_test.go`):
- `sanity.NewTestConfig()` defaults, plus `TestVolumeSize = 1 GiB`. `TestVolumeAccessType` comes from `CSI_SANITY_ACCESS_TYPE`, and `block` also sets `Controller.EnableBlock`. Nothing else is set: no `TestVolumeParameters`, no `TestNodeVolumeAttachLimit`, no secrets.
- Focus: `Identity Service|\[Controller Server\]`, applied only when no `-ginkgo.focus` is given. There is no skip pattern.
- A `cleanupNode` fixture registers a Node server that only answers `NodeUnpublishVolume` (NotFound) and `NodeGetCapabilities` (empty), so resource cleanup works.
- The endpoint runs without NFS config (non-NFS mode), on the simulator, or on a real pool when `ZFS_TEST_POOL` is set.

## Summary

| Class | Count | What |
|---|---|---|
| (a)+(c) node-service, focused out | 27 | `Node Service ...` |
| (c) GroupController, focused out (also (b): no GROUP_CONTROLLER_SERVICE) | 6 | `GroupController Service ...` |
| (b) controller capability not advertised (runtime `Skip` in csi-sanity) | 25 | publish 10, modify 8, GetSnapshot 3, health 3, snapshot topology 1 |
| upstream-disabled (`XIt` in csi-test, reported as Pending) | 1 | ListVolumes pagination |
| (d) should run but doesn't | **0** | |
| **total** | **59** | |

Every spec that the focus selects and that is not capability-gated passes: 44 of 44.

## (a)+(c) Node Service: 27, removed by the focus regex

The driver has no node plugin: ADR 0004 hands the node side to csi-driver-nfs. These specs only exercise the test fixture.
With `-ginkgo.focus='.*'` they would give 9 fail, 17 self-skip and 1 pass, all against `cleanupNode`. So the sanity_test.go comment that they "fail every Node spec in its BeforeEach" is no longer accurate, but the conclusion stands.

- NodeExpandVolume (4): no volume id; no volume path; volume not found; works after node-publish
- NodeGetCapabilities (1): return appropriate capabilities
- NodeGetInfo (1): return appropriate values
- NodeGetStorageHealth (1)
- NodeGetVolumeHealth (2): no volume id; health for a valid volume
- NodeGetVolumeStats (4): no volume id; no volume path; volume not on path; volume not found
- NodePublishVolume (4): no target path; no volume capability; no volume id; SINGLE_NODE_MULTI_WRITER already mounted at a different path
- NodeStageVolume (3): no staging path; no capability; no volume id
- NodeUnpublishVolume (3): no target path; no volume id; removes target path
- NodeUnstageVolume (2): no staging path; no volume id
- Node Service should work / should be idempotent (2)

## (c) GroupController: 6, removed by the focus regex

These are GroupControllerGetCapabilities (1) plus CreateVolumeGroupSnapshot (1), GetVolumeGroupSnapshot (2) and DeleteVolumeGroupSnapshot (2) under `[GroupController VolumeGroupSnapshots]`.

Even unfocused, all 6 self-skip ("GroupControllerService not supported") because Identity does not advertise GROUP_CONTROLLER_SERVICE.

Is not advertising correct? Yes. Nothing serves the GroupController RPCs.

Could it be implemented? Yes: ZFS can snapshot several datasets atomically (`zfs snapshot a@x b@x`). That would be a feature, though; it is not a gap.

## (b) Capability not advertised: 25, runtime `Skip()` in csi-sanity's BeforeEach/It

The driver advertises CREATE_DELETE_VOLUME, CREATE_DELETE_SNAPSHOT, LIST_VOLUMES, LIST_SNAPSHOTS, GET_CAPACITY, CLONE_VOLUME and EXPAND_VOLUME (`internal/csi/controller.go:94`). Its plugin capabilities are CONTROLLER_SERVICE and VolumeExpansion ONLINE.

### PUBLISH_UNPUBLISH_VOLUME: 10

Specs:
- ControllerPublishVolume (7): no node id; no volume capability; no volume id; more volumes than the node max attach limit; node does not exist; volume does not exist; already published but incompatible
- ControllerUnpublishVolume (1): no volume id
- Controller Service "volume lifecycle" (2): should work; should be idempotent

Correct not to advertise: yes, for now.
- The CSI front end has no ControllerPublish or ControllerUnpublish; it embeds Unimplemented.
- Advertising the capability obliges the server to implement both RPCs and makes the CO run external-attacher and call publish before node stage.
- ADR 0004 picks static export at create first ("needs no attacher sidecar"). Per-node publish is "later, behind a flag".
- The endpoint already has `PublishFileExport`/`UnpublishFileExport` (`api/zfsendpoint/v1/endpoint.proto:50`), so it would be correct to advertise once that flag exists and wires these RPCs. These 10 specs would then come into scope.
- The attach-limit spec also needs `TestNodeVolumeAttachLimit`, which is unset.

### MODIFY_VOLUME: 8

Specs:
- ModifyVolume (6): no volume id; unsupported mutable params; volume does not exist; VAC-created volume with unsupported params; modify a VAC-created volume; modify a volume created without VAC
- CreateVolume (2): create with a volume attribute class; reject an invalid volume attribute class

Correct not to advertise: yes. There is no ControllerModifyVolume and no mutable_parameters handling, and no ADR asks for it (`docs/research/gaps/csi-frontend.md:51` lists it as MISS). It could become a feature later, for example mutable ZFS properties such as compression or quota, but that would need design.

### GET_SNAPSHOT (alpha): 3

GetSnapshot specs: unknown id is an error; empty request is an error; returns the matching snapshot.

Correct not to advertise: yes. The RPC is optional and alpha, and the Kubernetes sidecars do not use it.

Implementing it is cheap and spec-correct for a controller-only driver: ListSnapshots with `snapshot_id`, where an empty result becomes NotFound (csi-frontend.md:49). That would bring 3 specs into scope. It is low value.

### GET_VOLUME_HEALTH / LIST_VOLUME_HEALTH (formerly VOLUME_CONDITION with GET_VOLUME): 3

Specs:
- ControllerGetVolumeHealth (2): no volume id; health for a valid volume
- ControllerListVolumeHealth (1): appropriate response

Correct not to advertise: yes, as long as nothing serves them.

Implementing them would be spec-correct for a controller-only driver. The endpoint has `GetVolume`, and abnormal conditions could come from pool or dataset state. But the RPCs are optional and alpha, and in Kubernetes only external-health-monitor consumes them.

There is no LIST_VOLUMES_PUBLISHED_NODES spec in v5.6. That capability requires PUBLISH_UNPUBLISH anyway, so it is not applicable.

### SNAPSHOT_ACCESSIBILITY_CONSTRAINTS (plugin capability): 1

Spec: CreateSnapshot "should succeed when creating a snapshot with accessibility requirements".

Correct not to advertise: yes. The driver reports no topology: there is no VOLUME_ACCESSIBILITY_CONSTRAINTS and no NodeGetInfo of its own. Advertising snapshot topology without volume topology would be wrong.

## Upstream-disabled: 1

`Controller Service [Controller Server] ListVolumes pagination should detect volumes added between pages and accept tokens when the last volume from a page is deleted` is `XIt` in csi-test (controller.go:262, csi-test#223). It is Pending for every driver and is not under the repo's control. The other ListVolumes specs, including the invalid starting_token spec, pass.

## (d) Specs the driver should run but doesn't: none

Every focused spec gated only on a capability the driver advertises passes in both access types:
- CreateVolume, including clone from volume and from snapshot
- DeleteVolume
- ValidateVolumeCapabilities
- ListVolumes
- GetCapacity
- CreateSnapshot, DeleteSnapshot and ListSnapshots (7 specs)
- ExpandVolume (3 specs)
- Identity (3 specs)

## The real-ZFS VM leg

`ci/real-zfs-guest.sh:231-232` runs `env ZFS_TEST_POOL=zcsi{1,2} ZFS_REQUIRE_REAL=1 CSI_SANITY_ACCESS_TYPE={mount,block} ./csi.test -test.count=1 -test.v -test.timeout=... -test.run '^TestSanity$'`.

What is the same as the local run:
- No `-ginkgo.*` flags, so the same default focus applies.
- The same TestConfig.
- No NFS config.
- The same fixed capabilities.

What is different:
- It runs on a real pool, with a udev settle.
- `ZFS_REQUIRE_REAL` refuses the simulator fallback.
- The log uses `-test.v` without `-ginkgo.v`.

So it should select and pass the same 44, and skip or pend the same 59. `ci/real-zfs-vm.sh:703-704` enforces this with `ci/check-skips.sh vm csi <log> ... 44` on each of the two logs.

## Would CI notice a passing spec starting to skip?

Partly. It catches the count dropping, not which spec changed.

- `ci/expected-skips.txt` cannot see it. Ginkgo spec skips are not `--- SKIP:` lines; TestSanity itself reports PASS, and the file says so.
- The only guard is the spec floor in `check-skips.sh` (`Ran N of M` ≥ 44, VM leg only).
- Verified locally, Ginkgo's "Ran" excludes runtime-`Skip()`ed specs as well as filtered ones:
  - focus `ModifyVolume` gives `Ran 0`, with 6 runtime skips;
  - `-ginkgo.skip=ExpandVolume` gives `Ran 41`.
  So a regression that turns one of the 44 into a skip, such as a dropped capability or a narrowed focus, gives `Ran 43` and fails the leg.

Holes:
1. **The floor checks a count, not identities.** If one spec starts skipping while another starts running, the count stays at 44 and nothing fails. Examples: a csi-test bump that adds specs, or advertising GET_SNAPSHOT (+3) at the same time as losing ExpandVolume (−3). The log has the names, but nothing compares them to a list.
2. **The floor is a minimum and does not ratchet.** If coverage grows (for example to 47 after implementing GetSnapshot), losing up to 3 specs later still passes until someone raises the 44 by hand.
3. **There is no floor on the simulator.** The unit job (`go test -race ./...` in `.github/workflows/ci.yml:138`) runs TestSanity without counting specs, so only the VM csi job checks.
4. **"Ran" counts specs that run, not specs that pass.** A failure is caught separately by the FAIL/exit-code check, so that is fine.

A stronger check would compare the sorted names of passed specs (from `-ginkgo.json-report` or `-ginkgo.v` output) against a checked-in list, per access type, with an exact match.
