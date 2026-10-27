import json, sys
recs = [json.loads(l) for l in open(sys.argv[1] if len(sys.argv) > 1 else "ledger.jsonl") if l.strip()]
def rv(r):
    o = r["confirmed"] - r["bugs"] - r["anomalies"] - r["adopted"]
    return 5*r["bugs"] + 3*r["anomalies"] + 2*r["adopted"] + max(o, 0)
def wv(r):
    return (5*r["bugs_fixed"] + r["rows_newly_killed"]/5 + (r["seconds_saved"] or 0)/10 + r.get("runner_seconds_saved", 0)/60
            + r["tests_added"]/5 - 4*r["defects"] - r["conflicts"] - 3*r["red_ci"])
for lane, val in (("reader", rv), ("writer", wv)):
    rows = [r for r in recs if r["lane"] == lane]
    print(f"\n{lane.upper()}S ({len(rows)})")
    print(f"{'id':26} {'value':>6} {'/100k tok':>9} {'/hour':>7}  quality")
    tv = tt = tm = 0
    for r in rows:
        v = val(r); t = r.get("tokens"); m = r.get("minutes")
        q = (f"precision {r['confirmed']}/{r['checked']} stale {r['wrong']}" if lane == "reader"
             else f"defects {r['defects']} red_ci {r['red_ci']} conflicts {r['conflicts']}")
        print(f"{r['id']:26} {v:6.1f} {v/(t/1e5) if t else float('nan'):9.2f} {v/(m/60) if m else float('nan'):7.2f}  {q}")
        tv += v; tt += t or 0; tm += m or 0
    if tt: print(f"{'lane total':26} {tv:6.1f} {tv/(tt/1e5):9.2f} {tv/(tm/60) if tm else 0:7.2f}")
