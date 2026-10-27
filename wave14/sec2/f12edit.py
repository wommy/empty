import os
os.chdir('/home/user/empty/.claude/worktrees/agent-a031c2bb39fd727ca')
def sub(p, old, new):
    s=open(p).read()
    assert s.count(old)==1, (p, old, s.count(old))
    open(p,'w').write(s.replace(old,new))
M='cmd/zfs-endpoint/main.go'
sub(M,'''	if err := checkExposure(ctx, z, *parent, etab); err != nil {
		return startupErr(ctx, err)
	}''','''	mount, err := checkExposure(ctx, z, *parent, etab)
	if err != nil {
		return startupErr(ctx, err)
	}
	warnTraversable(slog.Default(), *parent, mount)''')
sub(M,'''func checkExposure(ctx context.Context, z *zfs.Client, parent string, etab endpoint.EtabReader) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	mount, x, err := exposure(ctx, z, parent, etab)
	if err != nil {
		return err
	}
	if len(x) > 0 {
		return configErr(''','''func checkExposure(ctx context.Context, z *zfs.Client, parent string, etab endpoint.EtabReader) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	mount, x, err := exposure(ctx, z, parent, etab)
	if err != nil {
		return "", err
	}
	if len(x) > 0 {
		return mount, configErr(''')
sub(M,'''"unshare it (zfs set sharenfs=off on that dataset, or remove the /etc/exports line) and start again", parent, mount, strings.Join(x, "; "))
	}
	return nil
}''','''"unshare it (zfs set sharenfs=off on that dataset, or remove the /etc/exports line) and start again", parent, mount, strings.Join(x, "; "))
	}
	return mount, nil
}

// warnTraversable warns at startup when users other than the owner and
// group may search the parent's mountpoint (mode o+x): every volume is
// mounted below it, so any local user can then walk into what NFS
// clients wrote there. setuid=off, exec=off and devices=off (F12) stop
// the host running it from the volume, not a user reading or copying it
// out. Only a warning: the mode is the operator's, and a parent with no
// mountpoint, or one not mounted yet, is not checked.
func warnTraversable(log *slog.Logger, parent, mount string) {
	if !filepath.IsAbs(mount) {
		return
	}
	fi, err := os.Stat(mount)
	if err != nil {
		log.Debug("parent mountpoint not checked for traversal", "mountpoint", mount, "error", err)
		return
	}
	if fi.IsDir() && fi.Mode().Perm()&0o001 != 0 {
		log.Warn("the parent's mountpoint is searchable by every local user, who can then reach the files NFS clients write to the volumes below it; "+
			"chmod o-x it (F12)", "parent", parent, "mountpoint", mount, "mode", fi.Mode().Perm().String())
	}
}''')
