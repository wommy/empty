# Mutation sweep raw table

Snapshot of commit 96c49ff. Mutant definitions (exact old/new text): mutants.py. Per-mutant JSON: results*.jsonl.

| # | id | file:line | cat | risk | conc | status | killed by / note | mutation |
|---|---|---|---|---|---|---|---|---|
| 1 | own-vol-name | endpoint/table.go:108 | ownership | D |  | killed | TestLiteralDashMarkerIsAValue (internal/endpoint) | OwnedVolume ignores the csi_volume_name marker (send/recv / renamed copy looks owned) |
| 2 | own-vol-managed | endpoint/table.go:108 | ownership | D |  | killed | TestGoldenPlans (internal/endpoint) | OwnedVolume ignores managed_resource |
| 3 | own-snap-source | endpoint/table.go:125 | ownership | D |  | killed | TestOwnershipDifferentialFixtures (internal/endpoint) | OwnedSnapshot ignores snapshot_source_volume_id mismatch |
| 4 | own-snap-managed | endpoint/table.go:124 | ownership | D |  | killed | TestRowOwnedSnapshot (internal/endpoint) | OwnedSnapshot ignores managed_resource |
| 5 | own-clonesrc-for | endpoint/table.go:138 | ownership | D |  | killed | TestAdv4ReleasedOriginOutsideParent (internal/endpoint) | OwnedCloneSource ignores clone_source_for stamp |
| 6 | own-local-only | endpoint/table.go:371 | ownership | D | yes | killed | TestNFS_N16_InheritedServerIgnored (internal/csi) | ownership markers accepted when inherited (every child/snapshot of a marked dataset looks owned) |
| 7 | rec-owned | endpoint/reconcile.go:269 | reconcile-ownership | D | yes | killed | TestPlanReconcile (internal/endpoint) | reconcile candidate loop drops OwnedVolume (KNOWN gap ~:269) |
| 8 | rec-owned-managed | endpoint/reconcile.go:269 | reconcile-ownership | D | yes | survived |  | reconcile candidate loop weakened to managed-only (KNOWN gap ~:269 variant) |
| 9 | rec-want | endpoint/reconcile.go:269 | reconcile-refusal | D |  | killed | TestReconcileAgesFromCommit (internal/endpoint) | reconcile collects volumes IN the desired set |
| 10 | rec-stated | endpoint/reconcile.go:257 | reconcile-refusal | D |  | killed | TestAdv4PartialDesiredSetMassDeletes (internal/endpoint) | a desired set naming none of ours still collects committed volumes (adv4 F2) |
| 11 | rec-stated-own | endpoint/reconcile.go:257 | reconcile-refusal | D |  | killed | TestPlanReconcile (internal/endpoint) | a desired set naming only foreign/unowned datasets counts as stated |
| 12 | rec-empty-desired | endpoint/reconcile.go:274 | reconcile-refusal | D |  | killed | TestAdv4PartialDesiredSetMassDeletes (internal/endpoint) | empty-desired skip removed |
| 13 | rec-tooyoung | endpoint/reconcile.go:489 | reconcile-refusal | D |  | killed | TestAdv4TinyMinAgeInFlightCreate (internal/endpoint) | min-age gate removed |
| 14 | rec-committedat | endpoint/reconcile.go:486 | reconcile-refusal | D |  | killed | TestReconcileAgesFromCommit (internal/endpoint) | age ignores committed_at (adv4 F1) |
| 15 | rec-nocreation | endpoint/reconcile.go:482 | reconcile-refusal | D |  | survived |  | row with no creation time treated as 1970 (old enough) |
| 16 | rec-refuses-held | endpoint/reconcile.go:142 | lock | D |  | killed | TestAdv4OneBusyLockAbortsWholeRun (internal/endpoint) | refuses() ignores locks not held |
| 17 | rec-refuses-guid | endpoint/reconcile.go:153 | guid-recheck | D |  | killed | TestPlanReconcileHeldLimitsActions (internal/endpoint) | locked re-plan accepts an action whose guid/reason changed since the unlocked plan |
| 18 | rec-refuses-planned | endpoint/reconcile.go:152 | guid-recheck | D |  | killed | TestPlanReconcileHeldLimitsActions (internal/endpoint) | locked re-plan accepts actions not in the dry/unlocked plan |
| 19 | rec-vol-refuses | endpoint/reconcile.go:318 | lock | D |  | survived |  | volume groups skip refuses() under locks |
| 20 | rec-vol-refuses2 | endpoint/reconcile.go:322 | lock | D |  | killed | TestAdv4OneBusyLockAbortsWholeRun (internal/endpoint) | volume groups skip refuses() under locks (result ignored) |
| 21 | rec-src-refuses | endpoint/reconcile.go:382 | lock | D |  | survived |  | orphan clone-source destroys skip refuses() under locks |
| 22 | rec-src-unstamped | endpoint/reconcile.go:359 | reconcile-ownership | D |  | killed | TestAdv4UserSnapshotWithReservedPrefixCollected (internal/endpoint) | unstamped clone-source snapshots (admin's/democratic-csi's) collected |
| 23 | rec-src-inuse | endpoint/reconcile.go:355 | reconcile-refusal | D |  | killed | TestPlanReconcile (internal/endpoint) | clone source with live clones collected (defer-destroyed) |
| 24 | rec-src-vol-owned | endpoint/reconcile.go:363 | reconcile-ownership | D |  | killed | TestPlanReconcile (internal/endpoint) | clone source on an unmanaged volume collected |
| 25 | rec-src-addressable | endpoint/reconcile.go:369 | validation | I |  | killed | TestPlanReconcile (internal/endpoint) | unaddressable clone-source names planned |
| 26 | rec-maxdel | endpoint/reconcile.go:690 | reconcile-refusal | D | yes | survived |  | -max-deletions gate removed (KNOWN unexercised) |
| 27 | rec-maxdel-ge | endpoint/reconcile.go:690 | reconcile-refusal | D | yes | survived |  | -max-deletions off by one (allows limit+1) |
| 28 | rec-maxdel-default | endpoint/reconcile.go:690 | reconcile-refusal | D | yes | survived |  | default max-deletions effectively unlimited |
| 29 | rec-digest-required | endpoint/reconcile.go:681 | reconcile-refusal | D |  | killed | TestReconcileNeedsADigestToCollectUndesired (internal/endpoint) | apply without a dry-run digest |
| 30 | rec-digest-mismatch | endpoint/reconcile.go:687 | reconcile-refusal | D |  | killed | TestReconcileMaxDeletionsAndDigest (internal/endpoint) | stale digest accepted |
| 31 | rec-dryrun | endpoint/reconcile.go:678 | reconcile-refusal | D |  | killed | TestAdv4OneFailureStarvesAndHidesDestroys (internal/endpoint) | dry run applies |
| 32 | rec-max-ids | endpoint/reconcile.go:651 | validation | V |  | killed | TestReconcileRefusesAnOversizedDesiredSet (internal/endpoint) | desired-set size cap removed |
| 33 | rec-empty-id | endpoint/reconcile.go:655 | validation | V |  | killed | TestReconcileAgainstDesiredSet (internal/endpoint) | empty desired id accepted |
| 34 | rec-maxdel-args | endpoint/reconcile.go:659 | validation | V |  | killed | TestReconcileMaxDeletionsAndDigest (internal/endpoint) | negative / contradictory max_deletions accepted |
| 35 | rec-minage-floor | endpoint/reconcile.go:662 | validation | D |  | killed | TestAdv4TinyMinAgeInFlightCreate (internal/endpoint) | min age below the floor accepted |
| 36 | rec-whole | endpoint/reconcile.go:233 | containment | D |  | killed | TestPlannersRequireACoveringTable (internal/endpoint) | reconcile plans on a partial table |
| 37 | rec-trylock | endpoint/reconcile.go:713 | lock | D |  | killed | TestAdv4OneBusyLockAbortsWholeRun (internal/endpoint) | reconcile treats busy volume locks as held |
| 38 | rec-replan | endpoint/reconcile.go:724 | lock | D |  | killed | TestAdv4OneBusyLockAbortsWholeRun (internal/endpoint) | locked re-plan ignores held set and unlocked plan |
| 39 | apply-guid-empty | endpoint/apply.go:28 | guid-recheck | D |  | killed | TestApplyRefusesADestroyWithoutGUID (internal/endpoint) | destroy without plan guid accepted |
| 40 | apply-guid-cmp | endpoint/apply.go:117 | guid-recheck | D |  | killed | TestDestroyNeverHitsAReplacedDataset (hang: 5m test timeout) | sameDataset never refuses a replaced dataset |
| 41 | apply-guid-first | endpoint/apply.go:85 | guid-recheck | D |  | killed | TestDestroyNeverHitsAReplacedDataset (hang: 5m test timeout) | first guid re-check before destroy skipped |
| 42 | apply-guid-retry | endpoint/apply.go:96 | guid-recheck | D |  | survived |  | guid re-check before the post-settle retry destroy skipped |
| 43 | apply-create-p | endpoint/apply.go:38 | ownership | D |  | killed | TestModel (internal/endpoint) | create with -p (stamps ownership onto an existing dataset) |
| 44 | del-owned | endpoint/plan.go:796 | ownership | D |  | killed | TestGoldenPlans (internal/endpoint) | PlanDeleteVolume of an unmanaged dataset |
| 45 | del-children | endpoint/plan.go:801 | containment | D |  | killed | TestAdv4ChildDatasetsDestroyed (internal/endpoint) | delete with foreign datasets below |
| 46 | del-csisnap | endpoint/plan.go:807 | ownership | D |  | killed | TestPlanReconcile (internal/endpoint) | delete with live CSI snapshot |
| 47 | del-dependent | endpoint/plan.go:810 | reconcile-refusal | D |  | killed | TestReconcileSkipsSourceOfFailedClone (internal/endpoint) | delete with dependent clone |
| 48 | del-holds | endpoint/plan.go:815 | reconcile-refusal | D |  | killed | TestListenLeavesUmaskAlone (internal/endpoint) | delete through a user hold |
| 49 | del-foreign | endpoint/plan.go:827 | ownership | D |  | killed | TestPlanReconcile (internal/endpoint) | foreign snapshots destroyed without opt-in |
| 50 | del-foreign-class | endpoint/plan.go:821 | ownership | D |  | killed | TestPlanReconcile (internal/endpoint) | no snapshot is classed foreign |
| 51 | origin-src-owned | endpoint/plan.go:864 | ownership | D |  | killed | TestModel (internal/endpoint) | released origin on an unowned source volume |
| 52 | origin-stamped | endpoint/plan.go:873 | ownership | D |  | killed | TestAdv4ReleasedOriginOutsideParent (internal/endpoint) | released origin stamped for another clone |
| 53 | origin-name | endpoint/plan.go:858 | ownership | D |  | killed | TestPlanDeleteVolumeReleasesOnlyItsOwnOrigin (internal/csi) | any origin snapshot under the parent released with the clone |
| 54 | origin-parent | endpoint/plan.go:853 | containment | D |  | killed | TestListenLeavesUmaskAlone (internal/csi) | origin outside the parent considered |
| 55 | relsrc-owned | endpoint/plan.go:716 | ownership | D |  | killed | TestModel (internal/endpoint) | releaseCloneSourcesFor on unowned source volume |
| 56 | relsrc-used | endpoint/plan.go:719 | reconcile-refusal | D |  | killed | TestListenLeavesUmaskAlone (internal/csi) | releaseCloneSourcesFor releases a source still used |
| 57 | relsrc-stamp | endpoint/plan.go:722 | ownership | D |  | killed | TestModel (internal/endpoint) | releaseCloneSourcesFor releases unstamped / other-clone sources |
| 58 | relsrc-whole | endpoint/plan.go:709 | containment | D |  | killed | TestPlannersRequireACoveringTable (internal/endpoint) | releaseCloneSourcesFor on an uncovering table |
| 59 | create-owned | endpoint/plan.go:348 | ownership | D |  | killed | TestGoldenPlans (internal/endpoint) | CreateVolume adopts a foreign dataset of the same name |
| 60 | create-kind-src | endpoint/plan.go:352 | idempotency | I |  | killed | TestModel (internal/endpoint) | repeat with other kind/source accepted |
| 61 | create-cap | endpoint/plan.go:653 | idempotency | I |  | killed | TestSanity (internal/csi) | repeat with bigger capacity accepted |
| 62 | create-limit | endpoint/plan.go:656 | idempotency | I |  | killed | TestCreateVolumeIdempotent (internal/endpoint) | repeat exceeding limit accepted |
| 63 | create-sparse | endpoint/plan.go:659 | idempotency | I |  | killed | TestParametersAndAccessChecks (internal/csi) | sparse mismatch accepted |
| 64 | create-reserve | endpoint/plan.go:662 | idempotency | I |  | killed | TestModel (internal/endpoint) | reserve mismatch accepted |
| 65 | create-props | endpoint/plan.go:398 | idempotency | I |  | killed | TestPlanCreatePropertiesRetry (internal/endpoint) | property mismatch on repeat accepted |
| 66 | create-volblock | endpoint/plan.go:360 | idempotency | I |  | killed | TestPlanCreatePropertiesConvergeUncommitted (internal/endpoint) | uncommitted resume ignores volblocksize mismatch |
| 67 | nfs-compat-foreign | endpoint/plan.go:269 | idempotency | E |  | survived |  | repeat accepts inherited/received sharenfs |
| 68 | nfs-compat-share | endpoint/plan.go:279 | idempotency | E |  | killed | TestNFS_N9_Idempotency (internal/csi) | repeat with different NFS clients accepted |
| 69 | nfs-compat-server | endpoint/plan.go:282 | idempotency | I |  | killed | TestNFS_N9_Idempotency (internal/csi) | repeat with different NFS server accepted |
| 70 | create-srcsnap-stamp | endpoint/plan.go:494 | ownership | D |  | killed | TestCloneRefusesUnstampedReservedSnapshot (internal/endpoint) | clone reuses a clone-source snapshot not cut for it |
| 71 | create-srcsnap-owned | endpoint/plan.go:450 | ownership | S |  | killed | TestModel (internal/endpoint) | clone from a snapshot the endpoint does not own |
| 72 | create-srcvol-served | endpoint/plan.go:474 | ownership | S |  | killed | TestGoldenPlans (internal/endpoint) | clone from any dataset under the parent |
| 73 | create-sharenfs-off | endpoint/plan.go:415 | nfs-exposure | E |  | killed | TestGoldenPlans (internal/endpoint) | new filesystem inherits the parent's sharenfs/sharesmb |
| 74 | create-nosuid | endpoint/plan.go:420 | nfs-exposure | S |  | killed | TestGoldenPlans (internal/endpoint) | setuid/exec/devices not forced off (F12) |
| 75 | create-mount-escape | endpoint/plan.go:427 | containment | S |  | killed | TestNFS_N5_ShareNFSCanonicalAndServed (internal/csi) | mountpoint pinned outside the parent's mountpoint |
| 76 | val-cap-max | endpoint/plan.go:155 | validation | V |  | killed | TestReachCapacityWrap (internal/endpoint) | capacity cap removed |
| 77 | val-one-source | endpoint/plan.go:162 | validation | V |  | killed | TestPlannerErrorCodes (internal/endpoint) | two content sources accepted |
| 78 | val-limit | endpoint/plan.go:180 | validation | V |  | killed | TestPlannerErrorCodes (internal/endpoint) | capacity over limit accepted |
| 79 | nfs-empty-host | endpoint/plan.go:218 | nfs-validation | E |  | survived |  | empty NFS client (export to everyone) accepted |
| 80 | nfs-bare-addr | endpoint/plan.go:225 | nfs-validation | E |  | killed | TestNFS_N2_ClientsValidated (internal/csi) | bare-address NFS client accepted |
| 81 | nfs-floor | endpoint/plan.go:228 | nfs-validation | E |  | killed | TestNFS_N2_ClientsValidated (internal/csi) | CreateVolume NFS client broader than -nfs-min-prefix accepted |
| 82 | nfs-overlap | endpoint/plan.go:231 | nfs-validation | E |  | killed | TestNFS_N2_ClientsValidated (internal/csi) | overlapping NFS clients accepted |
| 83 | nfs-server-adv | endpoint/plan.go:247 | nfs-validation | I |  | killed | TestNFS_N3_ServerSelection (internal/csi) | unadvertised NFS server accepted |
| 84 | nfs-block | endpoint/plan.go:206 | nfs-validation | V |  | killed | TestValidateNFSExport (internal/endpoint) | NFS export on zvol accepted |
| 85 | nfs-no-servers | endpoint/plan.go:209 | nfs-validation | V |  | killed | TestFileExportCreateNeedsServers (internal/endpoint) | NFS export without advertised server |
| 86 | nfs-no-clients | endpoint/plan.go:212 | nfs-validation | V |  | killed | TestValidateNFSExport (internal/endpoint) | NFS export with no clients |
| 87 | nfs-propsize | endpoint/plan.go:237 | nfs-validation | V |  | survived |  | oversize sharenfs accepted |
| 88 | snapdel-owned | endpoint/plan.go:1028 | ownership | D |  | killed | TestGoldenPlans (internal/endpoint) | DeleteSnapshot of an unmanaged snapshot |
| 89 | snap-reserved | endpoint/plan.go:967 | validation | D |  | killed | TestGoldenPlans (internal/endpoint) | CSI snapshot named with the reserved clone-source prefix |
| 90 | snap-name | endpoint/plan.go:964 | validation | V |  | killed | TestPlanSnapshotLongAndUnusualNames (internal/endpoint) | snapshot name validation removed |
| 91 | snap-src-served | endpoint/plan.go:989 | ownership | S |  | killed | TestReachUncommittedServed (internal/endpoint) | snapshot of a dataset the endpoint does not serve |
| 92 | snap-name-global | endpoint/plan.go:985 | idempotency | I |  | killed | TestSanity (internal/csi) | snapshot name reused across volumes |
| 93 | snap-whole | endpoint/plan.go:977 | containment | I |  | killed | TestPlannersRequireACoveringTable (internal/endpoint) | snapshot-name uniqueness checked on a partial table |
| 94 | expand-served | endpoint/plan.go:889 | ownership | S |  | killed | TestReachUncommittedServed (internal/endpoint) | expand a dataset the endpoint does not serve |
| 95 | expand-cap-max | endpoint/plan.go:892 | validation | V |  | killed | TestReachCapacityWrap (internal/endpoint) | expand capacity cap removed |
| 96 | expand-limit | endpoint/plan.go:899 | validation | V |  | killed | TestPlannerErrorCodes (internal/endpoint) | expand over limit accepted |
| 97 | expand-noshrink | endpoint/plan.go:918 | validation | D |  | killed | TestGoldenPlans (internal/endpoint) | expand to a smaller size plans a shrink |
| 98 | floor-check | endpoint/export.go:141 | nfs-validation | E |  | killed | TestNFS_N2_ClientsValidated (internal/csi) | checkFloor never refuses a broad network |
| 99 | client-slash0 | endpoint/export.go:133 | nfs-validation | E |  | killed | TestParseClient (internal/endpoint) | 0.0.0.0/0 accepted |
| 100 | client-masked | endpoint/export.go:123 | nfs-validation | E |  | killed | TestNFS_N2_ClientsValidated (internal/csi) | unmasked network accepted |
| 101 | client-canon | endpoint/export.go:120 | nfs-validation | E |  | survived |  | non-canonical prefix accepted |
| 102 | client-v4 | endpoint/export.go:120 | nfs-validation | E |  | killed | TestNFS_N2_ClientsValidated (internal/csi) | IPv6 prefix accepted |
| 103 | export-owned | endpoint/export.go:650 | ownership | S |  | killed | TestPlanPublishExport (internal/endpoint) | PublishFileExport on a dataset the endpoint does not own |
| 104 | export-foreign | endpoint/export.go:749 | nfs-exposure | E |  | killed | TestFileExportReceivedShareNFS (internal/endpoint) | publish over a foreign sharenfs |
| 105 | export-crossmnt | endpoint/export.go:755 | nfs-exposure | E |  | killed | TestPlanPublishExport (internal/endpoint) | publish under a crossmnt ancestor export |
| 106 | export-overlap | endpoint/export.go:764 | nfs-validation | E |  | killed | TestExportConverges (internal/endpoint) | publish overlapping client |
| 107 | export-exposure-plan | endpoint/export.go:783 | nfs-exposure | E |  | killed | TestPlanPublishExport (internal/endpoint) | publish while served beyond sharenfs |
| 108 | export-mounted | endpoint/export.go:745 | nfs-exposure | I |  | killed | TestFileExportPreconditions (internal/endpoint) | publish on unmountable volume |
| 109 | export-block | endpoint/export.go:653 | nfs-validation | V |  | survived |  | export ops on a zvol |
| 110 | id-slash | endpoint/endpoint.go:404 | containment | S |  | killed | TestGoldenPlans (internal/endpoint) | volume id with '/' or '@' accepted (path escape below another volume / snapshot) |
| 111 | id-len | endpoint/endpoint.go:404 | validation | V |  | killed | TestNeverIssuedIDs (internal/csi) | volume id length cap removed |
| 112 | snapid-shape | endpoint/endpoint.go:426 | validation | V |  | killed | TestGoldenPlans (internal/csi) | malformed snapshot id accepted |
| 113 | snapid-vol | endpoint/endpoint.go:429 | containment | S |  | survived |  | snapshot id with a nested volume part accepted |
| 114 | get-served | endpoint/endpoint.go:707 | ownership | S |  | survived |  | readVolume returns unowned/uncommitted datasets |
| 115 | getvol-served | endpoint/endpoint.go:909 | ownership | S |  | killed | TestLiteralDashMarkerIsAValue (internal/endpoint) | GetVolume returns unowned/uncommitted datasets |
| 116 | list-served | endpoint/endpoint.go:964 | ownership | S |  | killed | TestNFS_N6_NFSDDownNotCommitted (internal/csi) | ListVolumes lists every dataset |
| 117 | listsnap-owned | endpoint/endpoint.go:1013 | ownership | S |  | killed | TestSanity (internal/csi) | ListSnapshots lists foreign snapshots |
| 118 | verify-owned | endpoint/endpoint.go:632 | ownership | E |  | survived |  | verifyExport ignores owner change |
| 119 | verify-exposure | endpoint/endpoint.go:650 | nfs-exposure | E |  | survived |  | create commits while served beyond its export |
| 120 | pub-gate | endpoint/endpoint.go:1114 | peer-auth | S |  | killed | TestFileExportPreconditions (internal/endpoint) | PublishFileExport without root/file-export |
| 121 | pub-floor | endpoint/endpoint.go:1124 | nfs-validation | E |  | killed | TestAdv4_ClientPolicyGaps (internal/endpoint) | PublishFileExport client broader than floor |
| 122 | pub-uncommitted | endpoint/endpoint.go:1136 | ownership | E |  | killed | TestReachUncommittedServed (internal/endpoint) | publish an in-flight provision |
| 123 | pub-exposure | endpoint/endpoint.go:1176 | nfs-exposure | E |  | killed | TestFileExportServedBeyondRequest (internal/endpoint) | publish succeeds while served beyond |
| 124 | unpub-covers | endpoint/endpoint.go:1270 | nfs-exposure | E |  | killed | TestFileExportUnpublishStillServed (internal/endpoint) | unpublish reports OK while still served |
| 125 | unpub-lookup | endpoint/endpoint.go:1266 | nfs-exposure | E |  | killed | TestFileExportVerifiesKernelTable (internal/endpoint) | unpublish reports OK while etab still lists the host |
| 126 | lock-create | endpoint/endpoint.go:550 | lock | D |  | killed | TestConcurrentCreatesSerialize (internal/endpoint) | CreateVolume takes no lock |
| 127 | lock-create-srcvol | endpoint/endpoint.go:548 | lock | D |  | survived |  | clone-from-volume does not lock the source |
| 128 | lock-delete | endpoint/endpoint.go:751 | lock | D |  | killed | TestDeleteCloneWaitsForBusySource (internal/endpoint) | DeleteVolume takes no lock |
| 129 | lock-delete-extra | endpoint/endpoint.go:771 | lock | D |  | killed | TestDeleteCloneWaitsForBusySource (internal/endpoint) | DeleteVolume does not lock the released origin's source |
| 130 | lock-expand | endpoint/endpoint.go:844 | lock | I |  | killed | TestReconcileLocksTargets (internal/endpoint) | ExpandVolume takes no lock |
| 131 | lock-snap | endpoint/endpoint.go:974 | lock | I |  | killed | TestDeleteCloneLocksItsSource (internal/endpoint) | CreateSnapshot takes no lock |
| 132 | lock-snapdel | endpoint/endpoint.go:1033 | lock | D |  | survived |  | DeleteSnapshot takes no lock |
| 133 | lock-publish | endpoint/endpoint.go:1127 | lock | E |  | survived |  | PublishFileExport takes no lock |
| 134 | cfg-parent | endpoint/endpoint.go:221 | validation | V |  | survived |  | invalid parent accepted |
| 135 | cfg-minprefix | endpoint/endpoint.go:260 | nfs-validation | E |  | killed | TestFileExportPreconditions (internal/endpoint) | nfs-min-prefix out of range accepted |
| 136 | cfg-servers-root | endpoint/endpoint.go:263 | validation | V |  | killed | TestFileExportCreateNeedsServers (internal/endpoint) | NFS servers without file export |
| 137 | cfg-server-valid | endpoint/endpoint.go:298 | validation | V |  | survived |  | NFS server name length unchecked |
| 138 | scan-parent-prefix | endpoint/table.go:492 | containment | D | yes | survived |  | scan keeps the parent itself / names outside it |
| 139 | scan-deeper | endpoint/table.go:496 | containment | D | yes | killed | TestAdv4ChildDatasetsDestroyed (internal/endpoint) | datasets below a volume become volumes |
| 140 | scan-children | endpoint/table.go:497 | containment | D | yes | killed | TestAdv4ChildDatasetsDestroyed (internal/endpoint) | children below a volume are not recorded (OWN-12) |
| 141 | scan-only-parent | endpoint/table.go:477 | containment | D | yes | killed | TestMissingParentIsUnavailableNotAbsent (internal/endpoint) | missing parent (exported pool) read as volume absent |
| 142 | scandel-src | endpoint/endpoint.go:813 | containment | D | yes | survived |  | scanDelete follows nested names |
| 143 | cover | endpoint/table.go:225 | containment | D |  | killed | TestPlannersRequireACoveringTable (internal/endpoint) | every table covers every volume |
| 144 | mustcover | endpoint/table.go:236 | containment | D |  | killed | TestPlannersRequireACoveringTable (internal/endpoint) | mustCover never refuses |
| 145 | headroom | endpoint/headroom.go:124 | validation | I |  | killed | TestMinFreeBytesGuardLevelUpUnreadable (internal/endpoint) | headroom never refuses |
| 146 | capacity-floor | endpoint/endpoint.go:1062 | validation | I |  | killed | TestGetCapacityIsChildSpace (internal/endpoint) | GetCapacity ignores min free |
| 147 | root-range | endpoint/root.go:54 | validation | S |  | killed | TestRootPropsRoundTrip (internal/endpoint) | root uid/gid range unchecked |
| 148 | root-max | endpoint/root.go:92 | validation | S |  | killed | TestFileExportCreateRootOwner (internal/endpoint) | nfs root uid/gid/mode max unchecked |
| 149 | setroot-clean | endpoint/root_linux.go:32 | containment | S |  | killed | TestLocalRoot (internal/endpoint) | SetRoot on unclean/relative mountpoint |
| 150 | setroot-source | endpoint/root_linux.go:67 | containment | S |  | killed | TestLocalRoot (internal/endpoint) | SetRoot chowns a mount that is not this dataset |
| 151 | setroot-dev | endpoint/root_linux.go:68 | containment | S |  | killed | TestLocalRoot (internal/endpoint) | SetRoot chowns a directory not on the mount's device |
| 152 | srv-allowed | server/roles.go:114 | peer-auth | S |  | killed | TestRolesPerListener (internal/server) | role check removed |
| 153 | srv-adminpeer | server/roles.go:117 | peer-auth | S |  | survived |  | admin uid check removed |
| 154 | srv-adminpeer-any | server/roles.go:128 | peer-auth | S |  | killed | TestAdminPeerIsRootOrTheDaemonsUser (internal/server) | adminPeer accepts every uid |
| 155 | srv-adminpeer-euid | server/roles.go:128 | peer-auth | S |  | killed | TestAdminPeerIsRootOrTheDaemonsUser (internal/server) | adminPeer drops the daemon's own uid (functional) |
| 156 | srv-norole | server/roles.go:111 | peer-auth | S |  | killed | TestRolesPerListener (internal/server) | connection without a role passes (Allowed(0) still refuses: likely equivalent) |
| 157 | srv-noauth | server/roles.go:110 | peer-auth | S |  | killed | TestRolesPerListener (internal/server) | unauthenticated connection passes role gate |
| 158 | srv-reconcile-csi | server/roles.go:56 | peer-auth | S |  | killed | TestEveryMethodHasARole (internal/server) | CSI socket may call Reconcile |
| 159 | srv-publish-csi | server/roles.go:57 | peer-auth | S |  | killed | TestRolesPerListener (internal/server) | CSI socket may PublishFileExport |
| 160 | srv-unpublish-csi | server/roles.go:58 | peer-auth | S |  | killed | TestEveryMethodHasARole (internal/server) | CSI socket may UnpublishFileExport |
| 161 | srv-stream | server/roles.go:134 | peer-auth | S |  | survived |  | streams pass without a role |
| 162 | srv-health-prefix | server/roles.go:99 | peer-auth | S |  | killed | TestRolesPerListener (internal/server) | every method treated as health (no auth) |
| 163 | srv-peercred | server/peercred.go:42 | peer-auth | S |  | killed | TestPeerCredentialsAreLogged (internal/server) | peer credentials never recorded (adminPeer fails open) |
| 164 | srv-minage-overflow | server/server.go:247 | validation | D |  | killed | TestReconcile (internal/server) | min_age overflow wraps to a small/negative age |
| 165 | srv-maxentries-neg | server/server.go:108 | validation | V |  | killed | TestRoundTripAndCodes (internal/server) | negative max_entries accepted |
| 166 | srv-reqid-len | server/daemon.go:297 | validation | V |  | survived |  | request id length unchecked (log injection) |
| 167 | srv-reqid-chars | server/daemon.go:301 | validation | V |  | killed | TestRequestIDReachesTheAuditLog (internal/server) | request id charset unchecked (log injection) |
| 168 | srv-draining | server/daemon.go:173 | lock | I |  | killed | TestShutdownWaitsForCancelledHandlers (internal/server) | calls accepted while draining |
| 169 | csi-nfs-clients-req | csi/controller.go:292 | nfs-validation | E |  | killed | TestNFS_N15_ClientsRequired (internal/csi) | NFS mode without nfs.clients |
| 170 | csi-nfs-keys-mode | csi/controller.go:306 | nfs-validation | V |  | killed | TestNFS_OffWithoutServers (internal/csi) | nfs.* keys outside NFS mode |
| 171 | csi-unknown-param | csi/controller.go:333 | validation | V |  | killed | TestParametersAndAccessChecks (internal/csi) | unknown StorageClass parameter silently dropped |
| 172 | csi-rootparam-range | csi/controller.go:279 | validation | S |  | survived |  | nfs.uid/gid/mode range unchecked |
| 173 | csi-noparams | csi/controller.go:347 | validation | V |  | survived |  | snapshot parameters accepted |
| 174 | csi-cap-neg | csi/controller.go:364 | validation | V |  | survived |  | negative capacity accepted |
| 175 | csi-cap-order | csi/controller.go:368 | validation | V |  | survived |  | required > limit accepted |
| 176 | csi-context | csi/controller.go:421 | idempotency | I |  | killed | TestNFS_N10_Validate (internal/csi) | ValidateVolumeCapabilities confirms any volume_context |
| 177 | csi-vvc-server | csi/controller.go:533 | idempotency | I |  | killed | TestNFS_N10_Validate (internal/csi) | VVC confirms volume on unadvertised server |
| 178 | csi-vvc-cap | csi/controller.go:521 | validation | V |  | killed | TestNFS_N10_Validate (internal/csi) | VVC confirms unsupported capability |
| 179 | csi-kind-mode | csi/controller.go:186 | validation | V |  | killed | TestNFS_OffWithoutServers (internal/csi) | CreateVolume unsupported access mode |
| 180 | csi-cap-mode | csi/controller.go:223 | validation | V |  | killed | TestParametersAndAccessChecks (internal/csi) | VVC unsupported access mode |
| 181 | csi-kind-block | csi/controller.go:195 | validation | V |  | killed | TestBlockIsOptIn (internal/csi) | CreateVolume block without -enable-block |
| 182 | csi-cap-block | csi/controller.go:231 | validation | V |  | killed | TestBlockIsOptIn (internal/csi) | VVC block without -enable-block |
| 183 | csi-cap-ro | csi/controller.go:252 | nfs-exposure | I |  | killed | TestNFS_N10_Validate (internal/csi) | VVC confirms rw on a read-only export |
| 184 | csi-fstype | csi/controller.go:200 | validation | V |  | killed | TestNFS_OffWithoutServers (internal/csi) | wrong fsType accepted |
| 185 | csi-kind-mixed | csi/controller.go:207 | validation | V |  | survived |  | mixed block/mount capabilities accepted |
| 186 | csi-name-req | csi/controller.go:441 | validation | V |  | survived |  | CreateVolume without name |
| 187 | csi-del-id-req | csi/controller.go:496 | validation | V |  | survived |  | DeleteVolume without id |
| 188 | csi-expand-block | csi/controller.go:605 | validation | V |  | killed | TestExpandValidatesCapability (internal/csi) | expand with block access on a filesystem |
| 189 | csi-snap-noparams | csi/controller.go:633 | validation | V |  | survived |  | CreateSnapshot params unchecked |
