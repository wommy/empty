import collections,sys
D,R,K,Q='delete','replace','keep','rewrite'
# (verdict, replacement / reason)
C={
'TestApplyRefusesAnUnknownOpKind':(D,'sealed op.Op: no unknown kind is constructible'),
'TestOpKindString':(D,'kindNames table, exhaustive by array length'),
'TestScanRecordsCoverage':(D,'Scope is a field Parse sets; World has no other constructor'),
'TestPlannersRequireACoveringTable':(R,'Bind: ErrScope/ErrStale (TestBindNeedsTheScanLease)'),
'TestUserValueDash':(R,'world_test tamper/ownership rows'),
'TestLiteralDashMarkerIsAValue':(R,'TestOwnershipIsDerivedOnce row (VN "-" -> Foreign)'),
'TestDecodeRejectsImpossibleZvolSizes':(R,'TestParseRejectsWhatZfsNeverPrints rows I7'),
'TestReachCapacityWrap':(D,'req.Bytes: RoundUp cannot overflow'),
'TestReachPlannerZeroBlockSize':(D,'req.BlockSize zero value is 16384'),
'TestReachUncommittedServed':(R,'FSM: Uncommitted is a State; List port reads State'),
'TestReachOriginOutsideParent':(R,'ReleasableOrigin rule; regress row F6'),
'TestManagedWithoutNameIsNotOwned':(R,'TestOwnershipIsDerivedOnce row'),
'TestZfsErrCodes':(R,'ErrTable rows (single table, step 0)'),
'TestCodeOfNeverReportsAFailureAsOK':(R,'code.Refuse(OK) -> Internal; TestErrTableIsTotal'),
'TestBusyIsMappedByOperation':(R,'ErrTable[class][Busy]'),
'TestUnknownFailureAfterAMutationIsRetryable':(R,'ErrTable[*][Unknown][mutated]'),
'TestPlanCreateMalformedSourceIsNotFound':(R,'req.ParseCreateVolume row'),
'TestPlanCreateIsEmptyWhenSatisfied':(R,'TestCreateCells (Committed x create: no ops)'),
'TestPlanCreateConvergesUncommittedClone':(R,'TestCrashConvergence'),
'TestPlanCreateRefusesForeignAndIncompatible':(R,'TestCreateCells (Foreign/Invalid cells)'),
'TestPlanCreateHonorsLimitOnly':(R,'TestCreateCells variants'),
'TestPlanCreateThickZvolLeavesReservationToZFS':(R,'golden plan row'),
'TestPlanDeleteVolume':(R,'TestDeleteCells'),
'TestPlanCreateFilesystemsAreNotShared':(R,'golden plan row (sharenfs=off)'),
'TestCloneLargerThanLimitIsOutOfRange':(R,'TestCreateCells / regress'),
'TestReserveRestoreIsIdempotent':(R,'TestCreateCells idempotent repeat'),
'TestCloneFailureAfterCloneConvergesToSameState':(R,'TestCrashConvergence'),
'FuzzCreateConverges':(R,'TestCrashConvergence (exhaustive, not sampled)'),
'TestAdv4ChildDatasetsDestroyed':(R,'TestDeleteCells child-dataset'),
'TestDeleteVolumeSparesChildDatasets':(R,'TestDeleteCells child-dataset'),
'TestDeleteVolumeSparesSnapshotCutAfterScan':(R,'TestRacingSnapshotStopsTheDestroy (golden)'),
'TestDeleteCloneInterruptedThenRetriedReleasesOrigin':(R,'TestDeleteCells crash/retry + R1'),
'TestCreateVolumeIdempotent':(R,'TestCreateCells'),
'TestCreateVolumeResumesInterruptedProvision':(R,'TestCrashConvergence'),
'TestNeverAdoptsOrDeletesUnmanaged':(R,'TestCreateCells/TestDeleteCells Foreign rows'),
'TestDeleteVolume':(R,'TestDeleteCells'),
'TestCloneFromVolumeReleasesInternalSnapshot':(R,'TestGoldenPlans + TestDeleteCells leak check'),
'TestThickCloneRetryIsIdempotent':(R,'TestCreateCells'),
'TestCloneResizeFailureConverges':(R,'TestCrashConvergence'),
'TestInheritedOwnershipIsNotOwnership':(R,'TestOwnershipIsDerivedOnce'),
'TestDeleteVolumeSparesForeignSnapshots':(R,'TestDeleteCells sanoid rows'),
'TestDeleteVolumeAfterItsCloneIsGone':(R,'TestDeleteCells'),
'TestCloneSourcesMustBeOwned':(R,'TestCloneSourceIsOursOnlyByStamp + TestSnapshotOwnership'),
'TestCloneNeverAdoptsARacingDataset':(R,'TestGoldenPlans (clone TolNone) + ErrTable Exists'),
'TestVolumeNameMustMatchID':(R,'TestOwnershipIsDerivedOnce'),
'TestPropertyPrefixIsValidated':(R,'NewSchema row'),
'TestAdv4ReleasedOriginOutsideParent':(R,'regress F6'),
'TestCloneRefusesUnstampedReservedSnapshot':(R,'TestCloneSourceIsOursOnlyByStamp'),
'TestLegacyUnstampedOriginReleasedWithItsClone':(Q,'core never releases an unstamped origin (OWN-13): invert, or knownOpen with expiry'),
'FuzzParseColumns':(R,'goes with the -Hp parser (step 1); FuzzParse on the JSON decoder'),
'TestParseColumnsRejectsForgedLines':(R,'TestPhantomRowsCannotBeForged'),
}
rows=[l.split() for l in open('tests.txt')]
agg=collections.defaultdict(lambda:[0,0])
out=[]
for f,n,loc in rows:
    v,why=C.get(n,(K,''))
    agg[v][0]+=1; agg[v][1]+=int(loc)
    out.append((f,n,int(loc),v,why))
missing=set(C)-{r[1] for r in rows}
if missing: print('MISSING',missing,file=sys.stderr)
for v in (D,R,Q,K): print(v,agg[v])
print('total',sum(a[0] for a in agg.values()),sum(a[1] for a in agg.values()))
with open('classified.tsv','w') as fh:
    for r in out: fh.write('\t'.join(map(str,r))+'\n')
