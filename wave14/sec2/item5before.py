"""Fail-before for item 5: the drop-in as at HEAD and no ci/real-zfs-unit.sh,
with the new deploy tests; then restore and run again."""
import os, subprocess
WT = '/home/user/empty/.claude/worktrees/agent-a031c2bb39fd727ca'
HERE = os.path.dirname(os.path.abspath(__file__))
os.chdir(WT)
conf = 'deploy/systemd/zfs-endpoint@.service.d/hardening.conf'
script = 'ci/real-zfs-unit.sh'
saved = {p: open(p).read() for p in (conf, script)}
head_conf = subprocess.run(['git', 'show', 'HEAD:' + conf], capture_output=True, text=True, check=True).stdout
cmd = ['go', 'test', '-count=1', '-race', '-run', 'TestUnitLint|TestUnitCheck', '-v', './deploy']
try:
    open(conf, 'w').write(head_conf)
    os.remove(script)
    r = subprocess.run(cmd, capture_output=True, text=True)
    open(os.path.join(HERE, 'item5-before.txt'), 'w').write('# HEAD drop-in, no ci/real-zfs-unit.sh\n' + r.stdout + r.stderr)
    print('before exit', r.returncode)
finally:
    for p, s in saved.items():
        open(p, 'w').write(s)
    os.chmod(script, 0o755)
r = subprocess.run(cmd, capture_output=True, text=True)
open(os.path.join(HERE, 'item5-after.txt'), 'w').write(r.stdout + r.stderr)
print('after exit', r.returncode)
