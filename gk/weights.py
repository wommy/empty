import sys
p = sys.argv[1]
new = ["TestPlanReconcileOrphanCloneSourceHonoursLocks", "TestPlanReconcileZeroCreationIsNeverOld",
       "TestPublishFileExportLocksTheVolume", "TestSnapshotDeleteAndCloneTakeTheSourceLocks"]
note = ("# note: Wave 25's guard-kill tests (pure planner, or their own simulator, no real-pool work) are set by hand to 0.00 "
        "like the other sim-only tests: " + ", ".join(new) + ".")
lines = open(p).read().split("\n")
last_note = max(i for i, l in enumerate(lines) if l.startswith("# note:"))
lines.insert(last_note + 1, note)
for t in new:
    zero = [i for i, l in enumerate(lines) if l.endswith(" 0.00") and not l.startswith("#")]
    at = next((i for i in zero if lines[i].split()[0] > t), zero[-1] + 1)
    lines.insert(at, t + " 0.00")
open(p, "w").write("\n".join(lines))
