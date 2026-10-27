#!/usr/bin/env python3
"""Mutants whose before-kill set lies wholly in changed/removed tests (an
empty kill set too: a panic or package-level failure). Only these can change
status after the test changes; every other killed mutant keeps a killer that
was not touched."""
import json, glob, sys
CHANGED = set("""TestPlanCreateMalformedSourceIsNotFound TestPlanCreateIsEmptyWhenSatisfied
TestPlanCreateConvergesUncommittedClone TestPlanCreateRefusesForeignAndIncompatible TestPlanCreateHonorsLimitOnly
TestPlanCreateThickZvolLeavesReservationToZFS TestPlanCreateFilesystemsAreNotShared TestPlanDeleteVolume TestPlanExpand
TestPlanSnapshotReservedPrefix TestPlanReconcileLocksEveryTarget TestDeleteVolumeAfterItsCloneIsGone
TestThickCloneRetryIsIdempotent TestPlanReconcile TestConcurrentCreatesSerialize
TestAdv4CommitRaceDestroysJustProvisionedVolume TestSimulateDestroyAllEquivalent FuzzSimulateDestroyAllEquivalent
TestSnapshotLifecycleWithClones TestExpand TestThickCloneRetry TestMessageSizes TestStartupWaitsForTheParent
TestEndpointTargetIsAnAbsoluteSocketPath TestGoldenPlans TestShutdownWaitsForCancelledHandlers
TestWaitersAreServedInOrder TestNoStarvation TestTryAcquire TestCancelledWaitersLeaveNothingBehind
TestLimitBoundsConcurrentCommands TestLimitWaiterRespectsContext TestKilledCommandIsInterrupted""".split())
rows = {}
for f in glob.glob(sys.argv[1] + ".w*.jsonl"):
    for l in open(f):
        r = json.loads(l)
        rows[r["id"]] = r
ids = [i for i, r in rows.items() if r["status"] == "KILLED" and set(r["fails"]) <= CHANGED]
print(len(rows), "rows,", len(ids), "targeted", file=sys.stderr)
print("\n".join(map(str, sorted(ids))))
