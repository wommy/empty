#!/usr/bin/env python3
"""Ids to re-run: exact successors of wave7's survivors, plus every mutant on
the HEAD lines that replaced the code of the ones whose code is gone.
usage: survivor_ids.py <port dir>  (prints comma-separated ids; writes successor notes)"""
import json, sys
P = sys.argv[1]
mapped = json.load(open(f"{P}/survivors_mapped.json"))
new = json.load(open(f"{P}/mutants.json"))
# gone survivor -> (file, HEAD lines that now hold its invariant)
SUCC = {
    187: ("internal/endpoint/plan.go", [252, 276]),   # clone source must be served (OwnedVolume && Committed)
    123: ("internal/endpoint/plan.go", [196]),        # create: existing row must be this volume
    127: ("internal/endpoint/plan.go", [196]),
    624: ("internal/endpoint/table.go", [79]),        # OwnedVolume name marker
    568: ("internal/endpoint/reconcile.go", [393]),   # tooYoung boundary
    579: ("internal/endpoint/reconcile.go", [464]),   # simulateDestroy -> simulateDestroyAll reap predicate
    593: ("internal/endpoint/reconcile.go", [431, 449]),
    597: ("internal/endpoint/reconcile.go", [441]),
    602: ("internal/endpoint/reconcile.go", [457]),
}
ids, notes = [], {}
for o in mapped:
    if o["exact_line"] and o["new"]:
        ids.append(o["new"]["id"]); notes[o["new"]["id"]] = f"exact successor of wave7 #{o['old_id']}"
    elif o["old_id"] in SUCC:
        f, lines = SUCC[o["old_id"]]
        for n in new:
            if n["file"] == f and n["line"] in lines and n["id"] not in notes:
                ids.append(n["id"]); notes[n["id"]] = f"HEAD-line successor of wave7 #{o['old_id']}"
    else:
        print("unmapped", o["old_id"], file=sys.stderr)
json.dump(notes, open(f"{P}/survivor_notes.json", "w"), indent=1)
print(",".join(map(str, ids)))
print(len(ids), file=sys.stderr)
