# zfs-endpoint executable spec (wave8)

Generated from `wave8-tools/rows.py` by `build_spec.py`; 175 deduplicated rows against HEAD 9f1790d. Machine form: `wave8/spec.json`. Go table skeleton: `wave8/spec_rows.go.txt`.

Fields: **drive** = what the residual test is after the refactor (type: compile-time plus one constructor test; table: a SpecRow; scenario: a multi-step or real-kernel test). **elim** = whether a type can make the violation unrepresentable. **status**: current (done and asserted), current-untested, open-finding (HEAD violates it), decided-pending (ADR/owner decided, not built). **oracle**: where the expected value must come from; `self` means our own code or model is the oracle (common-mode risk).


**By kind**

| value | rows |
|---|---|
| invariant | 56 |
| idempotency | 9 |
| error-code | 20 |
| ownership | 19 |
| crash-convergence | 11 |
| OpenZFS-fact | 28 |
| CSI-contract | 26 |
| perf | 6 |

**By drive**

| value | rows |
|---|---|
| table | 100 |
| scenario | 42 |
| type | 33 |

**By type_eliminable**

| value | rows |
|---|---|
| none | 86 |
| partial | 56 |
| full | 33 |

**By status**

| value | rows |
|---|---|
| current | 112 |
| decided-pending | 26 |
| open-finding | 25 |
| current-untested | 12 |

**By oracle**

| value | rows |
|---|---|
| adr | 73 |
| openzfs | 56 |
| csi-spec | 34 |
| self | 7 |
| nfs-utils | 5 |

**Kind x drive**

| kind | type | table | scenario |
|---|---|---|---|
| invariant | 20 | 29 | 7 |
| idempotency | 0 | 8 | 1 |
| error-code | 3 | 13 | 4 |
| ownership | 2 | 11 | 6 |
| crash-convergence | 0 | 3 | 8 |
| OpenZFS-fact | 7 | 9 | 12 |
| CSI-contract | 1 | 23 | 2 |
| perf | 0 | 4 | 2 |

Rows with no covering test: 57. Scenario rows whose oracle is upstream (must run against real OpenZFS/nfs-utils, never the simulator alone): OWN-06, OWN-08, OWN-12, OWN-15, CLN-02, DEL-03, DEL-05, DEL-08, SNP-11, ERR-02, SCN-14, NFS-05, NFS-06, NFS-11, NFS-14, NFS-15, NFS-16, NFS-21, REC-12, CR-02, CR-03, CR-04, PRF-05.


## OWN

### OWN-01 (ownership; table; elim partial; current; oracle openzfs)

Ownership is read only from user properties whose source is exactly `local`; `received`, `inherited from X`, `default`, `temporary` and `-` never confer ownership.

- source: README.md:30,62,81; docs/adr/0003:21; internal/zfs/zfs_test.go:202
- tests: TestPropLocalIsOnlyLocal, TestInheritedOwnershipIsNotOwnership, TestReceivedCopyIsNotOwned
- type: Decode `source` into a closed enum `Src{Local,Received,Inherited,Default,Temporary,None}`; only `decode` may build an `Owned` value and only from `Src.Local`. Residual: the source-string table must match what zfs prints.

### OWN-02 (ownership; table; elim partial; current; oracle adr)

A volume row is owned only when `managed_resource=true` is local AND local `csi_volume_name` equals its id; a renamed or re-marked alias is foreign: not listed, Get/Expand/CreateSnapshot NotFound, Delete FailedPrecondition, untouched.

- source: README.md:63; internal/endpoint/ownership_test.go:118
- tests: TestVolumeNameMustMatchID, TestReconcileSparesReceivedAndRenamed
- type: `State` column derived once at decode (lifecycle-fsm §5.1); planners accept `OwnedVol` handles only. Residual: the derivation table (M,C,VN,Src)->State.

### OWN-03 (ownership; type; elim full; open-finding; oracle adr)

A row with local `managed_resource` but no `csi_volume_name` (FSM I2) or any other impossible marker combination (I1-I13) is Invalid: refused everywhere with FailedPrecondition and reported by Reconcile, never treated as owned.

- source: scratchpad/wave5/lifecycle-fsm.md §1c, §6 R2
- tests: none
- type: `State` enum with `StInvalid{Why}`; the exhaustive (State,Request) table maps StInvalid to an error cell, so no planner body can see it.

### OWN-04 (ownership; table; elim partial; current; oracle adr)

A snapshot is a CSI snapshot only when managed locally, `csi_snapshot_name` is set locally, and `csi_snapshot_source_volume_id` is empty or equals its volume; anything else is foreign (not listed, DeleteSnapshot FailedPrecondition).

- source: scratchpad/wave5/lifecycle-fsm.md §4; scratchpad/wave5/impossible-reach.md F11
- tests: TestInheritedOwnershipIsNotOwnership, TestReceivedCopyIsNotOwned, TestDeleteVolumeSparesForeignSnapshots
- type: Same `State` derivation (StSnapCSI vs StSnapForeign).

### OWN-05 (ownership; table; elim partial; current; oracle adr)

An unmanaged dataset at a volume's name is never adopted or deleted: CreateVolume AlreadyExists, DeleteVolume FailedPrecondition, the dataset is unchanged.

- source: internal/endpoint/endpoint_test.go:166; docs/adr/0003:21
- tests: TestNeverAdoptsOrDeletesUnmanaged, TestPlanCreateRefusesForeignAndIncompatible
- type: StForeign cells are error-only in the FSM table. Residual: a fixture per foreign shape.

### OWN-06 (ownership; scenario; elim partial; current; oracle openzfs)

A received copy (`zfs send -R | zfs recv`) of an owned volume is not the endpoint's: not listed, GetVolume NotFound, DeleteVolume/DeleteSnapshot FailedPrecondition, untouched by Reconcile, and its snapshots do not shadow the original's CSI snapshot names.

- source: README.md:62; docs/adr/0003; internal/endpoint/ownership_test.go:151
- tests: TestReceivedCopyIsNotOwned, TestReconcileSparesReceivedAndRenamed, FuzzReconcileSafe
- type: Covered by OWN-01's Src enum; residual is a scenario because only `zfs recv` (or Sim.Receive) produces the shape.

### OWN-07 (ownership; table; elim partial; current; oracle adr)

Clone/restore sources must be a committed owned volume or a CSI snapshot of one; a foreign dataset, an admin snapshot, an internal clone-source snapshot or an uncommitted volume is NotFound, and nothing is snapshotted or cloned on it.

- source: README.md:64; internal/endpoint/ownership_test.go:16; scratchpad/adv3/csi.md #1; scratchpad/dod3/type12.md D09
- tests: TestCloneSourcesMustBeOwned
- type: `PlanClone(src CommittedVol | CSISnap)`: the planner signature cannot take a foreign row. Residual: scan-time only (see OWN-15).

### OWN-08 (ownership; scenario; elim partial; current; oracle openzfs)

A clone never adopts an existing dataset at its target: `OpClone` has no OkExists; a dataset created in the scan-to-clone window gives AlreadyExists and is not mutated (no refquota, no markers).

- source: README.md:64,83; internal/endpoint/ownership_test.go:86; scratchpad/wave5/impossible-reach.md F4; scratchpad/wave5/lifecycle-fsm.md CX3
- tests: TestCloneNeverAdoptsARacingDataset
- type: Op variant `Clone` has no tolerance field (only `CreateSnapshot(internal)` and `Destroy` carry tolerances). The race itself is runtime: scenario.

### OWN-09 (OpenZFS-fact; type; elim full; current-untested; oracle openzfs)

Volumes are never created with `zfs create -p`, because it exits 0 on an existing dataset and the plan would stamp ownership onto someone else's.

- source: README.md:55; docs/adr/0003:40
- tests: none
- type: The volume-create op/client call has no `parents` parameter (today `CreateFilesystem(ctx,name,props,parents bool)` accepts true).

### OWN-10 (ownership; scenario; elim none; current; oracle adr)

DeleteVolume refuses a volume carrying snapshots the endpoint did not make (sanoid, admin, received), naming each, unless `-destroy-foreign-snapshots`; Reconcile skips such volumes with that reason and never inherits the opt-in.

- source: README.md:65; docs/adr/0003; internal/endpoint/foreign_test.go:16; scratchpad/adv4/reconcile.md F4
- tests: TestDeleteVolumeSparesForeignSnapshots
- type: Depends on what exists at destroy time (OWN-15); a type only proves scan-time absence.

### OWN-11 (ownership; table; elim partial; current; oracle adr)

The endpoint's own internal clone-source snapshot is not a foreign snapshot: a source volume whose clone is gone deletes normally.

- source: internal/endpoint/foreign_test.go:75
- tests: TestDeleteVolumeAfterItsCloneIsGone
- type: StSnapCloneSrc* states distinct from StSnapForeign.

### OWN-12 (ownership; scenario; elim none; open-finding; oracle openzfs)

Unowned child datasets under a volume are never destroyed: the scan keeps deeper rows as dependents, and delete refuses (FailedPrecondition) or destroys without `-r` so zfs itself refuses.

- source: scratchpad/adv4/reconcile.md F5; scratchpad/wave5/impossible-reach.md F1; internal/endpoint/table.go:292; internal/endpoint/plan.go:410
- tests: none
- type: Children appear only on a real tree; needs a fixture with a child dataset and the non-recursive destroy.

### OWN-13 (ownership; table; elim partial; open-finding; oracle adr)

Clone-source snapshots are recognised by a stamp (`managed` + `clone_source_for=<id>` set at cut time), not by the `volume-source-for-volume-` name prefix; a user snapshot with that prefix is never collected.

- source: scratchpad/adv4/reconcile.md F6; internal/endpoint/reconcile_fuzz_test.go:140-147 (oracle codifies the bug)
- tests: none
- type: StSnapCloneSrc derived from the stamp, not the name. Residual: fixture with a prefix-named user snapshot.

### OWN-14 (invariant; type; elim full; open-finding; oracle adr)

Every op target and every lock key is under the configured parent; an origin is released only when it is exactly `parent/<src>@volume-source-for-volume-<this id>` with `<src>` an owned volume.

- source: scratchpad/adv4/reconcile.md F7; scratchpad/wave5/impossible-reach.md F6 (INV2)
- tests: none
- type: `Target` built only from a validated parent-relative `VolID`/`SnapID` (no constructor from a raw zfs name); origin parsed into (VolID, CloneSrcFor) or rejected.

### OWN-15 (ownership; scenario; elim none; open-finding; oracle openzfs)

Ownership proven at scan time can change before apply (snapshot, child or rename added out of band): destructive ops must fail closed on the live tree (non-recursive destroy, explicit snapshot list, or a guid/`managed`-checked channel program), never act on the scan's verdict alone.

- source: scratchpad/wave5/lifecycle-fsm.md CX2; scratchpad/wave5/impossible-reach.md F16; scratchpad/wave6/csi-nfs-design.md §8 C8
- tests: none
- type: TOCTOU: a type proves a property of the snapshot that was read, not of the pool at exec time.

### OWN-16 (ownership; type; elim full; current; oracle openzfs)

The default property namespace is `zfs-endpoint:`; `-property-prefix democratic-csi` is adoption mode; a prefix is refused at startup unless every derived name is a valid OpenZFS user property (`a-z 0-9 - _ . :`, under 256 bytes with the suffix).

- source: README.md:66; internal/endpoint/prefix_test.go:15,34
- tests: TestDefaultPropertyPrefixIsOurOwn, TestPropertyPrefixIsValidated
- type: `Prefix` newtype with a validating constructor; the only way to name a marker is `Prefix.Prop(Marker)`.

### OWN-17 (ownership; table; elim none; current; oracle adr)

Under the default prefix democratic-csi volumes are foreign; under the adoption prefix they are ordinary committed volumes (same behaviour, provenance only).

- source: scratchpad/wave5/lifecycle-fsm.md §1a, §4
- tests: TestFileExportAdoptedVolumes, TestCloneNeverAdoptsARacingDataset
- type: Configuration-dependent; run the ownership table under both prefixes.


## VOL

### VOL-01 (idempotency; table; elim none; current; oracle self)

For any request the planner accepts, a second identical CreateVolume succeeds, returns an identical Volume and performs zero zfs mutations (idempotency = empty plan).

- source: README.md:32,98; internal/endpoint/converge_test.go:19
- tests: FuzzCreateConverges, TestCreateVolumeIdempotent, TestCreateVolumeProperties, TestThickCloneRetryIsIdempotent, TestReserveRestoreIsIdempotent, TestRoundTripAndCodes
- type: Behavioural; FuzzCreateConverges never sets a content source, so clones are covered only by hand tests.

### VOL-02 (invariant; table; elim partial; current; oracle adr)

Capacity is rounded up to the effective volblocksize (requested, else configured default), never below the request; the returned capacity is >= required and <= limit.

- source: internal/endpoint/endpoint_test.go:111; internal/endpoint/props_test.go:354
- tests: TestCreateVolumeIdempotent, TestPlanCreateAppliesProperties, FuzzCreateConverges
- type: `Bytes` type with checked `RoundUp(BlockSize)`; `BlockSize` only constructible as a power of two in [512,128K].

### VOL-03 (invariant; table; elim none; current; oracle adr)

A limit-only request sets `refquota=limit`; it never creates an unlimited filesystem.

- source: internal/endpoint/plan_test.go:94
- tests: TestPlanCreateHonorsLimitOnly

### VOL-04 (error-code; table; elim partial; current; oracle csi-spec)

A rounded size above limit_bytes, or required > limit, is OutOfRange/InvalidArgument before any op.

- source: scratchpad/adv3/csi.md 'Rounding' (checked clean); internal/endpoint/props_test.go:361
- tests: TestPlanCreateAppliesProperties
- type: `CapacityRange` constructor rejects required>limit; rounding check remains.

### VOL-05 (error-code; table; elim none; current; oracle csi-spec)

An incompatible re-create of a committed volume (larger size, other kind, limit below current, sparse vs thick, different properties) is AlreadyExists with no ops.

- source: internal/endpoint/endpoint_test.go:123-135; internal/endpoint/plan_test.go:75
- tests: TestCreateVolumeIdempotent, TestPlanCreateRefusesForeignAndIncompatible, TestParametersAndAccessChecks, TestRoundTripAndCodes

### VOL-06 (idempotency; table; elim none; open-finding; oracle adr)

Capacity compatibility of a committed row: have >= required, and have <= limit when a limit is set; an unlimited filesystem (refquota 0, adopted) satisfies `required` when no limit is set.

- source: scratchpad/dod3/type12.md D07; internal/endpoint/plan.go (compat)
- tests: none

### VOL-07 (invariant; table; elim partial; current; oracle adr)

The commit marker `provision_success=true` is the last op of every create plan; uncommitted rows are never listed or returned as volumes.

- source: README.md:58; internal/endpoint/endpoint_test.go:119,153
- tests: TestCreateVolumeIdempotent, TestCreateVolumeResumesInterruptedProvision, TestPlanCreateConvergesUncommittedClone
- type: FSM cell `Ops` ends in `Commit`; `kindsMatch` assertion. Residual: per-cell check.

### VOL-08 (crash-convergence; scenario; elim none; current; oracle adr)

An owned uncommitted row (interrupted provision) is driven toward the request (sizes and properties set, then commit), not judged for compatibility.

- source: README.md:33; internal/endpoint/endpoint_test.go:138
- tests: TestCreateVolumeResumesInterruptedProvision, TestPlanCreatePropertiesConvergeUncommitted, TestCreateVolumeProperties, TestPlanCreateConvergesUncommittedClone

### VOL-09 (invariant; type; elim full; open-finding; oracle adr)

Uncommitted volumes are not served: GetVolume, ExpandVolume, CreateSnapshot and PublishFileExport on an uncommitted row are NotFound (FSM I14).

- source: scratchpad/wave5/lifecycle-fsm.md I14, §6 R2; scratchpad/wave5/impossible-reach.md F12
- tests: none
- type: Those planners take `CommittedVol`; only CreateVolume/Reconcile take `UncommittedVol`.

### VOL-10 (ownership; table; elim partial; current; oracle openzfs)

New filesystems get `sharenfs=off` and `sharesmb=off` set locally (a parent shared to `*` must not export them); zvols get no share properties.

- source: internal/endpoint/plan.go:205-208; internal/endpoint/plan_test.go:187
- tests: TestPlanCreateFilesystemsAreNotShared, TestPlanCreateAppliesProperties
- type: Per-kind create-op structs: `CreateFS{Share ShareValue}` requires a share value, `CreateZvol` has none.

### VOL-11 (OpenZFS-fact; type; elim full; current; oracle openzfs)

A thick zvol's `refreservation` is never hand-set (ZFS's zvol_auto_resv includes metadata and copies and tracks volsize); converging a thick zvol sets `refreservation=auto`.

- source: README.md:53; docs/adr/0003:38; internal/endpoint/plan.go:337
- tests: TestPlanCreateThickZvolLeavesReservationToZFS, TestPlanCreateConvergesUncommittedClone
- type: `ZvolReservation` enum {Sparse, Auto}: there is no byte-valued reservation for zvols in the op vocabulary.

### VOL-12 (invariant; type; elim full; current; oracle adr)

A sparse zvol has refreservation 0; `sparse` on a filesystem and `reserve` on a zvol are InvalidArgument.

- source: scratchpad/adv3/csi.md 'Parameters'; internal/zfs/zfs_test.go:102
- tests: TestLifecycleAgainstSim, TestParametersAndAccessChecks
- type: Request is a sum type: `FSReq{Reserve}` | `ZvolReq{Sparse}`.

### VOL-13 (invariant; table; elim none; current; oracle adr)

A filesystem with Reserve gets refreservation = refquota (including restores that take their size from the source).

- source: internal/endpoint/clonesize_test.go:86
- tests: TestReserveRestoreIsIdempotent, TestCloneFailureAfterCloneConvergesToSameState

### VOL-14 (idempotency; scenario; elim none; current; oracle adr)

Concurrent identical CreateVolumes serialize on the volume lock: all succeed and exactly one `zfs create` runs.

- source: internal/endpoint/endpoint_test.go:312
- tests: TestConcurrentCreatesSerialize
- type: Concurrency.

### VOL-15 (CSI-contract; type; elim full; current-untested; oracle csi-spec)

A volume id is the CSI name: non-empty, <= 128 bytes, a valid zfs component with no `/` or `@`; an invalid name on CreateVolume is InvalidArgument.

- source: docs/adr/0002:7; internal/endpoint/endpoint.go:214
- tests: none
- type: `VolID` newtype, constructor `ParseName`; every planner takes `VolID`.

### VOL-16 (OpenZFS-fact; table; elim partial; current; oracle openzfs)

volblocksize: the requested value rounds volsize; a clone/restore keeps its origin's block size, so `-o volblocksize` is never passed to `zfs clone` and a mismatching request is InvalidArgument.

- source: internal/endpoint/endpoint_test.go:492-517; internal/endpoint/props_test.go:366; scratchpad/adv3/props.md (libzfs_dataset.c:1006-1010)
- tests: TestCreateVolumeProperties, TestPlanCreateAppliesProperties
- type: `Clone` op props type excludes create-only props (`CloneProps` subset of `Props`).

### VOL-17 (idempotency; table; elim none; current; oracle openzfs)

volblocksize cannot be converged on an uncommitted row: a mismatch is AlreadyExists; a match plans only the commit.

- source: internal/endpoint/props_test.go:431
- tests: TestPlanCreatePropertiesConvergeUncommitted

### VOL-18 (CSI-contract; table; elim none; current; oracle openzfs)

A block volume's device path is `/dev/zvol/<dataset>`; a filesystem's export path is its mountpoint.

- source: internal/server/server_test.go:405; internal/endpoint/export_contract_test.go:78
- tests: TestRoundTripAndCodes, TestFileExportLifecycle

### VOL-19 (invariant; type; elim full; open-finding; oracle adr)

Capacities above MaxInt64 are refused at the API boundary and size rounding never wraps (OutOfRange instead of a 1 MiB 'success').

- source: scratchpad/wave5/impossible-reach.md F10
- tests: none
- type: `Bytes` is a checked uint63; `RoundUp` returns (Bytes, ok).


## CLN

### CLN-01 (invariant; table; elim none; current; oracle adr)

Restore from a snapshot honours growth (capacity = request) and records the content source (`csi_volume_content_source_type/id`); the Volume reports SourceType/SourceID.

- source: internal/endpoint/endpoint_test.go:232
- tests: TestSnapshotLifecycleWithClones

### CLN-02 (invariant; scenario; elim none; current; oracle openzfs)

Clone-from-volume cuts `<src>@volume-source-for-volume-<dst>`; the source cannot be deleted while the clone exists (FailedPrecondition); deleting the clone releases the internal snapshot.

- source: internal/endpoint/endpoint_test.go:268; internal/endpoint/plan_test.go:112
- tests: TestCloneFromVolumeReleasesInternalSnapshot, TestPlanDeleteVolume, TestReconcileCollectsLeakedCloneSource

### CLN-03 (CSI-contract; table; elim none; current; oracle csi-spec)

A clone/restore whose source is larger than limit_bytes is OutOfRange before any op (no clone-source snapshot is cut), on every attempt.

- source: internal/endpoint/clonesize_test.go:56; scratchpad/adv3/csi.md #2
- tests: TestCloneLargerThanLimitIsOutOfRange

### CLN-04 (invariant; table; elim none; current; oracle adr)

A clone with capacity 0 takes the source's size (refquota, and with Reserve also refreservation); Reserve from an unlimited source is InvalidArgument.

- source: internal/endpoint/clonesize_test.go:88
- tests: TestReserveRestoreIsIdempotent

### CLN-05 (crash-convergence; scenario; elim none; current; oracle self)

A failure or crash after `zfs clone` converges on retry to the end state of an uninterrupted run (same refquota and refreservation, same response).

- source: internal/endpoint/clonesize_test.go:121; scratchpad/wave5/lifecycle-fsm.md CX4
- tests: TestCloneFailureAfterCloneConvergesToSameState, TestCloneResizeFailureConverges, TestPlanCreateConvergesUncommittedClone

### CLN-06 (error-code; table; elim partial; current-untested; oracle adr)

A request kind that differs from the source kind (filesystem request from a zvol snapshot) is InvalidArgument before any op, even when the source volume row is missing from the scan.

- source: scratchpad/wave5/impossible-reach.md F8
- tests: none
- type: `CSISnap` carries its volume kind from decode.

### CLN-07 (error-code; type; elim full; current-untested; oracle adr)

A content source that is deferred-destroyed (deleted CSI snapshot) is NotFound.

- source: scratchpad/wave5/lifecycle-fsm.md §2a
- tests: none
- type: StSnapCSIDeferred is not a `CSISnap` handle.

### CLN-08 (CSI-contract; table; elim partial; current; oracle csi-spec)

A content-source id the endpoint could never have issued, or that names nothing, is NotFound (never InvalidArgument).

- source: internal/endpoint/plan_test.go:35; internal/endpoint/endpoint_test.go:259
- tests: TestPlanCreateMalformedSourceIsNotFound, TestSnapshotLifecycleWithClones
- type: ID parse returns `NeverIssued` variant mapped once.

### CLN-09 (invariant; scenario; elim none; current-untested; oracle adr)

A clone locks its source volume (and the source snapshot id for a restore) before scanning, so the source cannot be collected or deleted mid-clone.

- source: README.md:35; docs/adr/0002:13
- tests: none
- type: Concurrency.


## DEL

### DEL-01 (CSI-contract; table; elim none; current; oracle csi-spec)

DeleteVolume of a missing volume is OK, and so is a second delete.

- source: internal/endpoint/endpoint_test.go:180
- tests: TestDeleteVolume, TestPlanDeleteVolume, TestRoundTripAndCodes

### DEL-02 (CSI-contract; table; elim none; current; oracle csi-spec)

DeleteVolume refuses (FailedPrecondition) while a live (non-deferred) CSI snapshot exists, leaving every dataset unchanged.

- source: README.md:40,76; internal/endpoint/endpoint_test.go:192
- tests: TestDeleteVolume, TestPlanDeleteVolume, TestRoundTripAndCodes

### DEL-03 (OpenZFS-fact; scenario; elim none; current; oracle openzfs)

DeleteVolume refuses (FailedPrecondition) a volume with a dependent clone, including a clone outside the parent (zfs `destroy_check_dependent` refuses before destroying anything; the snapshot `clones` property is the native reverse index).

- source: internal/endpoint/plan_test.go:112; scratchpad/dod3/existing.md F2
- tests: TestPlanDeleteVolume, TestCloneFromVolumeReleasesInternalSnapshot
- type: Clones outside the scanned tree are invisible to any Table type.

### DEL-04 (invariant; table; elim partial; current; oracle adr)

A deferred (already deleted) CSI snapshot does not block its volume's delete or Reconcile.

- source: internal/endpoint/plan_test.go:154
- tests: TestPlanDeleteVolume, TestPlanReconcile
- type: StSnapCSIDeferred distinct state.

### DEL-05 (OpenZFS-fact; scenario; elim none; current; oracle openzfs)

A deferred-destroyed snapshot survives while it has clones and is reaped when the last clone is destroyed (no leak).

- source: internal/endpoint/endpoint_test.go:241-257; internal/zfs/zfs_test.go:114; internal/endpoint/reconcile_test.go:279
- tests: TestSnapshotLifecycleWithClones, TestLifecycleAgainstSim, TestSimulateDestroyReapsDeferredOrigins
- type: Kernel semantics (dsl_destroy rmorigin).

### DEL-06 (crash-convergence; scenario; elim partial; open-finding; oracle self)

A crash after any op of DeleteVolume converges on retry with no leaked clone-source snapshot (FSM CX1): order is `destroy -d` origin first, then each judged snapshot, then the dataset without `-r`.

- source: scratchpad/wave5/lifecycle-fsm.md CX1, §6 R1; internal/endpoint/plan.go:410-412
- tests: none
- type: FSM cell for Delete declares `Mid` states; per-cell crash test.

### DEL-07 (error-code; table; elim partial; current; oracle csi-spec)

`dataset is busy` and `it's being held` on a destroy are FailedPrecondition (CSI 'in use').

- source: README.md:40,68; internal/endpoint/opcodes_test.go:26
- tests: TestBusyIsMappedByOperation
- type: Code keyed by (OpKind, Kind).

### DEL-08 (ownership; scenario; elim none; open-finding; oracle openzfs)

When the id's name is absent but a row with a local `csi_volume_name`/`csi_snapshot_name` equal to it exists (zfs rename/promote), Delete is FailedPrecondition naming the new location, never OK.

- source: scratchpad/dod3/type12.md D06
- tests: none


## SNP

### SNP-01 (idempotency; table; elim none; current; oracle csi-spec)

CreateSnapshot is idempotent for the same (name, source): same id, size and ready_to_use, no ops.

- source: internal/endpoint/endpoint_test.go:221
- tests: TestSnapshotLifecycleWithClones, TestPlanSnapshotLongAndUnusualNames, TestReceivedCopyIsNotOwned

### SNP-02 (CSI-contract; table; elim partial; current; oracle csi-spec)

CSI snapshot names are global per endpoint: the same name on another volume is AlreadyExists (requires a whole-parent table).

- source: internal/endpoint/endpoint_test.go:225; docs/adr/0001 consequences; internal/endpoint/coverage_test.go:325
- tests: TestSnapshotLifecycleWithClones, TestPlannersRequireACoveringTable
- type: PlanCreateSnapshot takes `WholeTable`.

### SNP-03 (CSI-contract; table; elim partial; current; oracle csi-spec)

Snapshot id is `<vol>@<name>` when the name is a valid component and the id fits 128 bytes, else `<vol>@csi-<32 hex of sha256(name)>`; an id still over 128 bytes is InvalidArgument; a retry re-finds the hashed snapshot.

- source: docs/adr/0002 §Context; internal/endpoint/plan.go:479-516; internal/endpoint/plan_test.go:167
- tests: TestPlanSnapshotLongAndUnusualNames
- type: `SnapID` newtype built only by `id(vol,name)`; residual: hash-collision/legacy table.

### SNP-04 (error-code; type; elim full; current; oracle csi-spec)

A snapshot name that is empty, over 128 bytes or contains control characters is InvalidArgument.

- source: internal/endpoint/plan.go:472; internal/endpoint/plan_test.go:714
- tests: TestPlanSnapshotLongAndUnusualNames
- type: `SnapName` newtype.

### SNP-05 (error-code; type; elim full; current; oracle adr)

A CSI snapshot name with the reserved prefix `volume-source-for-volume-` is InvalidArgument.

- source: internal/endpoint/plan_test.go:158
- tests: TestPlanSnapshotReservedPrefix
- type: `SnapName` constructor refuses the prefix.

### SNP-06 (OpenZFS-fact; type; elim full; current; oracle openzfs)

Snapshot delete uses `zfs destroy -d`: deleting a cloned snapshot succeeds, keeps the data, hides it from ListSnapshots, and zfs reaps it with the last clone.

- source: README.md:54; docs/adr/0003:39; internal/endpoint/endpoint_test.go:241
- tests: TestSnapshotLifecycleWithClones
- type: `DestroySnap` op has no non-deferred form.

### SNP-07 (CSI-contract; table; elim none; current; oracle csi-spec)

DeleteSnapshot of a missing, deferred or never-issued id is OK (requires a covering table).

- source: README.md:39; internal/endpoint/endpoint_test.go:262; internal/endpoint/coverage_test.go:310
- tests: TestSnapshotLifecycleWithClones, TestPlannersRequireACoveringTable

### SNP-08 (error-code; table; elim partial; current; oracle adr)

DeleteSnapshot of a snapshot the endpoint did not make (admin, inherited markers, received) is FailedPrecondition and leaves it.

- source: internal/endpoint/endpoint_test.go:386; internal/endpoint/ownership_test.go:192
- tests: TestInheritedOwnershipIsNotOwnership, TestReceivedCopyIsNotOwned
- type: StSnapForeign error cell.

### SNP-09 (invariant; table; elim partial; current; oracle adr)

ListSnapshots lists only owned, non-deferred CSI snapshots.

- source: internal/endpoint/endpoint_test.go:248,382; internal/endpoint/ownership_test.go:179
- tests: TestSnapshotLifecycleWithClones, TestInheritedOwnershipIsNotOwnership, TestReceivedCopyIsNotOwned
- type: Visibility derived from State.

### SNP-10 (error-code; table; elim none; current-untested; oracle adr)

CreateSnapshot whose name matches a deferred-destroyed CSI snapshot is AlreadyExists.

- source: scratchpad/wave5/lifecycle-fsm.md §2a
- tests: none

### SNP-11 (OpenZFS-fact; scenario; elim none; current; oracle openzfs)

A snapshot's size is the volume size at snapshot time (a zvol snapshot keeps its volsize after the zvol grows).

- source: internal/endpoint/endpoint_test.go:218; CHECKPOINT 'sim zvol snapshot volsize follows live zvol (open)'
- tests: TestSnapshotLifecycleWithClones
- type: Simulator is known wrong here: only a real pool is an oracle.

### SNP-12 (CSI-contract; table; elim partial; current; oracle csi-spec)

CreateSnapshot from a never-issued or missing source id is NotFound.

- source: internal/csi/controller_test.go:171
- tests: TestNeverIssuedIDs
- type: ID parse variant.

### SNP-13 (CSI-contract; table; elim partial; decided-pending; oracle adr)

Long snapshot ids (ADR 0002): `zs1-` + sha256 hex of (vol, csi name); disjoint from short ids; ambiguous resolution is FailedPrecondition; lock order resolve, lock, rescan, verify same name, else Aborted.

- source: docs/adr/0002 §Decision
- tests: none
- type: `SnapID` sum type {Short, Long}.

### SNP-14 (invariant; table; elim partial; current; oracle adr)

CreateSnapshot and ExpandVolume refuse a volume whose markers name another volume (alias) or that is foreign: NotFound.

- source: internal/endpoint/ownership_test.go:130-137
- tests: TestVolumeNameMustMatchID
- type: `OwnedVol` handle.


## EXP

### EXP-01 (invariant; table; elim partial; current; oracle adr)

ExpandVolume rounds up to the zvol's volblocksize and returns the new capacity.

- source: internal/endpoint/endpoint_test.go:294; internal/endpoint/plan_test.go:143
- tests: TestExpand, TestPlanExpand
- type: `Bytes.RoundUp`.

### EXP-02 (idempotency; table; elim none; current; oracle csi-spec)

A shrink request is a no-op that returns the current capacity.

- source: internal/endpoint/endpoint_test.go:304
- tests: TestExpand

### EXP-03 (CSI-contract; table; elim none; current; oracle csi-spec)

Expanding an unlimited filesystem plans nothing, and the CSI response reports capacity_bytes = required (never 0).

- source: internal/endpoint/plan_test.go:143; internal/csi/controller_test.go:181; scratchpad/adv3/csi.md #8
- tests: TestPlanExpand, TestExpandUnlimitedFilesystem

### EXP-04 (CSI-contract; table; elim none; current; oracle csi-spec)

node_expansion_required is true only for a zvol with a mount capability or no capability given; false for raw block and filesystems.

- source: internal/csi/controller_test.go:53
- tests: TestExpandNodeExpansionRequired

### EXP-05 (invariant; type; elim full; current; oracle openzfs)

Expand never plans a volsize below the real one: a size cell zfs could not have printed (`-`, `none`, `1G`, negative, >2^64, not a volblocksize multiple) fails the scan as Internal instead of reading as 0.

- source: internal/endpoint/decode_test.go:11; scratchpad/wave5/impossible-reach.md F2
- tests: TestDecodeRejectsImpossibleZvolSizes
- type: Strict decode into `Size` (no zero default; `Prop.Uint` returning 0 for `-` is unrepresentable).


## LST

### LST-01 (CSI-contract; table; elim none; current; oracle csi-spec)

ListVolumes pages in id order with max_entries and a next token; an unknown starting token is Aborted; negative max_entries is InvalidArgument.

- source: internal/endpoint/endpoint_test.go:341; internal/server/server_test.go:443
- tests: TestPagination, TestRoundTripAndCodes

### LST-02 (invariant; table; elim partial; current; oracle adr)

ListVolumes returns only committed owned volumes (no uncommitted, foreign, received or aliased rows).

- source: internal/endpoint/endpoint_test.go:153; internal/endpoint/ownership_test.go:139,175
- tests: TestCreateVolumeResumesInterruptedProvision, TestVolumeNameMustMatchID, TestReceivedCopyIsNotOwned
- type: Visibility derived from State.

### LST-03 (CSI-contract; table; elim none; current; oracle csi-spec)

GetVolume of a missing volume is NotFound.

- source: internal/server/server_test.go:449
- tests: TestRoundTripAndCodes

### LST-04 (CSI-contract; table; elim none; open-finding; oracle csi-spec)

GetCapacity reports 0 when the requested capabilities or parameters could not be provisioned.

- source: scratchpad/adv3/csi.md #6
- tests: none


## ID

### ID-01 (CSI-contract; table; elim partial; current; oracle csi-spec)

An id the endpoint could never have issued (full dataset path, > 128 bytes, `@` in a volume id) names nothing: DeleteVolume and DeleteSnapshot OK; GetVolume, Validate, Expand, CreateSnapshot source and CreateVolume source NotFound; an empty id stays InvalidArgument.

- source: README.md:39; internal/endpoint/endpoint.go:221-233; internal/csi/controller_test.go:155; scratchpad/dod3/type12.md D14
- tests: TestNeverIssuedIDs, TestPlanCreateMalformedSourceIsNotFound, TestSnapshotLifecycleWithClones
- type: `ParseID` returns {Issued(VolID), NeverIssued, Empty}; one mapping table per RPC. Note PublishFileExport('a/b') is InvalidArgument today (TestFileExportPreconditions) - decide.


## ERR

### ERR-01 (error-code; table; elim partial; current; oracle openzfs)

Only a classified not-found counts as absent; any unclassified error from Exists or a scan is an error, never absence.

- source: README.md:51; docs/adr/0003:37; internal/zfs/zfs_test.go:52
- tests: TestExistsDoesNotSwallowUnknownErrors, TestClassify
- type: Tri-state `Presence{Present,Absent,Unknown}` with no bool conversion.

### ERR-02 (error-code; scenario; elim none; current; oracle openzfs)

A missing or exported pool or a missing parent is Unavailable on every RPC (single-volume and whole-parent scans), never NotFound or absent.

- source: README.md:42,52,84; internal/endpoint/endpoint_test.go:437; internal/endpoint/parent_test.go:14
- tests: TestMissingParentIsUnavailableNotAbsent, TestMissingParentIsUnavailableForWholeScans
- type: Sim-only today; real pool needs `zpool export`.

### ERR-03 (OpenZFS-fact; table; elim none; current; oracle openzfs)

zfs stderr is classified from OpenZFS 2.4's own strings: exists, does not exist, parent does not exist, has children, has dependent clones, busy, being held, out of space, size greater than available, size less than used, pool read-only, key not loaded, must be one of, must be power of 2, pool must be upgraded, module does not support.

- source: internal/zfs/zfs_test.go:16; internal/zfs/errors.go; scratchpad/dod3/type12.md (corpus 28/162 decisive in-scope strings mapped)
- tests: TestClassify, FuzzClassify
- type: Generated table from the OpenZFS corpus (tools/extract_corpus.py), ratcheted.

### ERR-04 (error-code; table; elim partial; current; oracle csi-spec)

Kind to code: Busy Unavailable (non-destroy); HasClones, HasChildren, Precondition, Permission FailedPrecondition; Unavailable Unavailable; InvalidProperty InvalidArgument; NoSpace ResourceExhausted; Interrupted by deadline DeadlineExceeded, by cancel Aborted; Unknown Internal.

- source: internal/endpoint/errors_test.go:15; internal/endpoint/endpoint.go:273-303
- tests: TestZfsErrCodes
- type: Exhaustive switch over a closed `Kind` enum (exhaustive lint).

### ERR-05 (error-code; table; elim partial; current; oracle csi-spec)

Busy is mapped by operation: destroy FailedPrecondition; create, snapshot, expand and clone Unavailable.

- source: README.md:68; internal/endpoint/opcodes_test.go:26
- tests: TestBusyIsMappedByOperation
- type: CodeRule keyed by (OpKind, Kind).

### ERR-06 (error-code; scenario; elim partial; current; oracle adr)

An unclassified failure before any mutation is Internal; after an earlier op of the plan applied it is Aborted, and the retry converges.

- source: README.md:41; internal/endpoint/opcodes_test.go:61
- tests: TestUnknownFailureAfterAMutationIsRetryable
- type: `Mutated` flag carried by apply's result type.

### ERR-07 (error-code; scenario; elim none; current; oracle self)

A zfs command killed by the caller's deadline or cancellation is ErrInterrupted carrying the context error (DeadlineExceeded/Aborted, never Internal); commands get SIGTERM then a bounded WaitDelay and return promptly.

- source: README.md:43,69; internal/zfs/zfs_test.go:144
- tests: TestKilledCommandIsInterrupted, TestZfsErrCodes
- type: Real exec only.

### ERR-08 (error-code; scenario; elim none; current; oracle csi-spec)

A busy lock returns Aborted one second before the caller's deadline (or after LockWait), so sidecars retry instead of timing out.

- source: README.md:35; internal/endpoint/endpoint.go:250-262
- tests: TestReconcileLocksTargets

### ERR-09 (invariant; type; elim full; current; oracle adr)

An OpKind apply does not know refuses the whole plan as Internal before any op runs; every OpKind has a distinct String().

- source: internal/endpoint/apply_test.go:13,30
- tests: TestApplyRefusesAnUnknownOpKind, TestOpKindString
- type: Closed op sum type + exhaustive switch; unknown kind unrepresentable.

### ERR-10 (crash-convergence; table; elim partial; current; oracle adr)

Apply tolerances: OkMissing on destroys and OkExists only on ops whose existing target is provably ours (the internal clone-source snapshot); replaying a partially applied plan converges.

- source: README.md:34; internal/endpoint/apply.go:44-45; internal/endpoint/plan.go:266
- tests: TestPlanReconcile, FuzzReconcileSafe, TestCloneNeverAdoptsARacingDataset
- type: Tolerance is a field only on op variants that may carry it.

### ERR-11 (error-code; table; elim partial; current; oracle csi-spec)

Every error returned after a mutation is in the sidecar in-progress set (Aborted, Unavailable, DeadlineExceeded, ResourceExhausted), so a half-applied plan is always re-driven.

- source: scratchpad/wave5/lifecycle-fsm.md assertion 5; scratchpad/dod3/type12.md CodeRule.Mutated
- tests: TestUnknownFailureAfterAMutationIsRetryable
- type: Apply returns `PostMutation(err)` whose code mapping is restricted.

### ERR-12 (error-code; table; elim none; open-finding; oracle csi-spec)

`parent does not exist` on create is Unavailable (the parent went away), not NotFound (which CSI reads as 'source missing', final).

- source: scratchpad/dod3/type12.md D01/D12; internal/endpoint/endpoint.go:279
- tests: none

### ERR-13 (OpenZFS-fact; table; elim none; decided-pending; oracle openzfs)

Classification coverage of in-scope decisive OpenZFS stderr strings only rises (corpus ratchet per pinned 2.4.x tag).

- source: scratchpad/dod3/type12.md 'DOD remedy'
- tests: none
- type: Generated from source; table.


## SCN

### SCN-01 (invariant; type; elim full; current; oracle openzfs)

zfs output is untrusted: a repeated (dataset, property) line fails the whole scan (user-property newline forgery).

- source: internal/zfs/fuzz_test.go:43
- tests: TestParseColumnsRejectsForgedLines, FuzzParseColumns
- type: JSON scan (`zfs get -j -p`) decoded into a map keyed by name: duplicates and forged lines are unrepresentable.

### SCN-02 (invariant; type; elim full; open-finding; oracle openzfs)

A forged line for a name zfs never printed (phantom row) cannot create a row.

- source: scratchpad/dod3/existing.md F5; scratchpad/adv4/zfs-json.md; docs/adr/0004:20,46
- tests: none
- type: Same JSON decode; with -Hp, grid check (P lines per block in request order).

### SCN-03 (invariant; table; elim partial; current; oracle openzfs)

A scan is all-or-nothing: a nonzero exit, a parse error or truncated output discards everything; no partial table reaches a planner.

- source: scratchpad/adv4/zfs-json.md §3; scratchpad/dod3/existing.md F4
- tests: FuzzParseColumns
- type: Scan returns `(Table, error)` with Table only constructible after a clean end.

### SCN-04 (OpenZFS-fact; table; elim none; decided-pending; oracle openzfs)

JSON scan uses `zfs get -j -p` (never `--json-int`, which drops leading zeros), runs with LC_ALL=C.UTF-8, and fails closed on JSON that zfs truncated with exit 0 (invalid UTF-8).

- source: scratchpad/adv4/zfs-json.md; docs/adr/0004:20
- tests: none

### SCN-05 (invariant; type; elim full; current; oracle openzfs)

Numeric cells are strict: sizes must be decimal where the property applies to the row's type; zvol volsize > 0 and volsize % volblocksize == 0, else the scan fails.

- source: internal/endpoint/decode_test.go:11; scratchpad/wave5/zfs-invariants.md §7 #7
- tests: TestDecodeRejectsImpossibleZvolSizes
- type: Typed decode (`Size`, `BlockSize`), see EXP-05.

### SCN-06 (invariant; type; elim full; open-finding; oracle openzfs)

Type and name agree (snapshot iff exactly one `@`); only the requested types are accepted (bookmark, `-`, unknown refused); a row without a `type` cell fails the scan.

- source: scratchpad/wave5/zfs-invariants.md §7 #1; scratchpad/wave5/impossible-reach.md F7
- tests: none
- type: `Kind` enum decoded jointly from (type, name); `Volumes()` iterates a typed slice.

### SCN-07 (invariant; table; elim partial; decided-pending; oracle openzfs)

Impossible rows fail the scan: readonly props with a source other than `-`, a quota-family `0` not sourced `default`, `inherited from` on PROP_DEFAULT props, origin on a non-clone, space bounds violated.

- source: scratchpad/wave5/zfs-invariants.md §7 #3-#9
- tests: none
- type: Decoder rules as a data table.

### SCN-08 (invariant; type; elim full; open-finding; oracle openzfs)

A user property literally set to `-` with source local is a real value, not 'unset'.

- source: scratchpad/wave5/zfs-invariants.md §4, §8; internal/endpoint/endpoint.go userValue
- tests: none
- type: `Prop{Set bool; Value string}` instead of '' sentinel (JSON gives this for free).

### SCN-09 (invariant; table; elim partial; open-finding; oracle openzfs)

Rows deeper than a volume are kept as that volume's dependents, never dropped.

- source: scratchpad/wave5/impossible-reach.md F1; internal/endpoint/table.go:292
- tests: none
- type: Table carries `Dependents(vol)`.

### SCN-10 (invariant; type; elim full; current; oracle adr)

A Table records its coverage (whole parent or the volumes scanned); planners that conclude from absence (delete missing, free name, unique snapshot name, reconcile, export) refuse an uncovered table with Internal.

- source: README.md:30; internal/endpoint/coverage_test.go:16,62
- tests: TestPlannersRequireACoveringTable, TestScanRecordsCoverage
- type: `WholeTable` vs `ScopedTable` types; absence-acting planners take `WholeTable` or a `Covered[VolID]` proof.

### SCN-11 (invariant; table; elim partial; open-finding; oracle openzfs)

Names zfs accepts but the endpoint cannot address (space, leading `_`) are marked and skipped with a reason, never targeted, and never abort a reconcile.

- source: scratchpad/wave5/zfs-invariants.md §3, §8; scratchpad/wave5/impossible-reach.md F9
- tests: none
- type: Decode yields `Unaddressable` rows that have no `Target`.

### SCN-12 (invariant; type; elim full; current; oracle adr)

The zfs client refuses flag-like and shell-meta dataset names and newlines in property values before exec, and never uses a shell.

- source: internal/zfs/zfs_test.go:65; internal/zfs/runner.go
- tests: TestRejectsFlagLikeAndShellNames
- type: `DatasetName` newtype; runner takes `[]Arg` built from typed values.

### SCN-13 (OpenZFS-fact; table; elim none; current; oracle openzfs)

Index properties print as the first table name (2.4 tables): gzip-6 -> gzip, zstd-fast-1 -> zstd-fast, posixacl -> posix, xattr on -> sa, snapdir=disabled valid, snapdir=shown refused.

- source: README.md:82; internal/zfs/zfs_test.go:166
- tests: TestSimPrintsIndexPropertiesLikeOpenZFS
- type: Sim fidelity; oracle is zfs_prop.c 2.4 or a real pool.

### SCN-14 (OpenZFS-fact; scenario; elim none; current; oracle openzfs)

Simulator lifecycle matches zfs: create of an existing dataset ErrExists; destroy of a cloned snapshot HasClones; `-d` keeps it until the last clone; sparse zvol refreservation 0; user properties set at create are `local`.

- source: internal/zfs/zfs_test.go:77
- tests: TestLifecycleAgainstSim


## PRP

### PRP-01 (invariant; type; elim full; current; oracle adr)

Property passthrough is default-deny: mountpoint, sharenfs, sharesmb, canmount, exec, setuid, devices, key*/encryption, quota family, volsize, readonly, zoned, every user property (including ownership markers), non-allowlisted natives, wrong case and empty keys are InvalidArgument before state is read, and nothing is created.

- source: README.md:13; internal/endpoint/props_test.go:12; internal/endpoint/endpoint_test.go:486
- tests: TestPlanCreatePropertiesDenyList, TestCreateVolumeProperties, FuzzCreateConverges, TestRoundTripAndCodes
- type: Request carries `map[AllowedProp]Value` where `AllowedProp` is a closed enum parsed at the API edge.

### PRP-02 (OpenZFS-fact; type; elim full; current; oracle openzfs)

Per-kind masks: recordsize, atime, relatime, xattr, acltype, snapdir are filesystem-only; volblocksize and copies are zvol-only (copies on a filesystem counts against refquota and would silently cut capacity).

- source: README.md:67; internal/endpoint/props_test.go:262-273; scratchpad/adv3/props.md F5
- tests: TestPlanCreatePropertiesDenyList
- type: `FSProp` and `ZvolProp` separate enums.

### PRP-03 (OpenZFS-fact; table; elim partial; current; oracle openzfs)

Values canonicalise to what zfs 2.4 prints: compression names case-sensitive (gzip-N -> gzip, zstd-fast-N -> zstd-fast, zstd-1..19); recordsize power of two 512..16M with K/M/KiB suffixes; volblocksize <= 128K; xattr only sa/off; acltype posixacl -> posix, noacl -> off; snapdir hidden/visible/disabled; dedup off/on/verify; copies 1-3; sync/logbias enumerations.

- source: internal/endpoint/props_test.go:49
- tests: TestValidatePropertyValues, TestSimPrintsIndexPropertiesLikeOpenZFS
- type: Per-prop value enums generated from zfs_prop.c 2.4.

### PRP-04 (invariant; table; elim none; current; oracle openzfs)

Requested properties are applied locally at create (`create -o`, `clone -o`) and read back equal to their canonical form.

- source: internal/endpoint/props_test.go:113; internal/endpoint/endpoint_test.go:468
- tests: TestPlanCreateAppliesProperties, TestCreateVolumeProperties, FuzzCreateConverges

### PRP-05 (idempotency; table; elim none; current; oracle openzfs)

A committed volume is compatible only if each requested property is set locally with the same canonical value; unrequested local properties are not compared; default/inherited is not local; volblocksize is compared by effective value.

- source: README.md:13; internal/endpoint/props_test.go:166
- tests: TestPlanCreatePropertiesRetry, TestRoundTripAndCodes

### PRP-06 (crash-convergence; table; elim none; current; oracle adr)

An uncommitted row converges properties by setting only the differing ones, then commits.

- source: internal/endpoint/props_test.go:201
- tests: TestPlanCreatePropertiesConvergeUncommitted, TestCreateVolumeProperties

### PRP-07 (OpenZFS-fact; table; elim none; decided-pending; oracle openzfs)

OpenZFS 2.4 is the only supported version: 2.4 value spellings only (xattr dir/on and snapdir=disabled differ on 2.2); the daemon refuses to start unless userland and kmod are both zfs-2.4.*.

- source: docs/adr/0004:19-21; scratchpad/adv3/props.md F1, F3; scratchpad/wave6/lifecycle.md #4
- tests: none

### PRP-08 (CSI-contract; table; elim partial; current; oracle adr)

CSI StorageClass parameters: only `sparse` and `reserve` (booleans) are accepted, `csi.storage.k8s.io/*` keys are dropped before the endpoint; any other key or a non-bool is InvalidArgument.

- source: internal/csi/controller_test.go:99; scratchpad/adv3/csi.md 'Idempotency through translation', #12
- tests: TestParametersAndAccessChecks
- type: Parsed `SCParams` struct.


## NFS

### NFS-01 (invariant; type; elim full; decided-pending; oracle adr)

Export state lives only in the dataset's `sharenfs` (and the kernel's etab); no client list of our own: `<prefix>:nfs_clients` is removed and a leftover is reported by reconcile.

- source: docs/adr/0004:23-25; scratchpad/wave6/csi-nfs-design.md §1
- tests: none
- type: No column/type for a stored client list; ShareState parsed from sharenfs alone. Retire TestShareNFSAndClientEncoding's nfs_clients half.

### NFS-02 (invariant; type; elim full; decided-pending; oracle adr)

Canonical sharenfs grammar: `off` | `rw=<list>[,ro=<list>]` | `ro=<list>`; each entry a masked IPv4 CIDR always written with `/N` (also /32), sorted, no duplicates, N >= floor (default /16), no overlap anywhere in the value.

- source: docs/adr/0004:27-30; scratchpad/wave6/csi-nfs-design.md §2
- tests: TestShareNFSAndClientEncoding
- type: `CanonicalShare` built from `[]netip.Prefix`; render(parse(x)) == x round-trip is the only test.

### NFS-03 (ownership; table; elim partial; current; oracle openzfs)

Ownership of the value: local and canonical is MANAGED (rewritten as a whole, whoever typed it); local non-canonical is FOREIGN (served untouched, reported, never written: Publish FailedPrecondition); not local is INHERITED (the parent's; the next write sets it locally).

- source: docs/adr/0004:26-29; internal/endpoint/export_test.go:603-609; internal/endpoint/export_contract_test.go:228
- tests: TestPlanPublishExport, TestFileExportAdoptedVolumes, TestPlanUnpublishExport
- type: `ShareState{Off,Managed(CanonicalShare),Foreign(raw),Inherited}`.

### NFS-04 (OpenZFS-fact; type; elim full; current; oracle openzfs)

Unexport sets `sharenfs=off` locally, never `zfs inherit` (an ancestor shared to `*` would re-export the volume to every host).

- source: README.md:60; internal/endpoint/export_test.go:650; internal/endpoint/export_contract_test.go:121
- tests: TestPlanUnpublishExport, TestFileExportLifecycle, TestExportConverges
- type: No `Inherit` op in the vocabulary.

### NFS-05 (OpenZFS-fact; scenario; elim none; current; oracle openzfs)

Every export change is verified in the kernel's etab because `zfs set sharenfs` exits 0 when exportfs fails: not served is Unavailable naming the NFS server; a retry re-shares although the property is already right; Unpublish never reports a revoke that did not happen.

- source: README.md:61; internal/endpoint/export_contract_test.go:189; internal/endpoint/export_test.go:594,655
- tests: TestFileExportVerifiesKernelTable, TestPlanPublishExport, TestPlanUnpublishExport
- type: Needs a second observer (etab).

### NFS-06 (OpenZFS-fact; scenario; elim none; decided-pending; oracle nfs-utils)

A successful export also requires a running nfsd (`/proc/fs/nfsd/threads` > 0): etab is written by exportfs even when nfsd is stopped.

- source: scratchpad/adv4/export.md F4; scratchpad/wave6/csi-nfs-design.md §9.3
- tests: none

### NFS-07 (idempotency; table; elim none; current; oracle csi-spec)

Publish of the same client in the same mode runs no `zfs set`; the other mode is AlreadyExists; Unpublish of an absent client, of a missing volume, of a zvol, and a repeated Unpublish are OK; publish/unpublish sequences converge.

- source: internal/endpoint/export_contract_test.go:88-133; internal/endpoint/export_test.go:243
- tests: TestFileExportLifecycle, TestPlanPublishExport, TestPlanUnpublishExport, TestExportConverges, TestFileExport

### NFS-08 (invariant; type; elim full; open-finding; oracle nfs-utils)

Export clients are IPv4 addresses/CIDRs only: empty, `*`, netgroups, IPv6, option text, shell metacharacters, `0.0.0.0/0`, prefixes below the floor, hex/octal aliases and leading zeros are InvalidArgument; no hostnames (DNS failure silently revokes).

- source: internal/endpoint/export_test.go:38; internal/endpoint/export_contract_test.go:165; docs/adr/0004:27-30; scratchpad/adv4/export.md F1, F5
- tests: TestParseClient, TestFileExportPreconditions, TestFileExport
- type: `Client` = `netip.Prefix` with floor check in its constructor.

### NFS-09 (invariant; table; elim partial; open-finding; oracle nfs-utils)

etab lookup matches the realpath of the mountpoint and the exact client string (no prefix rule); a line without ro/rw is not served; octal-escaped paths are decoded.

- source: internal/endpoint/export_test.go:95; scratchpad/adv4/export.md F3, F6; scratchpad/wave5/impossible-reach.md F13
- tests: TestParseEtab
- type: Etab entries decoded into (Path, netip.Prefix, Mode) with Mode required.

### NFS-10 (error-code; table; elim partial; current; oracle adr)

Publish preconditions: zvol FailedPrecondition; unmounted with canmount noauto/off FailedPrecondition and sharenfs unchanged; missing or unmanaged volume NotFound; invalid volume id InvalidArgument.

- source: internal/endpoint/export_contract_test.go:136; internal/endpoint/export_test.go:611-616
- tests: TestFileExportPreconditions, TestPlanPublishExport
- type: Export planners take `OwnedFS` (zvol unrepresentable).

### NFS-11 (OpenZFS-fact; scenario; elim partial; open-finding; oracle openzfs)

Unpublish on an unmounted volume never mounts it: write with `zfs set -u` (a plain set on a canmount=on unmounted dataset mounts and shares it).

- source: scratchpad/adv4/export.md F8; scratchpad/wave6/csi-nfs-design.md Fact F; internal/endpoint/export_test.go:661
- tests: TestPlanUnpublishExport
- type: `SetShare{NoMount bool}` required field derived from Mounted.

### NFS-12 (OpenZFS-fact; table; elim partial; open-finding; oracle openzfs)

sharenfs is limited to 8191 bytes (ZAP_MAXVALUELEN - 1); a longer value is ResourceExhausted before any set.

- source: scratchpad/adv4/export.md F7; scratchpad/wave5/zfs-invariants.md §4
- tests: none
- type: `CanonicalShare` constructor checks length.

### NFS-13 (invariant; table; elim none; current; oracle adr)

File export is advertised and served only when enabled (root); otherwise the capability is absent and both RPCs are Unimplemented.

- source: README.md:15; internal/endpoint/export_contract_test.go:173; internal/server/server_test.go:482
- tests: TestFileExportPreconditions, TestFileExport

### NFS-14 (crash-convergence; scenario; elim none; decided-pending; oracle openzfs)

Static export at create: `zfs create -o sharenfs=<canonical>`, verify nfsd and etab, and only then set the commit marker (committed means created and served); a crash at C1-C5 is recoverable from sharenfs, etab and the marker alone.

- source: docs/adr/0004:32; scratchpad/wave6/csi-nfs-design.md §4, §8
- tests: none

### NFS-15 (OpenZFS-fact; scenario; elim none; decided-pending; oracle nfs-utils)

Root squash stays on: the endpoint sets the new dataset root's owner and mode from StorageClass parameters before committing.

- source: docs/adr/0004:35
- tests: none

### NFS-16 (OpenZFS-fact; scenario; elim none; current; oracle openzfs)

The inherited-sharenfs window: a dataset without a local sharenfs inherits a shared parent's value (`on` = rw,crossmnt to `*`); the first publish pins a local value and `*` is no longer served; the parent's effective sharenfs must be `off` at startup so a crash inside the create ioctl (props not yet set) exports nothing.

- source: internal/endpoint/export_contract_test.go:228; internal/endpoint/export_test.go:605; scratchpad/wave6/csi-nfs-design.md Fact A, B, C1
- tests: TestFileExportAdoptedVolumes, TestPlanPublishExport
- type: Only real libshare + nfsd show the `*` export.

### NFS-17 (OpenZFS-fact; type; elim full; decided-pending; oracle openzfs)

Unsafe sharenfs forms (`rw=`, `rw=a::b`, `rw=a:`, bare rw/ro, `on`) export to the world; the endpoint never writes them and reconcile reports `world-export`.

- source: scratchpad/wave6/csi-nfs-design.md Fact B, §9.1
- tests: none
- type: `CanonicalShare` cannot render an empty host (non-empty sorted list).

### NFS-18 (OpenZFS-fact; type; elim full; decided-pending; oracle nfs-utils)

Overlapping clients are banned: mountd takes the first matching export (FQDN before subnet, then file order), so a ro /32 inside a rw /24 would be served rw.

- source: scratchpad/wave6/csi-nfs-design.md Fact D, §9.2
- tests: none
- type: `CanonicalShare` constructor rejects overlap.

### NFS-19 (CSI-contract; table; elim partial; decided-pending; oracle csi-spec)

A node_id used as an export client must be one IPv4 host (`/32`), never a CIDR; Unpublish with an empty node_id unpublishes all (MANAGED value -> off).

- source: docs/adr/0004:49; scratchpad/wave6/csi-nfs-design.md §12; scratchpad/adv4/csi-node-seam.md §1e
- tests: none
- type: `NodeHost` newtype (single /32).

### NFS-20 (invariant; table; elim none; decided-pending; oracle adr)

Reconcile reports export drift D1-D7 (inherited, foreign/world-export/below-floor, not-served/extra-served, nfsd-down, parent-shared, orphan-export, legacy nfs_clients); its only export write is a reshare of an unchanged MANAGED value.

- source: scratchpad/wave6/csi-nfs-design.md §10
- tests: none

### NFS-21 (OpenZFS-fact; scenario; elim none; current; oracle openzfs)

Deleting an exported filesystem needs no explicit unshare: `zfs destroy` unshares, runs exportfs, unmounts and destroys; a busy unmount re-shares and fails, leaving the volume unchanged.

- source: scratchpad/wave6/csi-nfs-design.md Fact E, §7; internal/endpoint/export_contract_test.go:128
- tests: TestFileExportLifecycle

### NFS-22 (CSI-contract; table; elim none; decided-pending; oracle csi-spec)

volume_context carries `server` (one of the advertised -nfs-server entries) and `share` (the mountpoint); publish_context stays empty; the export RPC returns the bare export path.

- source: scratchpad/wave6/csi-nfs-design.md §5; scratchpad/adv4/csi-node-seam.md §1b; internal/server/server_test.go:502
- tests: TestFileExport


## REC

### REC-01 (invariant; type; elim full; current; oracle adr)

Reconcile only destroys, replay-tolerantly (OkMissing); snapshots are destroyed deferred (`-d`), volumes are not.

- source: internal/endpoint/reconcile_test.go:223-229
- tests: TestPlanReconcile, FuzzReconcileSafe
- type: `ReconcileAction` holds a `Destroy` op variant only.

### REC-02 (ownership; table; elim partial; current; oracle adr)

Reconcile never targets unowned, received or renamed rows, desired volumes, CSI snapshots, volumes with CSI snapshots, rows younger than MinAge, or rows with unknown creation.

- source: README.md:100,108; internal/endpoint/reconcile_fuzz_test.go:24
- tests: FuzzReconcileSafe, TestPlanReconcile, TestReconcileSparesReceivedAndRenamed, TestReconcileAgainstDesiredSet
- type: Candidate filter = FSM cells; residual: property test.

### REC-03 (invariant; table; elim partial; current; oracle adr)

An empty desired set collects no committed volume (skipped with SkipEmptyDesired) but still collects old uncommitted rows.

- source: internal/endpoint/reconcile_test.go:69-80; internal/endpoint/reconcile_contract_test.go:383
- tests: TestPlanReconcile, TestReconcileAgainstDesiredSet, FuzzReconcileSafe
- type: `Desired` sum type {Unknown, Set(nonempty)}.

### REC-04 (invariant; table; elim none; open-finding; oracle adr)

A desired set naming no existing volume is treated like an empty one, and destructive runs are bounded (max_deletions, or a dry-run plan digest the real run must echo).

- source: scratchpad/adv4/reconcile.md F2
- tests: none

### REC-05 (crash-convergence; table; elim none; current; oracle adr)

Uncommitted rows older than MinAge are collected (abandoned creates); younger ones are skipped as possibly in flight; a desired uncommitted row is left for the caller's retry.

- source: internal/endpoint/reconcile_contract_test.go:37; internal/endpoint/reconcile_test.go:76-97
- tests: TestReconcileCollectsAbandonedCreate, TestPlanReconcile

### REC-06 (crash-convergence; scenario; elim none; open-finding; oracle adr)

MinAge counts from commit for committed rows (max(creation, committed_at)), has a floor, and the locked re-plan may only drop actions from the unlocked plan, never add or reclassify (SkipChangedSinceScan).

- source: scratchpad/adv4/reconcile.md F1
- tests: none
- type: Race between commit and reconcile.

### REC-07 (OpenZFS-fact; table; elim none; current; oracle openzfs)

Fixpoint: a clone, its clone-source snapshot and the freed source are collected in one run, clone first; a young clone blocks the chain with reasons.

- source: internal/endpoint/reconcile_test.go:126-150; internal/endpoint/reconcile_contract_test.go:396
- tests: TestPlanReconcile, TestReconcileAgainstDesiredSet, TestSimulateDestroyReapsDeferredOrigins
- type: simulateDestroy is a second model of dsl_destroy; oracle must be zfs.

### REC-08 (invariant; table; elim none; current; oracle adr)

Orphan clone-source snapshots (clone gone) are collected; one in use is kept; one on an unowned volume is reported only; a young one is skipped as mid-clone.

- source: internal/endpoint/reconcile_test.go:152-194; internal/endpoint/reconcile_contract_test.go:72
- tests: TestPlanReconcile, TestReconcileCollectsLeakedCloneSource

### REC-09 (invariant; table; elim none; current; oracle adr)

A dry run returns the plan as data, applies nothing and takes no locks; with nothing changing, it equals the real run's plan.

- source: internal/endpoint/reconcile_contract_test.go:319,476; internal/endpoint/reconcile_fuzz_test.go:130
- tests: TestReconcileCollectsAbandonedCreate, TestReconcileLocksTargets, FuzzReconcileSafe

### REC-10 (invariant; scenario; elim partial; current; oracle adr)

Reconcile locks every target and the clone each released source serves; a target held by another operation is not destroyed (Aborted today; per-group skip decided).

- source: internal/endpoint/reconcile_contract_test.go:185; internal/endpoint/reconcile_test.go:235,260; scratchpad/adv4/reconcile.md F8
- tests: TestReconcileLocksTargets, TestPlanReconcileLocksEveryTarget, TestPlanReconcileHeldLimitsActions
- type: Action type carries its lock set; apply takes `Locked[Action]`.

### REC-11 (idempotency; table; elim none; current; oracle self)

A second reconcile with the same request plans nothing.

- source: internal/endpoint/reconcile_contract_test.go:332,412
- tests: TestReconcileCollectsAbandonedCreate, TestReconcileAgainstDesiredSet, FuzzReconcileSafe

### REC-12 (crash-convergence; scenario; elim none; open-finding; oracle openzfs)

One failing destroy neither hides what already ran nor starves later candidates: apply per candidate group, report failures as skips, return the partial plan.

- source: scratchpad/adv4/reconcile.md F3; scratchpad/wave5/impossible-reach.md F5
- tests: none

### REC-13 (error-code; table; elim partial; current; oracle adr)

Reconcile input: an invalid desired id is InvalidArgument; min_age_seconds that overflows is InvalidArgument; 0 means the default of one hour.

- source: internal/endpoint/reconcile_contract_test.go:416; internal/server/server_test.go:471-476; internal/endpoint/reconcile.go:308
- tests: TestReconcileAgainstDesiredSet, TestReconcile

### REC-14 (invariant; table; elim partial; current; oracle adr)

Every skip carries its reason (desired missing, too young, no creation, empty desired, has snapshot X, dependent clone Y, unmanaged source, foreign snapshots named, changed since scan).

- source: internal/endpoint/reconcile_test.go:48; internal/server/server_test.go:465; internal/endpoint/foreign_test.go:488
- tests: TestPlanReconcile, TestReconcile, TestDeleteVolumeSparesForeignSnapshots
- type: Reason is a closed enum + detail.

### REC-15 (invariant; type; elim full; current-untested; oracle adr)

Garbage collection runs only on demand against a caller-supplied desired set, off the request path; request RPCs never consult a desired-state store.

- source: README.md:56-57; scratchpad/dod3/future.md §5
- tests: none
- type: No desired-state type reachable from request planners.


## LCK

### LCK-01 (invariant; scenario; elim partial; current; oracle adr)

Every resource an operation touches is locked (volume, a clone's source volume and snapshot, clone-source owners) with keys taken in sorted order (no deadlock).

- source: README.md:35; scratchpad/adv4/reconcile.md 'Checked and dropped'
- tests: TestConcurrentCreatesSerialize, TestReconcileLocksTargets
- type: Planner returns its lock set; apply refuses a plan whose targets are outside it.

### LCK-02 (invariant; scenario; elim partial; decided-pending; oracle adr)

A mutation never consumes a scan that started before it took its locks (single-flight sharing is for List* only).

- source: scratchpad/dod3/existing.md F3 'Risk'
- tests: none
- type: `LockedScan` token produced only by scan-after-lock.

### LCK-03 (invariant; scenario; elim none; current; oracle adr)

One endpoint instance per parent per host: a per-parent flock; a second instance on the same parent is refused ('already manages').

- source: cmd/zfs-endpoint/main_test.go:8; scratchpad/wave6/lifecycle.md #12
- tests: TestLockParentIsExclusive


## RT

### RT-01 (invariant; table; elim none; current; oracle adr)

The unix socket is 0600 in a 0700 directory; a live socket is refused, a stale one replaced, and a non-socket (file, directory, symlink) at the path is never deleted.

- source: README.md:18,70; internal/unixsock/unixsock_test.go
- tests: TestListenIsOwnerOnly, TestListenRefusesALiveSocket, TestListenReplacesAStaleSocket, TestListenNeverDeletesANonSocket

### RT-02 (invariant; table; elim none; decided-pending; oracle adr)

No unauthenticated TCP listener serves the destructive API (until ADR 0001 mTLS).

- source: scratchpad/wave6/lifecycle.md #2; docs/adr/0001
- tests: none

### RT-03 (invariant; scenario; elim none; decided-pending; oracle adr)

zfs children run in their own process group with a parent-death signal; shutdown drains in-flight zfs within -drain, then kills leftovers.

- source: scratchpad/wave6/lifecycle.md #1, #5
- tests: none

### RT-04 (invariant; table; elim none; decided-pending; oracle adr)

Every destroy plan is written to the audit log (principal, rpc, targets) before it is applied.

- source: scratchpad/wave6/lifecycle.md #9; scratchpad/dod3/future.md §3
- tests: none


## INV

### INV-01 (invariant; type; elim full; current; oracle adr)

Planners are pure: (Table, request) in, Plan or typed error out; no I/O, no clock except an injected Now.

- source: README.md:31
- tests: (all plan_test.go, props_test.go, export_test.go, reconcile_test.go tests)
- type: Planner signatures take no ctx/Runner; package boundary forbids importing the runner.

### INV-02 (invariant; table; elim none; decided-pending; oracle self)

After a successful mutating RPC, re-observing and re-planning the same request under the same locks yields an empty plan (verification = residual plan).

- source: scratchpad/dod3/future.md §4; README.md:32
- tests: FuzzCreateConverges, TestExportConverges
- type: Same-model blindness: re-plan agrees with a misread (future.md §4.4).

### INV-03 (invariant; table; elim partial; decided-pending; oracle adr)

Plans are printable, diffable data with golden tests (one fat Op struct or closed variants with String()).

- source: README.md:59; scratchpad/dod3/future.md §0
- tests: none


## CR

### CR-01 (crash-convergence; scenario; elim none; current; oracle self)

A crash or kill after any op k of any plan, followed by the CO's retry, ends in the same state and response as the uninterrupted run (DeleteVolume CX1 still open).

- source: README.md:33-34; scratchpad/wave5/lifecycle-fsm.md §3 assertion 2; scratchpad/wave5/impossible-reach.md FuzzCreateCrashPrefix
- tests: TestCloneFailureAfterCloneConvergesToSameState, TestCreateVolumeResumesInterruptedProvision
- type: Oracle is the uncrashed run of the same code: common-mode.

### CR-02 (crash-convergence; scenario; elim none; current-untested; oracle openzfs)

A kill in the middle of a multi-property `zfs set` (any proper subset applied) converges on retry.

- source: scratchpad/wave5/lifecycle-fsm.md §2b
- tests: none

### CR-03 (OpenZFS-fact; scenario; elim none; current-untested; oracle openzfs)

`zfs destroy -r` is not atomic: children and snapshots go first, in post-order, before a later failure; a held snapshot does not protect the rest of the tree.

- source: scratchpad/adv4/reconcile.md F3 (zfs_main.c:1508-1520, 1909-1922); scratchpad/wave5/lifecycle-fsm.md §2b
- tests: none

### CR-04 (OpenZFS-fact; scenario; elim none; current-untested; oracle openzfs)

`zfs create`/`clone` with `-o` props is atomic with respect to invalid props (the ioctl destroys the new objset if setting props fails) but not with the share/mount step, which fails with exit 0.

- source: scratchpad/adv3/props.md F4; scratchpad/wave6/csi-nfs-design.md Fact A
- tests: none


## CSI

### CSI-01 (CSI-contract; table; elim none; current; oracle csi-spec)

The controller advertises exactly CREATE_DELETE_VOLUME, CREATE_DELETE_SNAPSHOT, LIST_VOLUMES, LIST_SNAPSHOTS, GET_CAPACITY, CLONE_VOLUME, EXPAND_VOLUME (no PUBLISH_UNPUBLISH).

- source: internal/csi/controller_test.go:27
- tests: TestCapabilitiesAdvertised

### CSI-02 (CSI-contract; table; elim none; current; oracle csi-spec)

MULTI_NODE_MULTI_WRITER is InvalidArgument at create; ValidateVolumeCapabilities confirms only supported capabilities (zvol: block and mount; reserved filesystem: mount only) and returns NotFound for unknown ids.

- source: internal/csi/controller_test.go:131-149,163
- tests: TestParametersAndAccessChecks, TestNeverIssuedIDs
- type: Static-NFS mode changes this (all five modes for FILESYSTEM, csi-nfs-design §7).

### CSI-03 (CSI-contract; table; elim none; current; oracle adr)

volume_context `kind` is `filesystem` or `block` matching the endpoint kind.

- source: internal/csi/controller_test.go:68-74
- tests: TestExpandNodeExpansionRequired

### CSI-04 (CSI-contract; scenario; elim none; current; oracle csi-spec)

csi-sanity's Identity and Controller groups pass on the simulator and on real ZFS in both access types; the focus regex is unanchored and a spec-count floor fails a run that executes 0 specs.

- source: internal/csi/sanity_test.go:20,48; scratchpad/wave6/ci-gate.md #3
- tests: TestSanity

### CSI-05 (CSI-contract; scenario; elim none; current-untested; oracle csi-spec)

The front end passes endpoint gRPC codes through unchanged and propagates deadlines, so the sidecar sees ABORTED/UNAVAILABLE/DEADLINE_EXCEEDED as produced.

- source: scratchpad/adv3/csi.md 'Checked and clean'
- tests: none

### CSI-06 (CSI-contract; table; elim none; open-finding; oracle csi-spec)

ValidateVolumeCapabilities confirms parameters and volume_context only after checking them against the volume.

- source: scratchpad/adv3/csi.md #7
- tests: none


## PRF

### PRF-01 (perf; table; elim partial; current-untested; oracle adr)

Each request runs one scan per source (one `zfs get`) and decodes it once into an immutable Table.

- source: README.md:25-30
- tests: none
- type: Planner input structs hold one `Table` value; counted via Sim.Calls.

### PRF-02 (perf; table; elim none; decided-pending; oracle openzfs)

DeleteVolume scans only the volume's subtree (zfs refuses dependent clones itself); CreateSnapshot's global-name check reads only snapshots and 3 columns.

- source: scratchpad/dod3/existing.md F2, F6
- tests: none

### PRF-03 (perf; table; elim none; decided-pending; oracle adr)

PlanReconcile is linear in rows (one-pass simulateDestroyAll): 16k volumes plan in well under a second (31 s -> 86 ms measured).

- source: scratchpad/dod3/existing.md F1
- tests: none

### PRF-04 (perf; scenario; elim none; decided-pending; oracle adr)

Concurrent full-parent scans are single-flighted (List* only) and bounded by a 1-2 slot semaphore; zfs execs are bounded overall.

- source: scratchpad/dod3/existing.md F3; scratchpad/wave6/lifecycle.md #3
- tests: none

### PRF-05 (perf; scenario; elim none; decided-pending; oracle openzfs)

p99 latency of every RPC stays under 5 s (half the 10 s sidecar timeout) at 10k datasets / 100k snapshots on real ZFS.

- source: README.md:56; scratchpad/dod3/mcnamara-survivorship.md §5 #6-7
- tests: none

### PRF-06 (perf; table; elim none; decided-pending; oracle adr)

The scan decoder streams with all-or-nothing return and narrows columns and types per operation; Go-side cost per 10k rows is measured, not inferred (README:71's 12 us claim is falsified: ~230 ms).

- source: scratchpad/dod3/existing.md F4, F6, 'Prior claims'
- tests: none

