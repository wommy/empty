#!/usr/bin/env python3
"""Re-run wave7's tested mutants (9f1790d) with every test collected, TestModel included.
usage: run.py <worker> <nworkers>"""
import json, subprocess, sys, os, re, time
S = os.path.dirname(os.path.abspath(__file__))
W, N = int(sys.argv[1]), int(sys.argv[2])
wd = os.path.join(S, f"w{W}")
muts = {m["id"]: m for m in json.load(open(os.path.join(S, "../../wave7/mutants.json")))}
done = [json.loads(l) for l in open(os.path.join(S, "../../wave7/results.jsonl"))]
PRI = {"table.go": 0, "errors.go": 1, "reconcile.go": 2, "zfs/errors.go": 3, "plan.go": 4, "props.go": 5}
ids = sorted((r["id"] for r in done if r["status"] != "BUILD_FAIL"), key=lambda i: (PRI.get(muts[i]["bucket"], 9), i))
out = open(os.path.join(S, f"res{W}.jsonl"), "a")
seen = set()
try:
    seen = {json.loads(l)["id"] for l in open(os.path.join(S, f"res{W}.jsonl"))}
except Exception: pass
env = dict(os.environ, GOTOOLCHAIN="go1.27.3", MODEL_SEQS="300", MODEL_SHRINK="0")
for i, mid in enumerate(ids):
    if i % N != W or mid in seen: continue
    m = muts[mid]
    p = os.path.join(wd, m["file"])
    orig = subprocess.run(["git", "-C", wd, "show", "HEAD:" + m["file"]], capture_output=True, text=True).stdout
    b = orig.encode()
    mutated = b[:m["start"]] + m["repl"].encode() + b[m["end"]:]
    open(p, "wb").write(mutated)
    t = time.time()
    r = subprocess.run(["go", "test", "-count=1", "-v", "./internal/endpoint/", "./internal/csi/", "./internal/zfs/"],
                       cwd=wd, env=env, capture_output=True, text=True, timeout=900)
    fails = sorted(set(re.findall(r"^\s*--- FAIL: (\w+)", r.stdout, re.M)))
    build = "build failed" in r.stdout or "[setup failed]" in r.stdout or "cannot use" in r.stdout
    out.write(json.dumps({"id": mid, "fails": fails, "build_fail": build and not fails, "secs": round(time.time()-t, 1)}) + "\n"); out.flush()
    open(p, "w").write(orig)
