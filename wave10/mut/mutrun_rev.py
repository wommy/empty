#!/usr/bin/env python3
"""Like mutrun.py, but walks the mutant list from the end and skips any id
already recorded in any <prefix>.*.jsonl (re-read before each mutant), so
extra workers can join a running set.

usage: mutrun_rev.py <tree dir> <out.jsonl> <prefix> <worker> <nworkers>
"""
import glob, json, os, re, subprocess, sys, time

D = os.path.dirname(os.path.abspath(__file__))
tree, outp, prefix, W, N = sys.argv[1], sys.argv[2], sys.argv[3], int(sys.argv[4]), int(sys.argv[5])
muts = json.load(open(os.path.join(D, "mutants-head.json")))
ref = os.path.join(D, "ref")
env = dict(os.environ, GOTOOLCHAIN="go1.27.3")


def seen():
    s = set()
    for f in glob.glob(os.path.join(D, prefix + ".*.jsonl")):
        for l in open(f):
            try:
                r = json.loads(l)
            except Exception:
                continue
            if r.get("status") != "MISMATCH":
                s.add(r["id"])
    return s


out = open(outp, "a")
for k, m in reversed(list(enumerate(muts))):
    if k % N != W or m["id"] in seen():
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
