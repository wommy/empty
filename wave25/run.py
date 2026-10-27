#!/usr/bin/env python3
"""Mutation sweep runner. Usage: run.py MUTROOT [id ...]
Applies each mutant in mutants.py to MUTROOT (a scratch copy of the repo),
runs `go test ./internal/...` and, for endpoint mutants that survive it,
`MODEL_SEQS=200 go test -run TestModel ./internal/endpoint`; restores the
file; appends a JSON line per mutant to results.jsonl."""
import json, os, re, subprocess, sys, time
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from mutants import MUTANTS

root = os.path.abspath(sys.argv[1])
only = set(sys.argv[2:])
out = os.path.join(os.path.dirname(os.path.abspath(__file__)), os.environ.get("OUT", "results.jsonl"))
shard = os.environ.get("SHARD")
k, n = map(int, shard.split("/")) if shard else (0, 1)
done = set()
if os.path.exists(out):
    for l in open(out):
        done.add(json.loads(l)["id"])

def locate(src, old, nth):
    idx, pos = [], 0
    while (i := src.find(old, pos)) >= 0:
        idx.append(i); pos = i + 1
    if nth is None:
        return idx[0] if len(idx) == 1 else (None, len(idx))
    return idx[nth] if nth < len(idx) else (None, len(idx))

def gotest(args, env=None, timeout=900):
    e = dict(os.environ, **(env or {}))
    try:
        p = subprocess.run(["go", "test", "-count=1", "-failfast", "-timeout", "300s"] + args,
                           cwd=root, env=e, capture_output=True, text=True, timeout=timeout)
        return p.returncode, p.stdout + p.stderr
    except subprocess.TimeoutExpired as x:
        return -9, (x.stdout or b"").decode() if isinstance(x.stdout, bytes) else (x.stdout or "")

COMPILE = re.compile(r"^\S+\.go:\d+:\d+: (syntax error|declared and not used|undefined|cannot use|invalid operation|missing return|.*not used)", re.M)

def classify(rc, txt):
    if rc == 0:
        return "survived", ""
    ce = COMPILE.search(txt)
    if ce and not re.search(r"^\s*--- FAIL", txt, re.M):
        return "compile-fail", ce.group(0)[:200]
    if "[build failed]" in txt and not re.search(r"^\s*--- FAIL", txt, re.M):
        return "env-fail", "build failed without a compile error (OOM?)"
    m = re.search(r"^\s*--- FAIL: (\S+)", txt, re.M)
    pkg = re.search(r"^FAIL\s+(github.com/\S+)", txt, re.M)
    if m:
        first = m.group(1)
    elif "signal: killed" in txt:
        first = "test binary killed (OOM/runaway)"
    elif "panic:" in txt or rc == -9 or "test timed out" in txt:
        first = "panic/timeout"
    else:
        first = "?"
    if pkg: first += " (" + pkg.group(1).replace("github.com/wommy/empty/", "") + ")"
    return "killed", first

for i, mu in enumerate(MUTANTS):
    if i % n != k:
        continue
    if (only and mu["id"] not in only) or mu["id"] in done:
        continue
    path = os.path.join(root, mu["file"])
    src = open(path).read()
    at = locate(src, mu["old"], mu["nth"])
    rec = dict(mu)
    if isinstance(at, tuple):
        rec.update(status="nomatch", line=0, first=f"{at[1]} occurrences")
    else:
        rec["line"] = src.count("\n", 0, at) + 1
        open(path, "w").write(src[:at] + mu["new"] + src[at + len(mu["old"]):])
        t0 = time.time()
        try:
            rc, txt = gotest(["./internal/..."])
            st, first = classify(rc, txt)
            rec["by"] = "suite"
            if st == "survived" and mu["file"].startswith("internal/endpoint/"):
                rc, txt = gotest(["-run", "TestModel", "./internal/endpoint"], env={"MODEL_SEQS": "200"})
                st, first = classify(rc, txt)
                rec["by"] = "model"
            rec.update(status=st, first=first, secs=round(time.time() - t0, 1))
            if st != "survived":
                rec["tail"] = "\n".join(l for l in txt.splitlines() if l.strip() and not l.startswith("ok ") and "no test files" not in l)[:1500]
        finally:
            open(path, "w").write(src)
    with open(out, "a") as f:
        f.write(json.dumps(rec) + "\n")
    print(rec["id"], rec["status"], rec.get("first", ""), flush=True)
