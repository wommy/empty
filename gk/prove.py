#!/usr/bin/env python3
"""prove.py ROOT ID PKG RUN [extra go test args]: apply mutant ID, run test (expect FAIL), revert, run (expect PASS)."""
import os, subprocess, sys
sys.path.insert(0, "/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave25")
from mutants import MUTANTS
root, mid, pkg, run = sys.argv[1:5]
extra = sys.argv[5:]
mu = next(m for m in MUTANTS if m["id"] == mid)
path = os.path.join(root, mu["file"]); src = open(path).read()
idx = []; p = 0
while (i := src.find(mu["old"], p)) >= 0:
    idx.append(i); p = i + 1
at = idx[mu["nth"] or 0] if (mu["nth"] is not None or len(idx) == 1) else None
if at is None:
    print(mid, "NOMATCH", len(idx)); sys.exit(2)
def t():
    r = subprocess.run(["go", "test", "-count=1", "-timeout", "120s", "-run", run, pkg] + extra, cwd=root, capture_output=True, text=True)
    return r.returncode, r.stdout + r.stderr
open(path, "w").write(src[:at] + mu["new"] + src[at + len(mu["old"]):])
try:
    rc1, o1 = t()
finally:
    open(path, "w").write(src)
rc2, o2 = t()
fail = [l.strip() for l in o1.splitlines() if "--- FAIL" in l or "panic:" in l][:4]
print(f"{mid}: mutant rc={rc1} {'KILLED' if rc1 else 'SURVIVED'} {fail}; clean rc={rc2} {'PASS' if rc2 == 0 else 'FAIL'}")
if rc2: print(o2[-2000:])
if rc1 == 0: print(o1[-800:])
