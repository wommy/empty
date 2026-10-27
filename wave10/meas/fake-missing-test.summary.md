### real ZFS VM: stonking/current (prebaked)
- image serial: `20261025`, sha256 `feedface` (gpg-verified SHA256SUMS at bake)
- baked: 2026-10-27 (inputs `0123456789abcdef`)
- kernel: `7.3.0-9-generic`
- zfs version: `zfs-2.4.4-1ubuntu4 zfs-kmod-2.4.4-1ubuntu4 ` sysfs `2.4.4-1ubuntu4`
| leg | package | skips | skipped tests |
|---|---|---|---|
| vm | endpoint | 20 skipped / 17 allowed | TestBusyIsMappedByOperation TestCloneFailureAfterCloneConvergesToSameState/snapshot TestCloneFailureAfterCloneConvergesToSameState/snapshot-reserve TestCloneFailureAfterCloneConvergesToSameState/volume TestCloneFailureAfterCloneConvergesToSameState/volume-reserve TestCloneResizeFailureConverges TestConcurrentCreatesSerialize TestDeleteVolumeSparesForeignSnapshots TestFileExportDroppedHost TestFileExportReceivedShareNFS TestFileExportRequiresNFSD TestFileExportResolvesSymlinks TestFileExportUnmountedVolume TestFileExportVerifiesKernelTable TestMissingParentIsUnavailableForWholeScans TestMissingParentIsUnavailableNotAbsent TestReceivedCopyIsNotOwned TestReconcileLocksTargets TestReconcileSparesReceivedAndRenamed TestUnknownFailureAfterAMutationIsRetryable |
| vm | csi | 0 skipped / 0 allowed |  |
| vm | csi | 0 skipped / 0 allowed |  |
