"""Spec rows x tests present in a tree.
usage: spec_tests.py <tree> [deleted test names, comma separated]"""
import json, os, re, sys, collections
S = "/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad"
d = json.load(open(S + "/wave8/spec.json"))
tree = sys.argv[1]
deleted = set(sys.argv[2].split(",")) if len(sys.argv) > 2 and sys.argv[2] else set()
present = {}
for root, _, files in os.walk(tree):
    if "/.git" in root or "/gen/" in root:
        continue
    for f in files:
        if f.endswith("_test.go"):
            for m in re.finditer(r"^func ((?:Test|Fuzz)\w+)\(", open(os.path.join(root, f)).read(), re.M):
                present[m.group(1)] = os.path.relpath(os.path.join(root, f), tree)
rows = d["rows"]
missing = collections.Counter()
lost = []
for r in rows:
    have = [t for t in r["tests"] if t in present and t not in deleted]
    for t in r["tests"]:
        if t not in present:
            missing[t] += 1
    if r["tests"] and not have:
        lost.append((r["id"], r["status"], r["tests"]))
print("tests present:", len(present))
print("spec test names not present in tree:", dict(missing))
print("rows with tests listed but none present:", lost)
print("rows with no test listed:", sum(1 for r in rows if not r["tests"]))
