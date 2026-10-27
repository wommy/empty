"""Per spec row: which tests in the after tree cover it (markdown table).
usage: map_rows.py <before tree> <after tree>"""
import json, os, re, sys
S = "/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad"
rows = json.load(open(S + "/wave8/spec.json"))["rows"]


def present(tree):
    p = set()
    for root, _, files in os.walk(tree):
        if "/.git" in root or "/gen/" in root:
            continue
        for f in files:
            if f.endswith("_test.go"):
                p |= set(re.findall(r"^func ((?:Test|Fuzz)\w+)\(", open(os.path.join(root, f)).read(), re.M))
    return p


before, after = present(sys.argv[1]), present(sys.argv[2])
# replacements for removed tests (proof in tests.md section 4)
REPL = {
    "TestPlanCreateMalformedSourceIsNotFound": "TestGoldenPlans/restore/malformed-source, clone/malformed-source",
    "TestPlanCreateIsEmptyWhenSatisfied": "TestGoldenPlans/create/satisfied",
    "TestPlanCreateConvergesUncommittedClone": "TestGoldenPlans/create/uncommitted-clone-converges",
    "TestPlanCreateRefusesForeignAndIncompatible": "TestGoldenPlans/create/foreign, create/incompatible-*",
    "TestPlanCreateHonorsLimitOnly": "TestGoldenPlans/create/fs-limit-only",
    "TestPlanCreateThickZvolLeavesReservationToZFS": "TestGoldenPlans/create/zvol-thick, create/thick-on-sparse-zvol",
    "TestPlanCreateFilesystemsAreNotShared": "TestGoldenPlans/create/fs",
    "TestPlanDeleteVolume": "TestGoldenPlans/delete/*",
    "TestPlanExpand": "TestGoldenPlans/expand/*",
    "TestPlanSnapshotReservedPrefix": "TestGoldenPlans/snapshot/reserved-prefix",
    "TestPlanReconcileLocksEveryTarget": "TestPlanReconcile (lock-cover loop)",
    "TestDeleteVolumeAfterItsCloneIsGone": "TestCloneFromVolumeReleasesInternalSnapshot",
    "TestThickCloneRetryIsIdempotent": "TestThickCloneRetry",
    "TestEndpointTargetIsAnAbsoluteSocketPath": "TestTargetIsAnAbsoluteSocketPath (unixsock)",
}
print("| row | status | covering tests now | change |")
print("|---|---|---|---|")
for r in rows:
    ts = r["tests"]
    now = [t for t in ts if t in after]
    gone = [t for t in ts if t in before and t not in after]
    absent = [t for t in ts if t not in before and t not in after]
    cov = ", ".join(now + [REPL.get(t, "?" + t) for t in gone])
    if absent:
        cov += (", " if cov else "") + "(not a Go test name: " + ", ".join(absent) + ")"
    note = ""
    if gone:
        note = "removed: " + ", ".join(gone) + ("; SOLE killer replaced" if not now else "")
    print(f"| {r['id']} | {r['status']} | {cov or '-'} | {note} |")
