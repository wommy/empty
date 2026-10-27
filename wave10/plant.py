"""Apply one literal replacement to a file, run a go test, restore.
usage: plant.py <repo> <file> <old> <new> <go test args...>"""
import os, subprocess, sys
repo, f, old, new = sys.argv[1:5]
args = sys.argv[5:]
p = os.path.join(repo, f)
s = open(p).read()
assert s.count(old) == 1, ("not unique", s.count(old))
open(p, "w").write(s.replace(old, new))
try:
    r = subprocess.run(["go", "test", "-count=1"] + args, cwd=repo, capture_output=True, text=True,
                       env=dict(os.environ, GOTOOLCHAIN="go1.27.3"))
    out = [l for l in (r.stdout + r.stderr).splitlines() if l.startswith(("--- FAIL", "ok", "FAIL")) or "_test.go:" in l]
    print("\n".join(out[:12]))
    print("KILLED" if r.returncode else "SURVIVED")
finally:
    open(p, "w").write(s)
