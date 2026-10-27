# Gap: thin CSI front end over the ZFS endpoint

Question: is `api/zfsendpoint/v1/endpoint.proto` enough for a CSI controller that passes csi-sanity?
Short answer: no. The controller-only subset can pass with a ~350-line translator, but (a) one endpoint behaviour fails a sanity test today, (b) the full sanity run also exercises a Node Service that the endpoint cannot provide, and (c) passing sanity does not exercise StorageClass parameters at all.

Pins: csi-test `pkg/sanity` at `github:kubernetes-csi/csi-test@95f23ec4` (GitHits). CSI spec v1.14.0 at `scratchpad/repos/spec` (VERSION, csi.proto). linstor-csi at `github:piraeusdatastore/linstor-csi@4faad044` (GitHits). synology-csi at `scratchpad/repos/synology-csi` @02af05d (local).

## 1. Prior claims checked

| Prior claim | Verdict | Evidence |
|---|---|---|
| Endpoint codes equal gRPC codes, so front ends translate 1:1 (`errors.go`) | HOLDS for the values present. Missing: `Canceled`, `DeadlineExceeded`, `Unimplemented`. `zfsErr` defaults to `Internal` (`endpoint.go:194`), so a ctx deadline inside a zfs exec becomes final `INTERNAL`, not retryable `DEADLINE_EXCEEDED`. | `internal/endpoint/errors.go:10-23`, `internal/server/server.go:26-33` |
| adversarial S6: compatibility ignores sparse/reserve | BROKEN at HEAD. Sparse and Reserve are compared. Still unchecked: any StorageClass property, because the request has no such field. | `plan.go:239-262` |
| adversarial S7: LockWait 30s outlasts the 10s sidecar timeout | BROKEN at HEAD. `lock()` clamps the wait to deadline minus 1s, so the sidecar gets ABORTED. | `endpoint.go:167-184` (default at `:90-91`) |
| adversarial S1: refuse DeleteVolume while snapshots exist | HOLDS (deviates from a SHOULD, as stated). | `plan.go` PlanDeleteVolume |
| sidecar-conformance: advertising an RPC you do not implement fails its sanity tests | HOLDS. Gates confirmed at `controller.go` lines 347, 598, 1205, 1285, 1468, 1519, 1624, 1665. | csi-test |
| sidecar-conformance: ControllerGetCapabilities and ValidateVolumeCapabilities always run | HOLDS. Neither has a capability gate (`controller.go:127`, `:653`). | csi-test |
| real-zfs-ci: sanity runs over `unix:` without a cluster | Partly. The Go API is used by synology (`test/sanity/sanity_test.go:68-90`), but synology serves Controller and Node from one driver, which is the pattern the endpoint cannot follow. | synology-csi |
| api-packaging: no `buf breaking` config | BROKEN. `buf.yaml` sets `breaking: use: FILE`. `buf` is not installed here, so the check itself is unverified. | `/home/user/empty/buf.yaml` |
| (new) `plan.go:332`: snapshot name plus volume id must be at most 128 bytes | BROKEN against sanity. `len(vol)+1+len(name) > 128` rejects a 128-byte name on any volume. csi-test requires that name to succeed (`controller.go:459-469` for volumes; `:1405-1419` for snapshots, `MaxNameLength=128` at `:42`). Confirmed by a scratch test (copy in `scratchpad/probe`): vol of 44 bytes, name of 128, result `InvalidArgument`. The spec limit is on the name alone. | `plan.go:332` |

## 2. Coverage by RPC and sanity group

Legend: COVERED (endpoint or trivial front-end mapping), FE (front end only), MISS (no endpoint support, needs proto or planner), GATED (sanity skips unless advertised).

| RPC | sanity group (line) | Verdict |
|---|---|---|
| GetPluginInfo, GetPluginCapabilities, Probe | Identity (`identity.go`) | FE. Probe can call `GetCapabilities` for readiness. |
| ControllerGetCapabilities | `controller.go:127` | COVERED. Map `Capability` enum to RPC types. csi-test fails on unknown types (`:157-159`). |
| CreateVolume: no name, no caps | `:354`, `:365` | FE. Reject empty `volume_capabilities` with InvalidArgument. |
| CreateVolume: idempotent, same capacity | `:410` | COVERED (`compatible`). |
| CreateVolume: same name, different capacity, ALREADY_EXISTS | `:431` | COVERED. |
| CreateVolume: max-length name (128) | `:459` | COVERED for volumes. |
| CreateVolume from snapshot / missing snapshot NotFound | `:471`, `:507` | COVERED. |
| CreateVolume from volume / missing source NotFound | `:526`, `:550` | COVERED. Edge: a 128-byte source plus the internal `@volume-source-for-volume-<id>` name can exceed the 255 limit. Not in sanity. |
| CreateVolume with StorageClass parameters | not tested by default (`TestVolumeParameters` is empty, `sanity.go:98`) | MISS. The request has no field for compression, recordsize and so on. |
| CreateVolume with block access and zero capacity | not in default sanity | MISS in spec terms. The endpoint rejects it (`plan.go:93`). CSI says 0 lets the SP choose a default; the front end must supply one. |
| CreateVolume topology (`accessibility_requirements`) | `:1421` skipped unless NodeGetInfo reports topology | MISS, only if topology is advertised. Not needed for one host. |
| CreateVolume volume_context | node tests pass it through (`node.go:109`) | FE. Build it from `device_path` and `mountpoint`. No proto field. |
| DeleteVolume: missing id, invalid id `fake-vol-id` | `:605`, `:616` | COVERED. The front end must reject an empty id. An id with `/` or `@` returns InvalidArgument, which is a spec-level choice to confirm. |
| ValidateVolumeCapabilities | `:653-731` | FE plus COVERED for NotFound (`GetVolume`). Confirm only mount plus SINGLE_NODE_WRITER. Multi-writer and block (on a filesystem) are rejected. |
| ControllerPublish/Unpublish | `:733`, `:924` | GATED off. No attach concept. |
| ListVolumes incl. bad token ABORTED | `:189`, `:201` | COVERED (`page`, `endpoint.go:page`). |
| GetCapacity | `:165` | COVERED (`available` on the parent). |
| CreateSnapshot: same name/volume, different volume, max name | `:1285-1405` | COVERED except max name (see BROKEN above). |
| CreateSnapshot with accessibility requirements | `:1421` | GATED on topology. |
| DeleteSnapshot: empty id InvalidArgument, unknown id OK | `:1294`, `:1306` | Empty id needs a front-end check. Unknown id is COVERED. |
| ListSnapshots: id filter, source filter, empty on unknown, token | `:944-1190` | COVERED. |
| GetSnapshot: unknown id is an error, empty request is an error | `:1195-1273` (GET_SNAPSHOT gated) | FE. Use `ListSnapshots(snapshot_id)`; empty result becomes NotFound. Spec: `csi.proto:107`, alpha. |
| ControllerExpandVolume: no capacity range InvalidArgument, expand works | `:1459-1508` | FE for the missing-range case. Endpoint `PlanExpandVolume` returns success for capacity 0 on an existing volume (`plan.go:309`). |
| ControllerModifyVolume | `:1510-1613` (MODIFY_VOLUME gated) | MISS. Not advertised. |
| ControllerGetVolumeHealth, ControllerListVolumeHealth | `:1615-1700` gated | MISS. Not advertised. `VolumeCondition` was removed in 1.14 (`csi.proto:1174-1175`). |
| ControllerGetVolume | no test at this pin (no match in pkg/sanity) | FE. `GetVolume` plus empty `published_node_ids`. Alpha in spec (`csi.proto:115`). |
| Secrets (all RPCs) | `sc.Secrets` nil by default | FE. Ignore. The endpoint never sees secrets. |
| Node Service | `node.go:174` runs unconditionally; `NodeGetInfo` at `node.go:78` | NOT COVERED. The endpoint is not a node server. Full sanity needs a node stub or a ginkgo skip. Synology's sanity wires controller and node (`sanity_test.go:69`). |

## 3. Comparison: controller size

Per-RPC line spans, from each `func` line to the next `func` (includes blank lines and comments).

linstor-csi, `pkg/driver/driver.go` @4faad044 (GitHits):
- CreateVolume 196 (helpers: createNewVolume 144, createConsistencyGroupVolume 64, restoreConsistencyGroupVolume 55, consistencyGroupForPVC 21)
- ControllerPublish 100, ListVolumeHealth 65, ControllerGetCapabilities 83, ListSnapshots 66, ListVolumes 49, ControllerExpand 50, ValidateVolumeCapabilities 47, GetCapacity 35, DeleteSnapshot 32, GetVolumeHealth 28, DeleteVolume 24, CreateSnapshot 22, GetSnapshot 22, ControllerUnpublish 22, ControllerGetVolume 19
- Sum of the 16 RPC bodies: about 880 lines, excluding helpers.

synology-csi, `pkg/driver/controllerserver.go` @02af05d (local, 615 lines total):
- CreateVolume 171 (helpers: getSizeByCapacityRange 20, parseDevAttribs 15, isVolumeAccessModeSupport 10)
- ListVolumes 64, ListSnapshots 65, CreateSnapshot 60, ValidateVolumeCapabilities 44, GetCapacity 30, ControllerExpandVolume 30, DeleteSnapshot 15, DeleteVolume 14, ControllerGetCapabilities 6, ControllerPublish 4, ControllerUnpublish 4, ControllerGetVolume about 3 (last function, to EOF)

Neither is thin. Both own parameter parsing, capacity rounding and idempotency logic. The endpoint moves that into the daemon, so a front end can be about 350 lines (table below).

## 4. Minimal Go controller package (sketch)

Package `csizfs` (about 350 LOC, one file plus a mapping test):
- `controller.go`: 16 RPC methods (about 240 LOC). Each one validates CSI-only fields, calls the endpoint client, and converts the response.
- `convert.go`: `volumeToCSI`, `snapshotToCSI`, capability mapping (about 60 LOC).
- `errors.go`: passthrough (about 10 LOC).
- Tests: table-driven mapping plus a sanity harness over `unix:` (about 40 LOC of harness).

Mapping table:

| CSI field | Endpoint field |
|---|---|
| CreateVolume.name | CreateVolumeRequest.name |
| capacity_range.required_bytes / limit_bytes | capacity_bytes / limit_bytes |
| volume_capabilities[0].block vs mount | kind (BLOCK / FILESYSTEM) |
| access_mode | front-end check only (SINGLE_NODE_WRITER) |
| volume_content_source.snapshot.snapshot_id | source_snapshot_id |
| volume_content_source.volume.volume_id | source_volume_id |
| parameters (new `properties`) | properties (proposed) |
| Volume.device_path / mountpoint | volume_context (built here) |
| ExpandVolume.capacity_range | capacity_bytes / limit_bytes |
| CreateSnapshot.name, source_volume_id | name, source_volume_id |
| GetSnapshot.snapshot_id | ListSnapshots.snapshot_id, empty becomes NotFound |
| ListSnapshots filters, max_entries, starting_token | same names |
| ControllerGetCapabilities | GetCapabilities (enum to RPC type) |

Error passthrough: the endpoint returns a gRPC status whose code is the endpoint code. The front end returns that status unchanged, which avoids a mapping table. Exceptions, all front-end: empty ids and missing capacity ranges become InvalidArgument before the call. Unsupported access modes become InvalidArgument (spec `csi.proto` error table, line 526 and `:2546`). GetSnapshot empty becomes NotFound.

Front ends must not translate ABORTED to anything else, because the sidecars retry on it (`sidecar-conformance.md` EDGES).

## 5. Proposed proto changes

All additive. Under `buf breaking` FILE rules, new fields, new map fields and new enum values are non-breaking. Field numbers are new, nothing is renamed or removed.

```proto
message CreateVolumeRequest {
  // ... fields 1-8 unchanged
  // ZFS properties applied at create. Keys checked against a planner
  // allowlist (compression, recordsize, volblocksize, copies, sync,
  // logbias, atime). Compared in compatible(); mismatch is ALREADY_EXISTS.
  map<string, string> properties = 9;
}

enum Capability {
  // ... 1-7 unchanged
  CAPABILITY_PROPERTIES = 8;
}
```

Not proposed, and why:
- volume_context, topology, secrets, ControllerGetVolume, GetSnapshot RPC: the front end derives or ignores them, and sanity does not require them. Adding them would widen the contract for no test.
- ModifyVolume: gated off in sanity. Add later as a separate RPC.

The `gen/` files are checked in, so `buf generate` must run in the same change. `buf` is not on this machine, so `buf breaking` is unverified.

## 6. Planner and effect changes (data-oriented)

- Row: add typed columns `Compression string`, `Recordsize uint64`, `Copies uint8`, `Sync string`, `Logbias string`, `Atime bool` (volblocksize already exists as `BlockSz`). Decode in `decodeColumns`, adding names to `props.columns()` (`table.go:122`). One scan, no new `zfs` call.
- Planner: a pure `compatibleProps(row Row, want map[string]string) error`, called from `compatible()`. No new Op kinds: create and clone already carry `Props`, and `OpSet` covers any later change.
- Validation of values stays with ZFS (`ErrInvalidProperty` maps to InvalidArgument, `endpoint.go:190-207`). Unknown keys get InvalidArgument in the planner, not silent drops.
- Fix `plan.go:332` to bound the snapshot name at 128 bytes on its own, and keep the 255-byte dataset bound.
- Fix `PlanExpandVolume`: capacity 0 returns InvalidArgument (`plan.go:289-309`).
- Map ctx deadline and cancellation to `DeadlineExceeded` and `Canceled` in `zfsErr`. Add both codes to `errors.go`.

## 7. Test plan

- Planner table tests (`plan_test.go`): snapshot name at 128 bytes; properties match and mismatch; expand capacity 0; the new error mapping from ctx errors.
- Sim: run the csi-test `sanity.Test` against the front end over `unix:` with the in-memory zfstest backend. Advertise only what is implemented and run with the Node Service skipped. Record the skip list in the test.
- Real ZFS CI: the same sanity run on a file-backed pool with `ZFS_TEST_POOL`, plus a property case (`compression=zstd`, `recordsize=16K`) checked with `zfs get -Hp`.
- Golden tests for `csizfs` mapping: one case per RPC, asserting the endpoint request.

## 8. Risks

1. The Node Service block in csi-sanity (`node.go:174`) is the largest unknown. Without a node stub or a documented skip, "passes csi-sanity" is not achievable for a controller-only binary. Whether csi-test treats a missing Node client as a skip is unverified.
2. Sanity does not test StorageClass parameters (`TestVolumeParameters` is empty). A green run says nothing about compression or recordsize. Needs the property test above.
3. Internal vs DeadlineExceeded: until `zfsErr` maps ctx errors, a slow zfs exec becomes a final failure for the provisioner (`sidecar-conformance.md`).
4. Spec tension: block create with zero capacity (spec: SP chooses) versus the endpoint's rejection (`plan.go:93`). Decide and document.
5. Stale citations: `adversarial/csi-spec.md` cites `endpoint.go:390-421` (`adopt`) and `:613-624`. Neither exists at HEAD. Those verdicts were re-derived against the current code above.
6. Not run: `buf breaking`, `go test` on the real pool, and the csi-sanity harness end to end.
