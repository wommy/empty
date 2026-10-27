#!/usr/bin/env python3
# usage: mut.py <label> <file> <old-file> <new-file> <pkg> <run-regex> [extra go test args]
# applies one exact replacement, runs the tests, restores the file, appends evidence.
import sys, subprocess
label, f, oldp, newp, pkg, run = sys.argv[1:7]
extra = sys.argv[7:]
old = open(oldp).read(); new = open(newp).read()
orig = open(f).read()
assert orig.count(old) == 1, "old must occur once (%d)" % orig.count(old)
ev = '/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave24/cov-evidence.md'
try:
    open(f, 'w').write(orig.replace(old, new))
    r = subprocess.run(['go', 'test', '-count=1', '-run', run, *extra, pkg], capture_output=True, text=True,
                       cwd='/home/user/empty/.claude/worktrees/agent-a3b322c80ba25f7fc')
finally:
    open(f, 'w').write(orig)
out = (r.stdout + r.stderr).strip()
lines = [l for l in out.splitlines() if l.strip()]
if len(lines) > 25:
    lines = lines[:25] + ['...']
with open(ev, 'a') as e:
    e.write(f"\n### {label}\n\nMutation in `{f}`:\n\n```diff\n" +
            ''.join('- ' + l + '\n' for l in old.rstrip('\n').splitlines()) +
            ''.join('+ ' + l + '\n' for l in new.rstrip('\n').splitlines()) +
            f"```\n\n`go test -count=1 -run '{run}' {' '.join(extra)} {pkg}` exit {r.returncode}:\n\n```\n" + '\n'.join(lines) + "\n```\n")
print(out); print("EXIT", r.returncode)
