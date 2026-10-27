#!/usr/bin/env python3
# Add the wave24 endpoint tests at 0.00 (own simulator, milliseconds each),
# keeping the file's order: weight descending, then name.
p = '/home/user/empty/.claude/worktrees/agent-a3b322c80ba25f7fc/ci/real-zfs-weights.txt'
new = """TestReconcileDefaultMaxDeletionsWithDigest TestDeleteVolumeSettleWindow
TestFileExportCreateServedBeyondByAncestor TestFileExportCreateChangedBeforeCommit
TestFileExportRefusedPublishRestoresLocalList TestExpandRollbackFailureIsReported
TestFileExportCreateRootFailureIsRetryable TestRootErrMapping TestReconcileCancelledMidRunStops
TestReconcileRescanFailureDestroysNothing TestDeleteVolumeLockPassesAreBounded
TestWholeScanSlotWaitEndsWithTheContext""".split()
lines = open(p).read().splitlines()
head = [l for l in lines if l.startswith('#') or not l.strip()]
rows = [l.split() for l in lines if l.strip() and not l.startswith('#')]
names = {r[0] for r in rows}
for n in new:
    assert n not in names, n
    rows.append([n, '0.00'])
rows.sort(key=lambda r: (-float(r[1]), r[0]))
note = (" Wave 24's sim-only endpoint tests (own simulator, no real-pool work) are set by hand to 0.00 like the "
        "other sim-only tests: TestDeleteVolumeLockPassesAreBounded, TestDeleteVolumeSettleWindow, "
        "TestExpandRollbackFailureIsReported, TestFileExportCreateChangedBeforeCommit, "
        "TestFileExportCreateRootFailureIsRetryable, TestFileExportCreateServedBeyondByAncestor, "
        "TestFileExportRefusedPublishRestoresLocalList, TestReconcileCancelledMidRunStops, "
        "TestReconcileDefaultMaxDeletionsWithDigest, TestReconcileRescanFailureDestroysNothing, "
        "TestRootErrMapping, TestWholeScanSlotWaitEndsWithTheContext.")
out = []
for l in lines:
    if l.startswith('# Source:'):
        l = l + note
    if l.startswith('#') or not l.strip():
        out.append(l)
# header is everything before the first data row
first = next(i for i, l in enumerate(lines) if l.strip() and not l.startswith('#'))
hdr = out[:first]
open(p, 'w').write('\n'.join(hdr + [' '.join(r) for r in rows]) + '\n')
