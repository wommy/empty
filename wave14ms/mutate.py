#!/usr/bin/env python3
"""Apply one textual mutant, run the endpoint package tests, report killers.

usage: mutate.py NAME FILE OLD NEW [go test args...]
OLD/NEW are python string literals' contents (\\n and \\t are expanded).
The file is always restored.
"""
import subprocess
import sys

W = '/home/user/empty/.claude/worktrees/agent-a5e3d463acd0910f8'
name, f, old, new = sys.argv[1:5]
extra = sys.argv[5:]
old = old.encode().decode('unicode_escape')
new = new.encode().decode('unicode_escape')
p = W + '/' + f
src = open(p).read()
if src.count(old) != 1:
    print(f'{name}: OLD occurs {src.count(old)} times in {f}')
    sys.exit(2)
open(p, 'w').write(src.replace(old, new))
try:
    args = ['go', 'test', '-count=1'] + (extra or []) + ['./internal/endpoint/']
    r = subprocess.run(args, cwd=W, capture_output=True, text=True, timeout=900)
    out = r.stdout + r.stderr
    fails = sorted({l.split()[2] for l in out.splitlines() if l.startswith('--- FAIL: ')})
    if 'build failed' in out or '[setup failed]' in out:
        print(f'{name}: BUILD FAILED\n{out[:2000]}')
    elif fails:
        print(f'{name}: KILLED by {", ".join(fails)}')
    elif r.returncode != 0:
        print(f'{name}: FAILED (no test line)\n{out[-1500:]}')
    else:
        print(f'{name}: SURVIVED')
finally:
    open(p, 'w').write(src)
