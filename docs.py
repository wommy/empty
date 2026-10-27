root = '/home/user/empty/.claude/worktrees/agent-a5205796387710ae5/'
p = root + 'cmd/zfs-endpoint/startup.go'
s = open(p).read()
old = '''//   - exitConfig: bad flags, wrong OpenZFS, a parent that is not a
//     filesystem. Restarting cannot help; RestartPreventExitStatus=78.
//   - exitTempFail: the parent is not there yet (pool not imported, key
//     not loaded). Restart=on-failure retries it with backoff.
//   - 1: anything else.'''
new = '''//   - exitConfig: bad flags, a socket path bind cannot take, another
//     endpoint holding the parent (or a symlinked lock), wrong OpenZFS, a
//     parent that is not a filesystem, only democratic-csi volumes under
//     the default prefix. Restarting cannot help;
//     RestartPreventExitStatus=78.
//   - exitTempFail: the parent is not there yet (pool not imported, key
//     not loaded). Restart=on-failure retries it with backoff.
//   - 0: also a stop (SIGTERM) during startup, which is not a failure.
//   - 1: anything else.'''
assert old in s
s = s.replace(old, new)
open(p, 'w').write(s)

p = root + 'README.md'
s = open(p).read()
old = '''A busy lock is waited for up to `-lock-wait` (default 30 s), cut'''
new = '''A busy lock is waited for up to `-lock-wait` (default 30 s; `0` takes a free lock and never waits, a negative value is refused), cut'''
assert old in s
s = s.replace(old, new)
old = '''`democratic-csi` is the adoption mode |'''
new = '''`democratic-csi` is the adoption mode. Without the flag, a parent whose volumes carry only `democratic-csi:managed_resource` (provisioned before the default changed) exits 78 at startup, naming the flag |'''
assert old in s
s = s.replace(old, new)
open(p, 'w').write(s)
