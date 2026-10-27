#!/usr/bin/env python3
# Aggregate a ZFS_TEST_CALLS tsv into the calls the real endpoint leg makes.
import sys, collections, statistics
tsv, skips = sys.argv[1], set(open(sys.argv[2]).read().split())
rows = [l.rstrip("\n").split("\t") for l in open(tsv)]
# crash: per case, uninterrupted + mean of crash points (the PR leg runs one)
crash = collections.defaultdict(lambda: collections.Counter())
keep = []
for r in rows:
    test = r[0]
    parts = test.split("/")
    top = parts[0]
    if top in skips:
        continue
    if top == "TestCrashAtEveryMutationConverges":
        crash[parts[1]][parts[2]] += 1
        continue
    if top == "TestDestroyNeverHitsAReplacedDataset" and len(parts) > 2 and parts[2] != "destroy0":
        continue
    keep.append(r)
bytest = collections.Counter(r[0].split("/")[0] for r in keep)
cls = collections.Counter(r[1] for r in keep)
helper = collections.Counter((r[2].split(".", 1)[-1], r[3]) for r in keep if r[1] == "harness")
ctot = 0
for case, c in crash.items():
    pts = [v for k, v in c.items() if k != "uninterrupted"]
    ctot += c["uninterrupted"] + (statistics.mean(pts) if pts else 0)
bytest["TestCrashAtEveryMutationConverges"] = round(ctot)
# crash per class, scaled
cr = [r for r in rows if r[0].startswith("TestCrashAtEveryMutationConverges/")]
allc = len(cr)
for k in ("harness", "endpoint"):
    n = sum(1 for r in cr if r[1] == k)
    cls[k] += round(ctot * n / allc) if allc else 0
for r in cr:
    if r[1] == "harness":
        helper[(r[2].split(".", 1)[-1], r[3])] += ctot / allc
total = sum(bytest.values())
print(f"total {total}  harness {cls['harness']}  endpoint {cls['endpoint']}")
print("-- top tests")
for t, n in bytest.most_common(int(sys.argv[3]) if len(sys.argv) > 3 else 10):
    print(f"{n:6d} {t}")
print("-- top harness helpers (crash test scaled to the PR sample)")
for (f, s), n in helper.most_common(15):
    print(f"{n:6.0f} {f}  [{s}]")
