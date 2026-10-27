#!/usr/bin/env python3
"""Full kill sets for wave10/mut/mutants-head.json on one tree.

usage: mutrun.py <tree dir> <out.jsonl> <worker> <nworkers>

<tree dir> is an exported copy of the repo (git archive). The mutant's
original text is checked before it is applied, so a tree whose production
code differs from HEAD fails loudly (status MISMATCH) instead of testing a
different mutant. Every failing top-level test is recorded (no -failfast).
"""
import json, os, re, subprocess, sys, time

D = os.path.dirname(os.path.abspath(__file__))
tree, outp, W, N = sys.argv[1], sys.argv[2], int(sys.argv[3]), int(sys.argv[4])
muts = json.load(open(os.path.join(D, "mutants-head.json")))
ref = os.path.join(D, "ref")  # pristine HEAD export: never mutated
if os.environ.get("MUT_IDS"):  # optional: only these ids
    only = {int(x) for x in open(os.environ["MUT_IDS"]).read().split()}
    muts = [m for m in muts if m["id"] in only]
seen = set()
if os.path.exists(outp):
    seen = {json.loads(l)["id"] for l in open(outp) if json.loads(l)["status"] not in ("MISMATCH",)}
out = open(outp, "a")
env = dict(os.environ, GOTOOLCHAIN="go1.27.3")

for k, m in enumerate(muts):
    if k % N != W or m["id"] in seen:
        continue
    path = os.path.join(tree, m["file"])
    orig = open(path, "rb").read()
    want = open(os.path.join(ref, m["file"]), "rb").read()[m["start"]:m["end"]]
    if orig[m["start"]:m["end"]] != want:
        out.write(json.dumps({"id": m["id"], "status": "MISMATCH"}) + "\n"); out.flush()
        continue
    open(path, "wb").write(orig[:m["start"]] + m["repl"].encode() + orig[m["end"]:])
    t = time.time()
    try:
        r = subprocess.run(["go", "test", "-count=1", "-v", "-timeout=240s", "./..."], cwd=tree, env=env,
                           capture_output=True, text=True, timeout=400)
        so = r.stdout + r.stderr
        fails = sorted(set(re.findall(r"^--- FAIL: (\w+)", so, re.M)))
        pkgfail = sorted(set(re.findall(r"^FAIL\s+(\S+)", so, re.M)))
        build = bool(re.search(r"\[build failed\]|\[setup failed\]", so))
        if build and not fails:
            status = "BUILD_FAIL"
        elif fails or r.returncode != 0:
            status = "KILLED"
        else:
            status = "SURVIVED"
    except subprocess.TimeoutExpired:
        fails, pkgfail, status = [], [], "TIMEOUT"
    finally:
        open(path, "wb").write(orig)
    out.write(json.dumps({"id": m["id"], "file": m["file"], "line": m["line"], "op": m["op"], "status": status,
                          "fails": fails, "pkgfail": pkgfail, "secs": round(time.time() - t, 1)}) + "\n")
    out.flush()
