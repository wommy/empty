| test | part (r90) | old weight | r90 | r89 | r76 |
|---|---|---|---|---|---|
| TestPropertyRoundTrip | 01 | 19.33 | 16.24 | 15.87 | 14.04 |
| TestCrashAtEveryMutationConverges | 0 | 13.39 | 12.46 | 13.55 | 8.99 |
| TestRealFacts | 1 | 11.5 | 6.29 | 4.19 | 4.43 |
| TestZFSDiffReplay | 0 | 5.85 | 5.73 | 6.24 | 3.97 |
| TestModel | 0 | 3.01 | 2.80 | 3.23 | 1.16 |
| TestNFSReplay | 1 | 2.29 | 1.20 | 0.93 | 0.90 |
| TestDestroyNeverHitsAReplacedDataset | 1 | 1.99 | 1.03 | 0.69 | 1.73 |
| TestReconcileAgainstDesiredSet | 0 | 0.94 | 1.35 | 1.38 | 0.76 |
| TestCloneSourcesMustBeOwned | 0 | 0.89 | 1.24 | 1.26 | 0.49 |
| TestDeleteVolumeScanIsSubtreeScoped | 0 | 0.69 | 1.24 | 1.03 | 0.51 |
| TestFileExportServedBeyondRequest | 1 | 1.19 | 0.57 | 0.56 | 0.53 |
| TestFileExportCreateRootOwner | 1 | 0.91 | 0.33 | 0.23 | 0.48 |
| TestFileExportAdminWorldEditIsForeign | 1 | 0.91 | 0.55 | 0.29 | 0.44 |
| TestFileExportUnpublishStillServed | 1 | 0.87 | 0.62 | 0.49 | 0.29 |
| TestCreateVolumeProperties | 0 | 0.56 | 0.83 | 0.94 | 0.39 |
| TestFileExportStaleShareOnlySharenfsHeals | 1 | 0.8 | 0.17 | 0.09 | - |
| TestFileExportLeftoverClientList | 1 | 0.79 | 0.54 | 0.33 | 0.44 |
| TestFileExportCreateServerElsewhere | 1 | 0.7 | 0.29 | 0.14 | 0.29 |
| TestReserveRestoreIsIdempotent | 1 | 0.68 | 0.38 | 0.27 | 0.61 |
| TestCloneLargerThanLimitIsOutOfRange | 0 | 0.53 | 0.65 | 0.24 | 0.54 |
| TestFileExportCreate | 1 | 0.61 | 0.40 | 0.14 | 0.38 |
| TestCreateVolumePinsMountpoint | 0 | 0.47 | 0.61 | 0.24 | 0.30 |
| TestFilesystemCloneNeverSmallerThanSource | 1 | 0.56 | 0.34 | 0.25 | 0.59 |
| TestReconcileCollectsLeakedCloneSource | 0 | 0.55 | 0.54 | 0.66 | 0.26 |
| TestSnapshotSizeIsItsSizeWhenTaken | 1 | 0.55 | 0.27 | - | - |
| TestReconcileNeedsADigestToCollectUndesired | 0 | 0.52 | 0.54 | 0.21 | 0.25 |
| TestSnapshotLifecycleWithClones | 1 | 0.53 | 0.37 | 0.71 | 0.45 |
| TestExpandThickFilesystemRetryAfterENOSPC | 1 | 0.51 | 0.38 | 0.77 | 0.45 |
| TestCloneFromVolumeReleasesInternalSnapshot | 0 | 0.39 | 0.51 | 0.20 | 0.34 |
| TestRealGetJSONMatchesSim | 1 | 0.47 | 0.33 | 0.67 | 0.23 |
| TestResumeConvergesThickToThin | 0 | 0.46 | 0.31 | 0.15 | 0.24 |
| TestPagination | 1 | 0.45 | 0.32 | 0.67 | 0.29 |
| TestFileExportLifecycle | 1 | 0.43 | 0.15 | 0.12 | 0.14 |
| TestFileExportPreconditions | 1 | 0.41 | 0.15 | 0.10 | 0.19 |
| TestFileExportCreateServerSurvivesRestart | 1 | 0.41 | 0.21 | 0.11 | 0.25 |
| TestReconcileDigestCoversTheGuid | 1 | (none: 0.1) | 0.32 | 0.70 | 0.33 |
| TestVolumeNameMustMatchID | 1 | 0.28 | 0.16 | 0.48 | 0.23 |

not in weights file (weigh 0.1): TestReconcileDigestCoversTheGuid 0.32, TestMinFreeBytesGuard 0.01, TestGetCapacityIsChildSpace 0.00, TestChildAvail 0.00, TestMinFreeBytesGuardPoolRootRefquota 0.00, TestPlanReserves 0.00, TestMinFreeBytesGuardLevelUpUnreadable 0.00, TestReconcileLockedReplanKeepsTheDigestedGuid 0.00, TestThickZvolReservationIgnoresCopies 0.00, TestCheckHeadroom 0.00, TestMinFreeBytesGuardParentSpace 0.00
