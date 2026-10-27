#!/usr/bin/env python3
"""Run sampled mutants. usage: run_mutants.py <mutants.json> <results.jsonl> [workers]
Each worker owns a copy of mut-copy; applies one byte-range replacement,
runs go test ./internal/..., restores. Build failures are replaced by the next
candidate of the same bucket until the bucket quota is met."""
import json, os, random, shutil, subprocess, sys, threading, time, collections
W = os.path.dirname(os.path.abspath(__file__))
SRC = f"{W}/mut-copy"
muts = json.load(open(sys.argv[1])); out = sys.argv[2]
nw = int(sys.argv[3]) if len(sys.argv) > 3 else 4
QUOTA = {"plan.go": 90, "props.go": 40, "reconcile.go": 50, "table.go": 40, "zfs/errors.go": 40, "errors.go": 27, "zfs/zfs.go:parseColumns": 16}
rnd = random.Random(7)
pools = collections.defaultdict(list)
for m in muts: pools[m["bucket"]].append(m)
for b in pools: rnd.shuffle(pools[b])
lock = threading.Lock()
valid = collections.Counter(); inflight = collections.Counter()
done_ids = set()
if os.path.exists(out):
    for l in open(out):
        r = json.loads(l); done_ids.add(r["id"])
        if r["status"] != "BUILD_FAIL": valid[r["bucket"]] += 1
env = dict(os.environ, GOTOOLCHAIN="go1.27.3")
def next_mut():
    with lock:
        for b, q in QUOTA.items():
            if valid[b] + inflight[b] < q:
                while pools[b]:
                    m = pools[b].pop()
                    if m["id"] in done_ids: continue
                    inflight[b] += 1; return m
    return None
def worker(k):
    d = f"{W}/work/w{k}"
    if not os.path.exists(d): shutil.copytree(SRC, d)
    while (m := next_mut()):
        p = f"{d}/{m['file']}"; orig = open(f"{SRC}/{m['file']}", "rb").read()
        mutated = orig[:m["start"]] + m["repl"].encode() + orig[m["end"]:]
        open(p, "wb").write(mutated); t0 = time.time()
        try:
            r = subprocess.run(["go", "test", "./internal/...", "-count=1", "-failfast"], cwd=d, env=env,
                               capture_output=True, text=True, timeout=150)
            o = r.stdout + r.stderr
            if r.returncode == 0: st, info = "SURVIVED", ""
            elif "[build failed]" in o or "\n# " in o and "--- FAIL" not in o and "FAIL\t" not in o.replace("[build failed]", ""): st, info = "BUILD_FAIL", o[:200]
            else:
                fails = [l.strip() for l in o.splitlines() if l.startswith("--- FAIL") or l.strip().startswith("--- FAIL")]
                st, info = "KILLED", (fails[0] if fails else o[:200])
        except subprocess.TimeoutExpired:
            st, info = "KILLED", "TIMEOUT"
        open(p, "wb").write(orig)
        rec = dict(m, status=st, info=info, secs=round(time.time() - t0, 1))
        with lock:
            inflight[m["bucket"]] -= 1
            if st != "BUILD_FAIL": valid[m["bucket"]] += 1
            open(out, "a").write(json.dumps(rec) + "\n")
ts = [threading.Thread(target=worker, args=(k,)) for k in range(nw)]
[t.start() for t in ts]; [t.join() for t in ts]
print("done", dict(valid))
