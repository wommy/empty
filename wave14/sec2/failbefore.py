"""failbefore.py <spec.json>: disable a fix, run its tests, restore.

spec: {"name": "D7", "edits": [{"file": ..., "old": ..., "new": ...}],
       "pkg": "./internal/x", "run": "TestA|TestB"}
Writes <name>-before.txt (fix disabled) and <name>-after.txt (restored)
next to this script. Files are restored from memory even on failure.
"""
import json, os, subprocess, sys

WT = '/home/user/empty/.claude/worktrees/agent-a031c2bb39fd727ca'
HERE = os.path.dirname(os.path.abspath(__file__))
spec = json.load(open(sys.argv[1]))
os.chdir(WT)
orig = {}
try:
    for e in spec['edits']:
        f = e['file']
        s = open(f).read()
        orig.setdefault(f, s)
        cur = open(f).read()
        assert cur.count(e['old']) == 1, (f, e['old'][:60], cur.count(e['old']))
        open(f, 'w').write(cur.replace(e['old'], e['new']))
    r = subprocess.run(['go', 'test', '-count=1', '-race', '-run', spec['run'], '-v', spec['pkg']],
                       capture_output=True, text=True)
    open(os.path.join(HERE, spec['name'] + '-before.txt'), 'w').write(
        '# fix disabled: ' + json.dumps([e['file'] for e in spec['edits']]) + '\n' + r.stdout + r.stderr)
    print('before exit', r.returncode)
finally:
    for f, s in orig.items():
        open(f, 'w').write(s)
r = subprocess.run(['go', 'test', '-count=1', '-race', '-run', spec['run'], '-v', spec['pkg']],
                   capture_output=True, text=True)
open(os.path.join(HERE, spec['name'] + '-after.txt'), 'w').write(r.stdout + r.stderr)
print('after exit', r.returncode)
