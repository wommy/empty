"""First failing seed of TestSimulateDestroyAllEquivalent per mutant of
simulateDestroyAll (internal/endpoint/reconcile.go 419-466 at HEAD).
usage: seeds.py <tree>"""
import json, os, re, subprocess, sys
D = os.path.dirname(os.path.abspath(__file__))
tree = sys.argv[1]
gen = json.load(open(os.path.join(D, "headgen-full.json")))
ms = [g for g in gen if g["file"] == "internal/endpoint/reconcile.go" and 419 <= g["line"] <= 466]
path = os.path.join(tree, "internal/endpoint/reconcile.go")
orig = open(path, "rb").read()
env = dict(os.environ, GOTOOLCHAIN="go1.27.3")
for m in ms:
    open(path, "wb").write(orig[:m["start"]] + m["repl"].encode() + orig[m["end"]:])
    try:
        r = subprocess.run(["go", "test", "-count=1", "-run", "^TestSimulateDestroyAllEquivalent$", "./internal/endpoint"],
                           cwd=tree, env=env, capture_output=True, text=True, timeout=600)
        so = r.stdout + r.stderr
        s = re.search(r"seed (\d+):", so)
        res = s.group(1) if s else ("panic" if "panic" in so else ("build" if "build failed" in so else ("ok" if r.returncode == 0 else "fail")))
    finally:
        open(path, "wb").write(orig)
    print(m["id"], m["line"], m["op"], m["desc"][:70], "->", res, flush=True)
