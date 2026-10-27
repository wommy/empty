| row | status | covering tests now | change |
|---|---|---|---|
| OWN-01 | current | TestPropLocalIsOnlyLocal, TestInheritedOwnershipIsNotOwnership, TestReceivedCopyIsNotOwned |  |
| OWN-02 | current | TestVolumeNameMustMatchID, TestReconcileSparesReceivedAndRenamed |  |
| OWN-03 | open-finding | - |  |
| OWN-04 | current | TestInheritedOwnershipIsNotOwnership, TestReceivedCopyIsNotOwned, TestDeleteVolumeSparesForeignSnapshots |  |
| OWN-05 | current | TestNeverAdoptsOrDeletesUnmanaged, TestGoldenPlans/create/foreign, create/incompatible-* | removed: TestPlanCreateRefusesForeignAndIncompatible |
| OWN-06 | current | TestReceivedCopyIsNotOwned, TestReconcileSparesReceivedAndRenamed, FuzzReconcileSafe |  |
| OWN-07 | current | TestCloneSourcesMustBeOwned |  |
| OWN-08 | current | TestCloneNeverAdoptsARacingDataset |  |
| OWN-09 | current-untested | - |  |
| OWN-10 | current | TestDeleteVolumeSparesForeignSnapshots |  |
| OWN-11 | current | TestCloneFromVolumeReleasesInternalSnapshot | removed: TestDeleteVolumeAfterItsCloneIsGone; SOLE killer replaced |
| OWN-12 | open-finding | - |  |
| OWN-13 | open-finding | - |  |
| OWN-14 | open-finding | - |  |
| OWN-15 | open-finding | - |  |
| OWN-16 | current | TestDefaultPropertyPrefixIsOurOwn, TestPropertyPrefixIsValidated |  |
| OWN-17 | current | TestFileExportAdoptedVolumes, TestCloneNeverAdoptsARacingDataset |  |
| VOL-01 | current | FuzzCreateConverges, TestCreateVolumeIdempotent, TestCreateVolumeProperties, TestReserveRestoreIsIdempotent, TestRoundTripAndCodes, TestThickCloneRetry | removed: TestThickCloneRetryIsIdempotent |
| VOL-02 | current | TestCreateVolumeIdempotent, TestPlanCreateAppliesProperties, FuzzCreateConverges |  |
| VOL-03 | current | TestGoldenPlans/create/fs-limit-only | removed: TestPlanCreateHonorsLimitOnly; SOLE killer replaced |
| VOL-04 | current | TestPlanCreateAppliesProperties |  |
| VOL-05 | current | TestCreateVolumeIdempotent, TestParametersAndAccessChecks, TestRoundTripAndCodes, TestGoldenPlans/create/foreign, create/incompatible-* | removed: TestPlanCreateRefusesForeignAndIncompatible |
| VOL-06 | open-finding | - |  |
| VOL-07 | current | TestCreateVolumeIdempotent, TestCreateVolumeResumesInterruptedProvision, TestGoldenPlans/create/uncommitted-clone-converges | removed: TestPlanCreateConvergesUncommittedClone |
| VOL-08 | current | TestCreateVolumeResumesInterruptedProvision, TestPlanCreatePropertiesConvergeUncommitted, TestCreateVolumeProperties, TestGoldenPlans/create/uncommitted-clone-converges | removed: TestPlanCreateConvergesUncommittedClone |
| VOL-09 | open-finding | - |  |
| VOL-10 | current | TestPlanCreateAppliesProperties, TestGoldenPlans/create/fs | removed: TestPlanCreateFilesystemsAreNotShared |
| VOL-11 | current | TestGoldenPlans/create/zvol-thick, create/thick-on-sparse-zvol, TestGoldenPlans/create/uncommitted-clone-converges | removed: TestPlanCreateThickZvolLeavesReservationToZFS, TestPlanCreateConvergesUncommittedClone; SOLE killer replaced |
| VOL-12 | current | TestLifecycleAgainstSim, TestParametersAndAccessChecks |  |
| VOL-13 | current | TestReserveRestoreIsIdempotent, TestCloneFailureAfterCloneConvergesToSameState |  |
| VOL-14 | current | TestConcurrentCreatesSerialize |  |
| VOL-15 | current-untested | - |  |
| VOL-16 | current | TestCreateVolumeProperties, TestPlanCreateAppliesProperties |  |
| VOL-17 | current | TestPlanCreatePropertiesConvergeUncommitted |  |
| VOL-18 | current | TestRoundTripAndCodes, TestFileExportLifecycle |  |
| VOL-19 | open-finding | - |  |
| CLN-01 | current | TestSnapshotLifecycleWithClones |  |
| CLN-02 | current | TestCloneFromVolumeReleasesInternalSnapshot, TestReconcileCollectsLeakedCloneSource, TestGoldenPlans/delete/* | removed: TestPlanDeleteVolume |
| CLN-03 | current | TestCloneLargerThanLimitIsOutOfRange |  |
| CLN-04 | current | TestReserveRestoreIsIdempotent |  |
| CLN-05 | current | TestCloneFailureAfterCloneConvergesToSameState, TestCloneResizeFailureConverges, TestGoldenPlans/create/uncommitted-clone-converges | removed: TestPlanCreateConvergesUncommittedClone |
| CLN-06 | current-untested | - |  |
| CLN-07 | current-untested | - |  |
| CLN-08 | current | TestSnapshotLifecycleWithClones, TestGoldenPlans/restore/malformed-source, clone/malformed-source | removed: TestPlanCreateMalformedSourceIsNotFound |
| CLN-09 | current-untested | - |  |
| DEL-01 | current | TestDeleteVolume, TestRoundTripAndCodes, TestGoldenPlans/delete/* | removed: TestPlanDeleteVolume |
| DEL-02 | current | TestDeleteVolume, TestRoundTripAndCodes, TestGoldenPlans/delete/* | removed: TestPlanDeleteVolume |
| DEL-03 | current | TestCloneFromVolumeReleasesInternalSnapshot, TestGoldenPlans/delete/* | removed: TestPlanDeleteVolume |
| DEL-04 | current | TestPlanReconcile, TestGoldenPlans/delete/* | removed: TestPlanDeleteVolume |
| DEL-05 | current | TestSnapshotLifecycleWithClones, TestLifecycleAgainstSim, TestSimulateDestroyReapsDeferredOrigins |  |
| DEL-06 | open-finding | - |  |
| DEL-07 | current | TestBusyIsMappedByOperation |  |
| DEL-08 | open-finding | - |  |
| SNP-01 | current | TestSnapshotLifecycleWithClones, TestPlanSnapshotLongAndUnusualNames, TestReceivedCopyIsNotOwned |  |
| SNP-02 | current | TestSnapshotLifecycleWithClones, TestPlannersRequireACoveringTable |  |
| SNP-03 | current | TestPlanSnapshotLongAndUnusualNames |  |
| SNP-04 | current | TestPlanSnapshotLongAndUnusualNames |  |
| SNP-05 | current | TestGoldenPlans/snapshot/reserved-prefix | removed: TestPlanSnapshotReservedPrefix; SOLE killer replaced |
| SNP-06 | current | TestSnapshotLifecycleWithClones |  |
| SNP-07 | current | TestSnapshotLifecycleWithClones, TestPlannersRequireACoveringTable |  |
| SNP-08 | current | TestInheritedOwnershipIsNotOwnership, TestReceivedCopyIsNotOwned |  |
| SNP-09 | current | TestSnapshotLifecycleWithClones, TestInheritedOwnershipIsNotOwnership, TestReceivedCopyIsNotOwned |  |
| SNP-10 | current-untested | - |  |
| SNP-11 | current | TestSnapshotLifecycleWithClones |  |
| SNP-12 | current | TestNeverIssuedIDs |  |
| SNP-13 | decided-pending | - |  |
| SNP-14 | current | TestVolumeNameMustMatchID |  |
| EXP-01 | current | TestExpand, TestGoldenPlans/expand/* | removed: TestPlanExpand |
| EXP-02 | current | TestExpand |  |
| EXP-03 | current | TestExpandUnlimitedFilesystem, TestGoldenPlans/expand/* | removed: TestPlanExpand |
| EXP-04 | current | TestExpandNodeExpansionRequired |  |
| EXP-05 | current | TestDecodeRejectsImpossibleZvolSizes |  |
| LST-01 | current | TestPagination, TestRoundTripAndCodes |  |
| LST-02 | current | TestCreateVolumeResumesInterruptedProvision, TestVolumeNameMustMatchID, TestReceivedCopyIsNotOwned |  |
| LST-03 | current | TestRoundTripAndCodes |  |
| LST-04 | open-finding | - |  |
| ID-01 | current | TestNeverIssuedIDs, TestSnapshotLifecycleWithClones, TestGoldenPlans/restore/malformed-source, clone/malformed-source | removed: TestPlanCreateMalformedSourceIsNotFound |
| ERR-01 | current | TestExistsDoesNotSwallowUnknownErrors, TestClassify |  |
| ERR-02 | current | TestMissingParentIsUnavailableNotAbsent, TestMissingParentIsUnavailableForWholeScans |  |
| ERR-03 | current | TestClassify, FuzzClassify |  |
| ERR-04 | current | TestZfsErrCodes |  |
| ERR-05 | current | TestBusyIsMappedByOperation |  |
| ERR-06 | current | TestUnknownFailureAfterAMutationIsRetryable |  |
| ERR-07 | current | TestKilledCommandIsInterrupted, TestZfsErrCodes |  |
| ERR-08 | current | TestReconcileLocksTargets |  |
| ERR-09 | current | TestApplyRefusesAnUnknownOpKind, TestOpKindString |  |
| ERR-10 | current | TestPlanReconcile, FuzzReconcileSafe, TestCloneNeverAdoptsARacingDataset |  |
| ERR-11 | current | TestUnknownFailureAfterAMutationIsRetryable |  |
| ERR-12 | open-finding | - |  |
| ERR-13 | decided-pending | - |  |
| SCN-01 | current | TestParseColumnsRejectsForgedLines, FuzzParseColumns |  |
| SCN-02 | open-finding | - |  |
| SCN-03 | current | FuzzParseColumns |  |
| SCN-04 | decided-pending | - |  |
| SCN-05 | current | TestDecodeRejectsImpossibleZvolSizes |  |
| SCN-06 | open-finding | - |  |
| SCN-07 | decided-pending | - |  |
| SCN-08 | open-finding | - |  |
| SCN-09 | open-finding | - |  |
| SCN-10 | current | TestPlannersRequireACoveringTable, TestScanRecordsCoverage |  |
| SCN-11 | open-finding | - |  |
| SCN-12 | current | TestRejectsFlagLikeAndShellNames |  |
| SCN-13 | current | TestSimPrintsIndexPropertiesLikeOpenZFS |  |
| SCN-14 | current | TestLifecycleAgainstSim |  |
| PRP-01 | current | TestPlanCreatePropertiesDenyList, TestCreateVolumeProperties, FuzzCreateConverges, TestRoundTripAndCodes |  |
| PRP-02 | current | TestPlanCreatePropertiesDenyList |  |
| PRP-03 | current | TestValidatePropertyValues, TestSimPrintsIndexPropertiesLikeOpenZFS |  |
| PRP-04 | current | TestPlanCreateAppliesProperties, TestCreateVolumeProperties, FuzzCreateConverges |  |
| PRP-05 | current | TestPlanCreatePropertiesRetry, TestRoundTripAndCodes |  |
| PRP-06 | current | TestPlanCreatePropertiesConvergeUncommitted, TestCreateVolumeProperties |  |
| PRP-07 | decided-pending | - |  |
| PRP-08 | current | TestParametersAndAccessChecks |  |
| NFS-01 | decided-pending | - |  |
| NFS-02 | decided-pending | TestShareNFSAndClientEncoding |  |
| NFS-03 | current | TestPlanPublishExport, TestFileExportAdoptedVolumes, TestPlanUnpublishExport |  |
| NFS-04 | current | TestPlanUnpublishExport, TestFileExportLifecycle, TestExportConverges |  |
| NFS-05 | current | TestFileExportVerifiesKernelTable, TestPlanPublishExport, TestPlanUnpublishExport |  |
| NFS-06 | decided-pending | - |  |
| NFS-07 | current | TestFileExportLifecycle, TestPlanPublishExport, TestPlanUnpublishExport, TestExportConverges, TestFileExport |  |
| NFS-08 | open-finding | TestParseClient, TestFileExportPreconditions, TestFileExport |  |
| NFS-09 | open-finding | TestParseEtab |  |
| NFS-10 | current | TestFileExportPreconditions, TestPlanPublishExport |  |
| NFS-11 | open-finding | TestPlanUnpublishExport |  |
| NFS-12 | open-finding | - |  |
| NFS-13 | current | TestFileExportPreconditions, TestFileExport |  |
| NFS-14 | decided-pending | - |  |
| NFS-15 | decided-pending | - |  |
| NFS-16 | current | TestFileExportAdoptedVolumes, TestPlanPublishExport |  |
| NFS-17 | decided-pending | - |  |
| NFS-18 | decided-pending | - |  |
| NFS-19 | decided-pending | - |  |
| NFS-20 | decided-pending | - |  |
| NFS-21 | current | TestFileExportLifecycle |  |
| NFS-22 | decided-pending | TestFileExport |  |
| REC-01 | current | TestPlanReconcile, FuzzReconcileSafe |  |
| REC-02 | current | FuzzReconcileSafe, TestPlanReconcile, TestReconcileSparesReceivedAndRenamed, TestReconcileAgainstDesiredSet |  |
| REC-03 | current | TestPlanReconcile, TestReconcileAgainstDesiredSet, FuzzReconcileSafe |  |
| REC-04 | open-finding | - |  |
| REC-05 | current | TestReconcileCollectsAbandonedCreate, TestPlanReconcile |  |
| REC-06 | open-finding | - |  |
| REC-07 | current | TestPlanReconcile, TestReconcileAgainstDesiredSet, TestSimulateDestroyReapsDeferredOrigins |  |
| REC-08 | current | TestPlanReconcile, TestReconcileCollectsLeakedCloneSource |  |
| REC-09 | current | TestReconcileCollectsAbandonedCreate, TestReconcileLocksTargets, FuzzReconcileSafe |  |
| REC-10 | current | TestReconcileLocksTargets, TestPlanReconcileHeldLimitsActions, TestPlanReconcile (lock-cover loop) | removed: TestPlanReconcileLocksEveryTarget |
| REC-11 | current | TestReconcileCollectsAbandonedCreate, TestReconcileAgainstDesiredSet, FuzzReconcileSafe |  |
| REC-12 | open-finding | - |  |
| REC-13 | current | TestReconcileAgainstDesiredSet, TestReconcile |  |
| REC-14 | current | TestPlanReconcile, TestReconcile, TestDeleteVolumeSparesForeignSnapshots |  |
| REC-15 | current-untested | - |  |
| LCK-01 | current | TestConcurrentCreatesSerialize, TestReconcileLocksTargets |  |
| LCK-02 | decided-pending | - |  |
| LCK-03 | current | TestLockParentIsExclusive |  |
| RT-01 | current | TestListenIsOwnerOnly, TestListenRefusesALiveSocket, TestListenReplacesAStaleSocket, TestListenNeverDeletesANonSocket |  |
| RT-02 | decided-pending | - |  |
| RT-03 | decided-pending | - |  |
| RT-04 | decided-pending | - |  |
| INV-01 | current | (not a Go test name: (all plan_test.go, props_test.go, export_test.go, reconcile_test.go tests)) |  |
| INV-02 | decided-pending | FuzzCreateConverges, TestExportConverges |  |
| INV-03 | decided-pending | - |  |
| CR-01 | current | TestCloneFailureAfterCloneConvergesToSameState, TestCreateVolumeResumesInterruptedProvision |  |
| CR-02 | current-untested | - |  |
| CR-03 | current-untested | - |  |
| CR-04 | current-untested | - |  |
| CSI-01 | current | TestCapabilitiesAdvertised |  |
| CSI-02 | current | TestParametersAndAccessChecks, TestNeverIssuedIDs |  |
| CSI-03 | current | TestExpandNodeExpansionRequired |  |
| CSI-04 | current | TestSanity |  |
| CSI-05 | current-untested | - |  |
| CSI-06 | open-finding | - |  |
| PRF-01 | current-untested | - |  |
| PRF-02 | decided-pending | - |  |
| PRF-03 | decided-pending | - |  |
| PRF-04 | decided-pending | - |  |
| PRF-05 | decided-pending | - |  |
| PRF-06 | decided-pending | - |  |
