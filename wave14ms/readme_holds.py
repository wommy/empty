p = '/home/user/empty/.claude/worktrees/agent-a5e3d463acd0910f8/README.md'
s = open(p).read()


def rep(a, b):
    global s
    assert s.count(a) == 1, a
    s = s.replace(a, b)


rep('''volumes with CSI or foreign snapshots or clones (reported as skipped)''',
    '''volumes with CSI or foreign snapshots or clones, user-held snapshots (`userrefs`) or datasets below them (reported as skipped, so no run plans a destroy zfs refuses every time)''')
rep('''Destroys never use `-r`, so zfs refuses on child datasets and clones the scan cannot see.''',
    '''Destroys never use `-r`, so zfs refuses on a dataset or clone that appeared after the scan, or a clone the scan cannot see.''')
rep('''| `-r` took child datasets the depth-1 scan cannot see and snapshots cut after the scan.''',
    '''| `-r` took child datasets someone made below a volume and snapshots cut after the scan.''')
rep('''except a volume zfs refuses for something the scan cannot see (a child dataset, an outside clone of a deferred snapshot)''',
    '''except a volume zfs refuses for something the scan cannot see (an outside clone of a deferred snapshot)''')
open(p, 'w').write(s)
