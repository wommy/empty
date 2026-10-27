#!/usr/bin/env python3
# usage: mutj.py <spec.json>: a list of {label, file, old, new, pkg, run, args?}.
# Each applies one exact replacement, runs the tests, restores the file and
# appends its evidence to cov-evidence.md.
import sys, subprocess, json
root = '/home/user/empty/.claude/worktrees/agent-a3b322c80ba25f7fc'
ev = '/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave24/cov-evidence.md'
for m in json.load(open(sys.argv[1])):
    f = root + '/' + m['file']
    old, new = m['old'], m['new']
    orig = open(f).read()
    assert orig.count(old) == 1, "%s: old must occur once (%d)" % (m['label'], orig.count(old))
    extra = m.get('args', [])
    try:
        open(f, 'w').write(orig.replace(old, new))
        r = subprocess.run(['go', 'test', '-count=1', '-run', m['run'], *extra, m['pkg']],
                           capture_output=True, text=True, cwd=root)
    finally:
        open(f, 'w').write(orig)
    out = (r.stdout + r.stderr).strip()
    lines = [l for l in out.splitlines() if l.strip()]
    if len(lines) > 25:
        lines = lines[:25] + ['...']
    with open(ev, 'a') as e:
        e.write(f"\n### {m['label']}\n\nMutation in `{m['file']}`:\n\n```diff\n" +
                ''.join('- ' + l + '\n' for l in old.rstrip('\n').splitlines()) +
                ''.join('+ ' + l + '\n' for l in new.rstrip('\n').splitlines()) +
                f"```\n\n`go test -count=1 -run '{m['run']}' {' '.join(extra)} {m['pkg']}` exit {r.returncode}:\n\n```\n" +
                '\n'.join(lines) + "\n```\n")
    print('=====', m['label'], 'EXIT', r.returncode); print(out)
