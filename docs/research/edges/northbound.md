# ZFS endpoint: northbound edge

## EDGES

- **CSI to endpoint (gRPC, typed).** Prior art is mostly per-driver logic, not a shared typed API.
- **linstor-csi** (repo `linbit/linstor-csi` resolves to `piraeusdatastore/linstor-csi@4faad044`) keeps no state. It translates CSI RPCs into LINSTOR client calls. Per-RPC calls seen in `pkg/driver/driver.go`:
  - CreateVolume: `FindByID` (:573), `AccessibleTopologies` (:604), `CreateConsistencyGroupVolume` (:701), `FindSnapsByID` (:752), `RestoreConsistencyGroupVolume` (:776)
  - DeleteVolume: `Delete` (:805)
  - ControllerPublish: `FindByID` (:826), `NodeAvailable` (:849), `Attach` (:871)
  - ControllerUnpublish: `Detach` (:927)
  - ListVolumes: `ListAll` (:984)
  - ControllerExpand: `ControllerExpand` (:1441)
- **LOC of linstor-csi: not measured.** I did not get a line count, so I cannot state how thin it is.
- **linstor-server (ZFS layer):** `satellite/.../zfs/ZfsProvider.java:323-325` (snapshot options), `server/.../zfs/ZfsData.java:42`. I did not verify its REST routes.

## SEAMS

1. **Idempotency key and dedup.** Best existing example is linstor-csi. It derives the volume ID from the CSI name (`CompatibleVolumeId(req.GetName(), ...)`, :567), then calls `FindByID` (:573). On a hit it returns success only if the existing volume matches on size (:589-593), resource group (:596), and fstype (:581). Mismatches return `AlreadyExists` (:582, :590, :597). Ownership is checked via a property prefix `linstor-csi` (:585).
2. **Replica create by UUID (Mayastor).** `io-engine/src/lvs/lvs_store.rs:1202-1207` returns `RepExists` when a UUID already exists. `lvs_error.rs:145` and `grpc/v0/mayastor_grpc.rs:232` map that to `ALREADY_EXISTS`. The Python BDD test asserts `ALREADY_EXISTS` for a duplicate UUID (`test/python/v1/replica/test_bdd_replica.py:270-275`). A replay is therefore an error, not success. The share path does return the existing replica when the protocol matches (`grpc/v1/replica.rs:272-274`).
3. **Snapshot idempotency (Synology).** `pkg/driver/controllerserver.go:456-461` checks the snapshot name and returns `AlreadyExists` if the volume ID is incompatible. Volume create keys on `req.GetName()` (:108) and returns `AlreadyExists` on a capacity mismatch (:257). `lookup_failure_test.go:128` checks that provisioning does not run when the lookup fails.
4. **Replication protocol (zrepl).** `internal/replication/logic/pdu/pdu.proto:4-14` defines `Replication` with `ListFilesystemVersions` returning Snapshot/Bookmark with GUID (:30-40). This is a reusable message shape for snapshot listing. It has no request IDs and no CSI-style operations.

## GAPS

- **No ZFS-native typed gRPC volume daemon found.** My search for one returned an error, so this remains open.
- **Pass-through of the CSI name as a backend key is unverified.** Synology `spec.BackendName` (`dsm.go:278`) is set upstream, and I did not trace where it comes from.
- **Error mapping is inconsistent.** Mayastor maps to `ALREADY_EXISTS` on replay. linstor-csi returns `Internal` for lookup failures (:576) and `AlreadyExists` for mismatches. Neither defines a retry contract.
- **No request-ID idempotency.** All three key on name or UUID. None found a request-ID dedup layer.
- **zrepl protos are replication-only.** They are not a volume lifecycle API.
