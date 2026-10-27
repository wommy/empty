p = '/home/user/empty/.claude/worktrees/agent-a5205796387710ae5/cmd/zfs-endpoint/main.go'
s = open(p).read()
old = '''		if err := d.version(ctx, z); err != nil {
			return err
		}'''
new = '''		if err := d.version(ctx, z); err != nil {
			return startupErr(ctx, err)
		}'''
assert old in s
s = s.replace(old, new)
old = '''	if err := waitParent(ctx, z, *parent, *parentWait, after); err != nil {
		return err
	}'''
new = '''	if err := waitParent(ctx, z, *parent, *parentWait, after); err != nil {
		return startupErr(ctx, err)
	}'''
assert old in s
s = s.replace(old, new)
old = '''// configLockWait maps'''
new = '''// startupErr is err from a startup step, or nil once ctx has ended: a
// SIGTERM (systemctl stop) while waiting for the pool or probing zfs is a
// normal stop, whatever the interrupted probe returned. Exit 1 would leave
// the unit failed, and 75 would have Restart=on-failure start it again.
func startupErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		slog.Info("stopped during startup", "error", err)
		return nil
	}
	return err
}

// configLockWait maps'''
assert old in s
s = s.replace(old, new)
open(p, 'w').write(s)
