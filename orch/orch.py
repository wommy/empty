"""Transition an agent or queue item; the only writer of events.jsonl.
usage: orch.py set <id> <status> [note]   |  orch.py launch <queue id> <agent id>  |  orch.py checkpointed"""
import json, sys, time, os
D = os.path.dirname(os.path.abspath(__file__))
EDGES = {"queued": {"running"}, "running": {"reported"}, "reported": {"triaged"},
         "triaged": {"done", "merged", "rejected"}}
def load(f): return [json.loads(l) for l in open(os.path.join(D, f)) if l.strip()] if os.path.exists(os.path.join(D, f)) else []
def save(f, rows): open(os.path.join(D, f), "w").write("".join(json.dumps(r) + "\n" for r in rows))
def event(i, a, b, note=""):
    open(os.path.join(D, "events.jsonl"), "a").write(json.dumps({"ts": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "id": i, "from": a, "to": b, "note": note}) + "\n")
cmd = sys.argv[1]
if cmd == "set":
    i, to, note = sys.argv[2], sys.argv[3], " ".join(sys.argv[4:])
    rows = load("agents.jsonl"); r = next(r for r in rows if r["id"] == i)
    ok = to == "abandoned" or to in EDGES.get(r["status"], set())
    if to in ("done",) and r["lane"] != "reader" or to in ("merged", "rejected") and r["lane"] != "writer": ok = False
    if not ok: sys.exit(f"refused: {i} {r['status']} -> {to}")
    event(i, r["status"], to, note); r["status"] = to; save("agents.jsonl", rows)
elif cmd == "launch":
    q, aid = sys.argv[2], sys.argv[3]
    queue = load("queue.jsonl"); item = next(x for x in queue if x["id"] == q)
    rows = load("agents.jsonl")
    rows.append({"id": aid, "name": item["name"], "lane": item["lane"], "wave": item.get("wave"), "status": "running",
                 "branch": item.get("branch"), "report": item.get("report"), "anomaly": item.get("anomaly", False), "queue": q})
    save("agents.jsonl", rows); save("queue.jsonl", [x for x in queue if x["id"] != q]); event(aid, "queued", "running", q)
elif cmd == "checkpointed":
    event("-", "-", "checkpoint")
