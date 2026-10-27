"""Pure judgment: read the tables, print the next actions in order. Changes nothing."""
import json, os
D = os.path.dirname(os.path.abspath(__file__))
def load(f):
    p = os.path.join(D, f)
    return [json.loads(l) for l in open(p) if l.strip()] if os.path.exists(p) else []
agents, queue, events = load("agents.jsonl"), load("queue.jsonl"), load("events.jsonl")
state = json.load(open(os.path.join(D, "state.json")))
ledger = {r["id"] for r in load("../lanes/ledger.jsonl")}
scored = lambda a: (a.get("ledger") or a["id"]) in ledger
first = lambda rows: sorted(rows, key=lambda r: (not r.get("anomaly"), r.get("priority", 99)))
finished = {a["id"] for a in agents if a["status"] in ("done", "merged")} | {a.get("queue") for a in agents if a["status"] in ("done", "merged")}
ready = lambda q: all(d in finished for d in q.get("deps", []))
running = lambda lane: [a for a in agents if a["status"] == "running" and a["lane"] == lane]
out = []
if state.get("ci") == "red": out.append(("FIX", f"PR CI red at {state.get('head')}"))
for a in first([a for a in agents if a["status"] == "reported"]): out.append(("TRIAGE", f"{a['id']} {a['name']} -> {a.get('report')}"))
for a in first([a for a in agents if a["status"] == "triaged" and a["lane"] == "writer"]): out.append(("MERGE", f"{a['id']} {a['name']} branch {a.get('branch')}"))
for a in agents:
    if a["status"] in ("done", "merged", "rejected") and not scored(a): out.append(("SCORE", f"{a['id']} {a['name']}"))
blocked = state.get("ci") == "red"
rq = first([q for q in queue if q["lane"] == "reader" and ready(q)])
nr = len(running("reader"))
caps = state.get("caps", {"readers": 5, "writers": 3})
rslots = min(3, max(0, caps["readers"] - nr))
if rslots and rq and not blocked: out.append(("LAUNCH-READERS", ", ".join(q["id"] for q in rq[:rslots])))
elif rq: out.append(("WAIT-READERS", f"{nr} readers running (cap {caps['readers']}); next: {', '.join(q['id'] for q in rq[:3])}"))
wq = first([q for q in queue if q["lane"] == "writer" and ready(q)])
nw = len(running("writer")); slots = max(0, state.get("caps", {}).get("writers", 3) - nw)
if slots and wq and not blocked: out.append(("LAUNCH-WRITERS", ", ".join(q["id"] for q in wq[:slots])))
elif wq: out.append(("WAIT-WRITERS", f"{nw} writers running (max 3); next: {wq[0]['id']}"))
# owner rule: never ask the owner an unresearched question; research and an
# adversarial pass come first, then the question carries the recommendation
for q in state.get("questions", []):
    q = q if isinstance(q, dict) else {"text": q}
    if q.get("researched") and q.get("attacked"):
        # research settles most questions (owner: "eliminates 88%+"): decide
        # and record when the evidence is decisive and the choice reversible;
        # ask only what stays contested, or is irreversible or outward-facing
        if q.get("decisive") and not q.get("irreversible"): out.append(("DECIDE+RECORD", f"{q['text']} -> {q.get('recommend')} (ADR; evidence {q.get('report')})"))
        else: out.append(("ASK", f"{q['text']} (recommend: {q.get('recommend')}; evidence: {q.get('report')})"))
    else: out.append(("RESEARCH-FIRST", q["text"]))
last_cp = max([i for i, e in enumerate(events) if e["to"] == "checkpoint"], default=-1)
if len(events) - 1 > last_cp: out.append(("CHECKPOINT", f"{len(events) - 1 - last_cp} transitions since the last one"))
print(f"readers {nr} running, writers {nw} running (caps {state.get('caps')}), CI {state.get('ci')}")
for k, v in out: print(f"{k:15} {v}")
