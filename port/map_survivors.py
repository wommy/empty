#!/usr/bin/env python3
"""Map wave7's 71 survivors (old copy) onto mutants enumerated from the
port branch. usage: map_survivors.py <wave7 dir> <port dir>
Writes <port>/survivors_mapped.json: one entry per old survivor with the new
mutant (or null when the code it mutated no longer exists)."""
import json, sys
W, P = sys.argv[1], sys.argv[2]
old = {m["id"]: m for m in json.load(open(f"{W}/mutants.json"))}
surv = [r for r in map(json.loads, open(f"{W}/results.jsonl")) if r["status"] == "SURVIVED"]
new = json.load(open(f"{P}/mutants.json"))

def line_text(root, f, line):
    return open(f"{root}/{f}").read().splitlines()[line - 1].strip()

out = []
for s in surv:
    m = old[s["id"]]
    src = open(f"{W}/mut-copy/{m['file']}", "rb").read()
    snippet = src[m["start"]:m["end"]].decode()
    ltext = line_text(f"{W}/mut-copy", m["file"], m["line"])
    cands = []
    for n in new:
        if n["file"] != m["file"] or n["op"] != m["op"] or n["desc"] != m["desc"] or n["repl"] != m["repl"]:
            continue
        nsrc = open(f"{P}/base/{n['file']}", "rb").read()
        if nsrc[n["start"]:n["end"]].decode() != snippet:
            continue
        same_line = line_text(f"{P}/base", n["file"], n["line"]) == ltext
        cands.append((not same_line, abs(n["line"] - m["line"]), n))
    cands.sort(key=lambda c: (c[0], c[1]))
    best = cands[0][2] if cands else None
    exact = bool(cands) and not cands[0][0]
    out.append(dict(old_id=m["id"], old_line=m["line"], file=m["file"], op=m["op"], desc=m["desc"],
                    new=best, exact_line=exact, old_line_text=ltext))
json.dump(out, open(f"{P}/survivors_mapped.json", "w"), indent=1)
for o in out:
    n = o["new"]
    print(o["old_id"], o["file"], o["old_line"], o["op"], "->", (n["id"], n["line"]) if n else None, "" if o["exact_line"] else "APPROX/GONE", "|", o["old_line_text"][:90])
