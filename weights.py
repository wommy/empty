import sys
p = '/home/user/empty/.claude/worktrees/agent-a641f1e792d2b8587/ci/real-zfs-weights.txt'
lines = open(p).read().split('\n')
for name in sys.argv[1:]:
    if any(l.split(' ')[0] == name for l in lines):
        continue
    for i, l in enumerate(lines):
        if l.endswith(' 0.00') and l.split(' ')[0] > name:
            lines.insert(i, name + ' 0.00')
            break
    else:
        raise SystemExit('no place for ' + name)
open(p, 'w').write('\n'.join(lines))
