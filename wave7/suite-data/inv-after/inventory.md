total 1813 stmts; sim union 1575 (86.9%); real-reachable 1385 (76.4%)

| pkg | test | runs | sim s | real s | stmts | unique | unique on real leg | class | invariant | redundant? |
|---|---|---|---|---|---|---|---|---|---|---|
| cmd/zfs-endpoint | TestLockParentIsExclusive | unit | 0 |  | 26 | 10 |  | process-singleton | one endpoint process per parent (flock) |  |
| csi | TestCapabilitiesAdvertised | both | 0 | 0.06 | 92 | 0 | 0 | csi-surface | controller advertises exactly the supported RPC set |  |
| csi | TestExpandNodeExpansionRequired | both | 0.02 | 0.35 | 494 | 0 | 0 | csi-expand | node expansion only for a zvol carrying a filesystem | yes |
| csi | TestExpandUnlimitedFilesystem | both | 0.02 |  | 460 | 2 | 3 | csi-expand | expand of an unlimited fs reports the requested capacity |  |
| csi | TestNeverIssuedIDs | both | 0 |  | 275 | 9 | 9 | csi-ids | ids never issued are NotFound (Delete: OK), never InvalidArgument |  |
| csi | TestParametersAndAccessChecks | both | 0.02 | 0.29 | 517 | 17 | 18 | csi-params | StorageClass parameters and access modes are validated |  |
| csi | TestSanity | both | 1.06 | 4.97 | 850 | 63 | 80 | csi-spec | csi-sanity Identity + Controller specs pass |  |
| endpoint | FuzzCreateConverges | sim* | 0.02 | 0.00 | 468 | 1 |  | create-idempotency | accepted creates converge; retry mutates nothing (fuzz) |  |
| endpoint | FuzzReconcileSafe | sim* | 0.01 | 0.00 | 265 | 0 |  | reconcile-safety | reconcile never targets kept rows and converges (fuzz) |  |
| endpoint | TestApplyRefusesAnUnknownOpKind | sim* | 0.01 |  | 56 | 1 |  | apply-safety | unknown op kind refuses the whole plan before any op |  |
| endpoint | TestBusyIsMappedByOperation | sim | 0 |  | 576 | 1 |  | error-mapping | busy maps by op: delete FailedPrecondition, else Unavailable |  |
| endpoint | TestCloneFailureAfterCloneConvergesToSameState | sim | 0 |  | 540 | 0 |  | crash-convergence | failure after clone converges to the uninterrupted end state | yes |
| endpoint | TestCloneFromVolumeReleasesInternalSnapshot | both | 0 | 0.33 | 480 | 0 | 0 | clone-source-lifecycle | volume clones release their internal snapshot | yes |
| endpoint | TestCloneLargerThanLimitIsOutOfRange | both | 0.01 |  | 497 | 0 | 2 | create-capacity | a clone larger than limit is OutOfRange before any op | yes |
| endpoint | TestCloneNeverAdoptsARacingDataset | sim* | 0.02 |  | 495 | 1 |  | ownership | a clone never adopts a dataset created in the race window |  |
| endpoint | TestCloneResizeFailureConverges | sim | 0 | 0.18 | 512 | 0 |  | crash-convergence | failure between clone and resize converges on retry | yes |
| endpoint | TestCloneSourcesMustBeOwned | both | 0 |  | 453 | 0 | 0 | ownership | clone sources must be owned, committed, CSI | yes |
| endpoint | TestConcurrentCreatesSerialize | sim | 0.03 | 0.31 | 392 | 0 |  | locking | concurrent identical creates run zfs create once |  |
| endpoint | TestCrashAtEveryMutationConverges | both | 0 |  | 958 | 4 | 46 | crash-convergence | a crash before/after any mutating call converges to the uninterrupted end state |  |
| endpoint | TestCreateVolumeIdempotent | both | 0 | 0.18 | 416 | 1 | 2 | create-idempotency | identical create is a no-op; incompatible is AlreadyExists |  |
| endpoint | TestCreateVolumeProperties | both | 0.01 | 0.93 | 580 | 0 | 12 | props-e2e | properties applied, read back, compared on retry, converged |  |
| endpoint | TestCreateVolumeResumesInterruptedProvision | both | 0 | 0.19 | 371 | 0 | 0 | create-resume | an uncommitted provision is hidden then resumed | yes |
| endpoint | TestDefaultPropertyPrefixIsOurOwn | sim* | 0 |  | 371 | 0 |  | prefix | default property prefix is zfs-endpoint | yes |
| endpoint | TestDeleteVolume | both | 0 | 0.23 | 493 | 0 | 0 | delete-semantics | delete is idempotent and refuses while CSI snapshots exist | yes |
| endpoint | TestDeleteVolumeAfterItsCloneIsGone | both | 0 |  | 458 | 0 | 0 | clone-source-lifecycle | internal clone-source snapshot does not block delete | yes |
| endpoint | TestDeleteVolumeSparesForeignSnapshots | sim | 0 |  | 497 | 2 |  | ownership | foreign snapshots block delete and reconcile unless opted in |  |
| endpoint | TestExpand | both | 0 | 0.21 | 420 | 0 | 0 | expand | expand grows, never shrinks, NotFound when missing | yes |
| endpoint | TestExportConverges | sim* | 0 | 0.00 | 157 | 0 |  | export-plan | publish/unpublish twice converges to an empty plan | yes |
| endpoint | TestFileExportAdoptedVolumes | both(nfs) | 0 | 0.25 | 377 | 0 | 1 | export-adoption | inherited sharenfs is pinned; foreign local sharenfs refused |  |
| endpoint | TestFileExportLifecycle | both(nfs) | 0 | 0.31 | 582 | 0 | 26 | export-e2e | publish/unpublish reaches the kernel table, idempotently |  |
| endpoint | TestFileExportPreconditions | both | 0 | 0.30 | 479 | 3 | 18 | export-input | export refuses zvols, unmounted, missing, bad hosts, disabled |  |
| endpoint | TestFileExportVerifiesKernelTable | sim | 0 | 0.06 | 530 | 2 |  | export-verify | publish/unpublish verify etab, not zfs set's exit code |  |
| endpoint | TestGoldenPlans | sim* | 0 |  | 559 | 21 |  | plan-golden | every (Table, request) row plans exactly its golden ops, deterministically |  |
| endpoint | TestInheritedOwnershipIsNotOwnership | both | 0 | 0.19 | 427 | 0 | 2 | ownership | inherited user props are not ownership | yes |
| endpoint | TestMissingParentIsUnavailableForWholeScans | sim | 0 |  | 189 | 7 |  | missing-parent | whole-parent scans map a missing parent to Unavailable |  |
| endpoint | TestMissingParentIsUnavailableNotAbsent | sim | 0.02 | 0.05 | 461 | 3 |  | missing-parent | missing parent is Unavailable, not NotFound/absent |  |
| endpoint | TestNeverAdoptsOrDeletesUnmanaged | both | 0 | 0.13 | 331 | 0 | 1 | ownership | unmanaged datasets are never adopted or deleted | yes |
| endpoint | TestOpKindString | sim* | 0 |  | 19 | 0 |  | diagnostics | op kinds render stable names |  |
| endpoint | TestPagination | both | 0 | 1.03 | 405 | 0 | 1 | pagination | list pages in order; bad token Aborted |  |
| endpoint | TestParseClient | sim* | 0 | 0.00 | 47 | 2 |  | export-input | NFS client specs are parsed or InvalidArgument |  |
| endpoint | TestParseEtab | sim* | 0 | 0.00 | 47 | 10 |  | export-etab | etab lines parse to client lookups |  |
| endpoint | TestPlanCreateAppliesProperties | sim* | 0 | 0.00 | 197 | 1 |  | props-apply | validated properties are planned at create |  |
| endpoint | TestPlanCreateConvergesUncommittedClone | sim* | 0 | 0.00 | 104 | 0 |  | create-resume | an uncommitted clone is driven to size, reservation, commit | yes |
| endpoint | TestPlanCreateFilesystemsAreNotShared | sim* | 0 | 0.00 | 141 | 0 |  | export-default | new filesystems are created unshared |  |
| endpoint | TestPlanCreateHonorsLimitOnly | sim* | 0 | 0.00 | 82 | 0 |  | create-capacity | limit-only request sets a quota | yes |
| endpoint | TestPlanCreateIsEmptyWhenSatisfied | sim* | 0 | 0.00 | 93 | 0 |  | create-idempotency | a satisfied create plans nothing | yes |
| endpoint | TestPlanCreateMalformedSourceIsNotFound | sim* | 0 | 0.00 | 99 | 1 |  | create-source | unissuable content-source ids are NotFound |  |
| endpoint | TestPlanCreatePropertiesConvergeUncommitted | sim* | 0 | 0.00 | 141 | 2 |  | create-resume | uncommitted rows are driven to the requested properties |  |
| endpoint | TestPlanCreatePropertiesDenyList | sim* | 0 | 0.00 | 55 | 1 |  | props-allowlist | properties outside the allowlist/kind are InvalidArgument |  |
| endpoint | TestPlanCreatePropertiesRetry | sim* | 0 | 0.00 | 151 | 1 |  | props-compat | retry compatibility compares local canonical values |  |
| endpoint | TestPlanCreateRefusesForeignAndIncompatible | sim* | 0 | 0.00 | 105 | 0 |  | create-compat | foreign or incompatible existing rows are AlreadyExists |  |
| endpoint | TestPlanCreateThickZvolLeavesReservationToZFS | sim* | 0 | 0.00 | 81 | 0 |  | create-capacity | thick zvols keep zfs's auto refreservation | yes |
| endpoint | TestPlanDeleteVolume | sim* | 0 | 0.00 | 79 | 0 |  | delete-semantics | delete refuses snapshotted/cloned volumes, defers clone sources | yes |
| endpoint | TestPlanExpand | sim* | 0 | 0.00 | 69 | 0 |  | expand | expand rounds to volblocksize; unlimited fs gets no quota | yes |
| endpoint | TestPlanPublishExport | sim* | 0 | 0.00 | 161 | 0 |  | export-plan | publish plans the minimal sharenfs set or refuses | yes |
| endpoint | TestPlanReconcile | sim* | 0 | 0.00 | 195 | 3 |  | reconcile-plan | reconcile collects exactly the undesired/uncommitted owned, old rows |  |
| endpoint | TestPlanReconcileHeldLimitsActions | sim* | 0 | 0.00 | 141 | 4 |  | reconcile-locks | actions outside held locks, and dependents, are skipped |  |
| endpoint | TestPlanReconcileLocksEveryTarget | sim* | 0 | 0.00 | 170 | 0 |  | reconcile-locks | every reconcile action locks its volume and served clone | yes |
| endpoint | TestPlanSnapshotLongAndUnusualNames | sim* | 0 | 0.00 | 73 | 1 |  | snapshot-names | long/unusual names hash to a valid <=128-byte id, stably |  |
| endpoint | TestPlanSnapshotReservedPrefix | sim* | 0 | 0.00 | 41 | 0 |  | snapshot-names | internal clone-source prefix is reserved | yes |
| endpoint | TestPlanUnpublishExport | sim* | 0 | 0.00 | 162 | 2 |  | export-plan | unpublish plans the minimal sharenfs set or refuses |  |
| endpoint | TestPlannersRequireACoveringTable | sim* | 0 |  | 120 | 9 |  | plan-coverage | planners refuse a table that does not cover what they judge |  |
| endpoint | TestPropertyPrefixIsValidated | sim* | 0 |  | 49 | 3 |  | prefix | invalid property prefixes are refused at startup |  |
| endpoint | TestPropertyRoundTrip | both | 0.07 |  | 458 | 0 | 4 | props-roundtrip | every allowlisted value is applied locally, reads back canonical, retries clean | yes |
| endpoint | TestPropertyRoundTripCoversEveryKey | sim* | 0.02 |  | 55 | 0 |  | props-roundtrip | every allowlisted key yields table rows | yes |
| endpoint | TestReceivedCopyIsNotOwned | sim | 0 |  | 503 | 0 |  | ownership | received properties are not ownership | yes |
| endpoint | TestReconcileAgainstDesiredSet | both | 0 | 0.76 | 641 | 0 | 7 | reconcile-e2e | reconcile against a desired set collects in dependency order | yes |
| endpoint | TestReconcileCollectsAbandonedCreate | both | 0 | 0.17 | 403 | 0 | 3 | reconcile-e2e | abandoned creates collected when old; dry run applies nothing | yes |
| endpoint | TestReconcileCollectsLeakedCloneSource | both | 0.01 | 0.36 | 516 | 0 | 8 | reconcile-e2e | leaked clone-source snapshots collected, used ones kept | yes |
| endpoint | TestReconcileLocksTargets | sim | 0 | 0.05 | 550 | 6 |  | reconcile-locks | reconcile aborts on a held lock; dry run takes none |  |
| endpoint | TestReconcileSparesReceivedAndRenamed | sim | 0.01 |  | 508 | 0 |  | ownership | reconcile spares received and renamed datasets | yes |
| endpoint | TestReserveRestoreIsIdempotent | both | 0.04 |  | 521 | 1 | 1 | create-idempotency | reserve-restores reserve the source size, retry no-op |  |
| endpoint | TestScanRecordsCoverage | sim* | 0 |  | 222 | 0 |  | plan-coverage | scans record exactly which ids they cover | yes |
| endpoint | TestShareNFSAndClientEncoding | sim* | 0 | 0.00 | 77 | 2 |  | export-encoding | sharenfs/client encoding is canonical and round-trips |  |
| endpoint | TestSimulateDestroyReapsDeferredOrigins | sim* | 0 | 0.00 | 40 | 0 |  | reconcile-plan | deferred destroys reap when the last clone goes | yes |
| endpoint | TestSnapshotLifecycleWithClones | both | 0.02 | 0.68 | 559 | 0 | 0 | snapshot-lifecycle | snapshot ids/global names, restore with growth, deferred delete |  |
| endpoint | TestThickCloneRetryIsIdempotent | both | 0 | 0.30 | 447 | 0 | 0 | create-idempotency | thick clone retry succeeds | yes |
| endpoint | TestUnknownFailureAfterAMutationIsRetryable | sim | 0 |  | 414 | 0 |  | error-mapping | unknown error after a mutation is Aborted, before Internal | yes |
| endpoint | TestValidatePropertyValues | sim* | 0 | 0.00 | 57 | 2 |  | props-canon | allowlisted values canonicalise to what zfs prints |  |
| endpoint | TestVolumeNameMustMatchID | both | 0 |  | 338 | 1 | 1 | ownership | markers naming another volume are not served |  |
| endpoint | TestZfsErrCodes | sim* | 0 |  | 39 | 7 |  | error-mapping | each zfs error Kind maps to its CSI code |  |
| server | TestFileExport | unit | 0.01 | 0.00 | 554 | 7 |  | grpc-surface | gRPC file export gated by capability |  |
| server | TestReconcile | unit | 0 | 0.00 | 461 | 11 |  | grpc-surface | gRPC reconcile: capability, young skip, input validation |  |
| server | TestRoundTripAndCodes | unit | 0.01 | 0.00 | 601 | 3 |  | grpc-surface | gRPC maps endpoint codes and fields |  |
| unixsock | TestListenIsOwnerOnly | unit | 0 |  | 7 | 0 |  | unixsock | socket is owner-only | yes |
| unixsock | TestListenNeverDeletesANonSocket | unit | 0 |  | 4 | 1 |  | unixsock | a non-socket path is never deleted |  |
| unixsock | TestListenRefusesALiveSocket | unit | 0.15 |  | 10 | 2 |  | unixsock | a live socket is not replaced |  |
| unixsock | TestListenReplacesAStaleSocket | unit | 0 |  | 9 | 1 |  | unixsock | a stale socket is replaced |  |
| zfs | FuzzClassify | unit | 0 | 0.00 | 4 | 0 |  | zfs-classify | classify returns a valid Kind for any text (fuzz) | yes |
| zfs | FuzzParseColumns | unit | 0 | 0.00 | 28 | 1 |  | zfs-parse | parser output shape is consistent (fuzz) |  |
| zfs | TestClassify | unit | 0 | 0.00 | 26 | 0 |  | zfs-classify | known stderr texts classify to their Kind | yes |
| zfs | TestExistsDoesNotSwallowUnknownErrors | unit | 0 | 0.00 | 34 | 1 |  | zfs-classify | only a classified not-found is absent |  |
| zfs | TestKilledCommandIsInterrupted | unit | 0.36 |  | 45 | 18 |  | zfs-runner | a killed command is Interrupted with the ctx error, bounded |  |
| zfs | TestLifecycleAgainstSim | unit | 0 | 0.00 | 119 | 4 |  | sim-fidelity | client create/get/snapshot/clone/defer-destroy/list on sim |  |
| zfs | TestParseColumnsRejectsForgedLines | unit | 0 | 0.00 | 26 | 1 |  | zfs-parse | duplicate (forged) lines fail the scan |  |
| zfs | TestPropLocalIsOnlyLocal | unit | 0 |  | 1 | 0 |  | ownership | only source=local is local | yes |
| zfs | TestRejectsFlagLikeAndShellNames | unit | 0 | 0.00 | 12 | 3 |  | zfs-argv | flag-like/shell names and newline values refused |  |
| zfs | TestSimPrintsIndexPropertiesLikeOpenZFS | unit | 0 |  | 62 | 0 |  | sim-fidelity | sim prints index props like OpenZFS 2.4 | yes |
