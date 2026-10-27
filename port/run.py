#!/usr/bin/env python3
"""Run mutants against the port branch copy (port/base).
usage: run.py <mutants.json> <out.jsonl> ids <id,id,...> [workers]
       run.py <mutants.json> <out.jsonl> sample [workers]   (wave7 quotas, seed 7)
Each worker owns a copy of base; applies one byte-range replacement, runs
go test ./internal/... -count=1 -failfast, restores. In sample mode a build
failure is replaced by the next candidate of its bucket (as wave7 did)."""
import collections, json, os, random, shutil, subprocess, sys, threading
P = os.path.dirname(os.path.abspath(__file__))
SRC = f"{P}/base"
muts = json.load(open(sys.argv[1])); out = sys.argv[2]; mode = sys.argv[3]
byid = {m["id"]: m for m in muts}
lock = threading.Lock(); env = dict(os.environ, GOTOOLCHAIN="go1.27.3")
done = set()
if os.path.exists(out):
    done = {json.loads(l)["id"] for l in open(out)}
if mode == "ids":
    queue = [byid[int(i)] for i in sys.argv[4].split(",") if int(i) not in done]
    nw = int(sys.argv[5]) if len(sys.argv) > 5 else 4
    QUOTA = None
else:
    nw = int(sys.argv[4]) if len(sys.argv) > 4 else 4
    QUOTA = {"plan.go": 90, "props.go": 40, "reconcile.go": 50, "table.go": 40, "zfs/errors.go": 40, "errors.go": 27, "zfs/zfs.go:parseColumns": 16}
    rnd = random.Random(7); pools = collections.defaultdict(list)
    for m in muts: pools[m["bucket"]].append(m)
    for b in pools: rnd.shuffle(pools[b])
    valid = collections.Counter(); inflight = collections.Counter()
    if os.path.exists(out):
        for l in open(out):
            r = json.loads(l)
            if r["status"] != "BUILD_FAIL": valid[r["bucket"]] += 1

def next_mut():
    with lock:
        if QUOTA is None:
            return queue.pop() if queue else None
        for b, q in QUOTA.items():
            if valid[b] + inflight[b] < q:
                while pools[b]:
                    m = pools[b].pop()
                    if m["id"] in done: continue
                    inflight[b] += 1; return m
        return None

def worker(k):
    d = f"{P}/work/w{k}"
    if os.path.exists(d): shutil.rmtree(d)
    shutil.copytree(SRC, d)
    while (m := next_mut()):
        p = f"{d}/{m['file']}"; orig = open(f"{SRC}/{m['file']}", "rb").read()
        open(p, "wb").write(orig[:m["start"]] + m["repl"].encode() + orig[m["end"]:])
        try:
            r = subprocess.run(["go", "test", "./internal/...", "-count=1", "-failfast"], cwd=d, env=env,
                               capture_output=True, text=True, timeout=150)
            o = r.stdout + r.stderr
            fails = [l.strip() for l in o.splitlines() if l.strip().startswith("--- FAIL")]
            if r.returncode == 0: st, info = "SURVIVED", ""
            elif not fails and ("[build failed]" in o or "[setup failed]" in o): st, info = "BUILD_FAIL", o[:200]
            else: st, info = "KILLED", (fails[0] if fails else o[:200])
        except subprocess.TimeoutExpired:
            st, info = "KILLED", "TIMEOUT"
        open(p, "wb").write(orig)
        with lock:
            if QUOTA is not None:
                inflight[m["bucket"]] -= 1
                if st != "BUILD_FAIL": valid[m["bucket"]] += 1
            open(out, "a").write(json.dumps(dict(id=m["id"], bucket=m["bucket"], file=m["file"], line=m["line"], op=m["op"],
                                                 desc=m["desc"], status=st, info=info)) + "\n")

ts = [threading.Thread(target=worker, args=(k,)) for k in range(nw)]
[t.start() for t in ts]; [t.join() for t in ts]
