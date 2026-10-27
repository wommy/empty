#!/usr/bin/env python3
"""Re-run every SURVIVED mutant from results.jsonl against the copy's current
tests (incl. the new mutation_kill_test.go files). usage: rerun_survivors.py results.jsonl out.jsonl"""
import json, os, shutil, subprocess, sys, threading, queue
W = os.path.dirname(os.path.abspath(__file__)); SRC = f"{W}/mut-copy"
surv = [r for r in map(json.loads, open(sys.argv[1])) if r["status"] == "SURVIVED"]
q = queue.Queue(); [q.put(m) for m in surv]; lock = threading.Lock()
env = dict(os.environ, GOTOOLCHAIN="go1.27.3"); open(sys.argv[2], "w").close()
def worker(k):
    d = f"{W}/work/w{k}"
    for f in ("internal/endpoint/mutation_kill_test.go", "internal/zfs/mutation_kill_test.go"):
        shutil.copy(f"{SRC}/{f}", f"{d}/{f}")
    while True:
        try: m = q.get_nowait()
        except queue.Empty: return
        p = f"{d}/{m['file']}"; orig = open(f"{SRC}/{m['file']}", "rb").read()
        open(p, "wb").write(orig[:m["start"]] + m["repl"].encode() + orig[m["end"]:])
        try:
            r = subprocess.run(["go", "test", "./internal/...", "-count=1", "-failfast"], cwd=d, env=env, capture_output=True, text=True, timeout=150)
            o = r.stdout + r.stderr
            fails = [l.strip() for l in o.splitlines() if l.strip().startswith("--- FAIL")]
            st = "SURVIVED" if r.returncode == 0 else "KILLED"; info = fails[0] if fails else ""
        except subprocess.TimeoutExpired: st, info = "KILLED", "TIMEOUT"
        open(p, "wb").write(orig)
        with lock: open(sys.argv[2], "a").write(json.dumps(dict(id=m["id"], bucket=m["bucket"], file=m["file"], line=m["line"], op=m["op"], desc=m["desc"], status=st, info=info)) + "\n")
ts = [threading.Thread(target=worker, args=(k,)) for k in range(4)]; [t.start() for t in ts]; [t.join() for t in ts]
