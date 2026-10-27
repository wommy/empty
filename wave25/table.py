#!/usr/bin/env python3
"""Merge results*.jsonl into mutation-raw.md (one row per mutant) and print counts."""
import json, glob, os, sys
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from mutants import MUTANTS
here = os.path.dirname(os.path.abspath(__file__))
res = {}
for f in sorted(glob.glob(os.path.join(here, "results*.jsonl"))):
    for l in open(f):
        r = json.loads(l); res[r["id"]] = r
order = [m["id"] for m in MUTANTS]
rows = ["| # | id | file:line | cat | risk | conc | status | killed by / note | mutation |", "|---|---|---|---|---|---|---|---|---|"]
cnt = {}
for i, id in enumerate(order, 1):
    r = res.get(id)
    if not r: continue
    cnt[r["status"]] = cnt.get(r["status"], 0) + 1
    if r.get("first", "").startswith("panic/timeout") and "running tests:" in r.get("tail", ""):
        t = r["tail"].split("running tests:")[1].split()[0].split("/")[0]
        r["first"] = t + " (hang: 5m test timeout)"
    st = r["status"] + (" (model)" if r.get("by") == "model" and r["status"] == "killed" else "")
    mut = r["note"].replace("|", "/")
    rows.append(f"| {i} | {id} | {r['file'].replace('internal/','')}:{r['line']} | {r['cat']} | {r['risk']} | {'yes' if r['conc'] else ''} | {st} | {r.get('first','').replace('|','/')} | {mut} |")
open(os.path.join(here, "mutation-raw.md"), "w").write("# Mutation sweep raw table\n\nSnapshot of commit 96c49ff. Mutant definitions (exact old/new text): mutants.py. Per-mutant JSON: results*.jsonl.\n\n" + "\n".join(rows) + "\n")
print(cnt, len(res))
