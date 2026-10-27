import sys

path = sys.argv[1]
lines = open(path).read().splitlines()
note = ("# note: ADR 0010 (rollback of a failed fresh create): its sim-only and pure tests are set by hand to 0.00 "
        "(TestCreateRollbackAmbiguousFirstOp, TestCreateRollbackConcurrentSameName, TestCreateRollbackFailureIsJoined, "
        "TestCreateRollbackFreesTheName, TestCreateRollbackLeavesNothing, TestCreateRollbackReleasesCutSource, "
        "TestCreateNoRollbackOnResume, TestPlanRollbackCreate), TestCreateNoRollbackOnCallerDeadline to 0.10 (two 50 ms deadlines), "
        "and TestDestroyNeverHitsAReplacedDataset from 1.99 to 2.09 for its rollback-create case's destroy0 on a real pool "
        "(a create, a clone, a failed commit, a swap: ~0.4 test-s / nproc 4).")
new = {
    "TestCreateRollbackAmbiguousFirstOp": "0.00",
    "TestCreateRollbackConcurrentSameName": "0.00",
    "TestCreateRollbackFailureIsJoined": "0.00",
    "TestCreateRollbackFreesTheName": "0.00",
    "TestCreateRollbackLeavesNothing": "0.00",
    "TestCreateRollbackReleasesCutSource": "0.00",
    "TestCreateNoRollbackOnResume": "0.00",
    "TestPlanRollbackCreate": "0.00",
    "TestCreateNoRollbackOnCallerDeadline": "0.10",
    "TestDestroyNeverHitsAReplacedDataset": "2.09",
}
last_note = max(i for i, l in enumerate(lines) if l.startswith("# note:"))
head, data = [], []
for i, l in enumerate(lines):
    f = l.split()
    if not l.startswith("#") and len(f) == 2:
        data.append(f)
    else:
        head.append(l)
        if i == last_note:
            head.append(note)
names = {n for n, _ in data}
data = [[n, new.get(n, w)] for n, w in data]
data += [[n, w] for n, w in new.items() if n not in names]
data.sort(key=lambda f: (-float(f[1]), f[0]))
open(path, "w").write("\n".join(head + [n + " " + w for n, w in data]) + "\n")
