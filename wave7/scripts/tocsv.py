import json, sys, csv, os
from datetime import datetime
def t(s): return datetime.strptime(s, "%Y-%m-%dT%H:%M:%SZ").timestamp() if s else None
raw, out = sys.argv[1], sys.argv[2]
runs = json.load(open(os.path.join(raw, "runs.json")))["workflow_runs"]
w = csv.writer(open(out, "w", newline=""))
w.writerow(["run_number","run_id","head_sha","run_conclusion","run_created","run_wall_s",
            "job","job_conclusion","job_queue_s","job_wall_s",
            "step_no","step","step_conclusion","step_start","step_s","step_offset_in_job_s"])
for r in sorted(runs, key=lambda r: r["run_number"]):
    p = os.path.join(raw, f"jobs-{r['id']}.json")
    if not os.path.exists(p): continue
    jobs = json.load(open(p))["jobs"]
    cr = t(r["created_at"])
    ends = [t(j["completed_at"]) for j in jobs if j.get("completed_at")]
    run_wall = int(max(ends) - cr) if ends and r["status"] == "completed" else ""
    for j in jobs:
        js, je = t(j.get("started_at")), t(j.get("completed_at"))
        for s in j["steps"]:
            ss, se = t(s.get("started_at")), t(s.get("completed_at"))
            w.writerow([r["run_number"], r["id"], r["head_sha"][:7], r["conclusion"] or r["status"], r["created_at"], run_wall,
                        j["name"], j["conclusion"] or j["status"], int(js-cr) if js else "", int(je-js) if je and js else "",
                        s["number"], s["name"], s["conclusion"] or s["status"], s.get("started_at") or "",
                        int(se-ss) if se and ss else "", int(ss-js) if ss and js else ""])
