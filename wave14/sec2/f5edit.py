import os
os.chdir('/home/user/empty/.claude/worktrees/agent-a031c2bb39fd727ca')
def sub(p, old, new):
    s=open(p).read()
    assert s.count(old)==1, (p, old, s.count(old))
    open(p,'w').write(s.replace(old,new))
M='cmd/zfs-endpoint/main.go'
sub(M,'''	go dmn.Probe(probeCtx, healthInterval, func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, probeTimeout)
		defer cancel()
		_, err := z.Get(ctx, *parent, "type")
		return err
	})''','''	go dmn.Probe(probeCtx, healthInterval, healthCheck(z, *parent, etab, slog.Default()))''')
sub(M,'''func checkExposure(ctx context.Context, z *zfs.Client, parent string, etab endpoint.EtabReader) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	ds, err := z.Get(ctx, parent, "mountpoint")
	if err != nil {
		return fmt.Errorf("parent %s: reading its mountpoint: %w", parent, err)
	}
	mount := ds.Props["mountpoint"].Value
	if !filepath.IsAbs(mount) {
		return nil
	}
	b, err := etab.ReadEtab(ctx)
	if err != nil {
		return fmt.Errorf("reading the NFS export table: %w", err)
	}
	if x := endpoint.ExposedByAncestor(b, mount); len(x) > 0 {
		return configErr("parent %s (mounted at %s) is under a crossmnt NFS export, which serves every volume to its clients as soon as it is created: %s; "+
			"unshare it (zfs set sharenfs=off on that dataset, or remove the /etc/exports line) and start again", parent, mount, strings.Join(x, "; "))
	}
	return nil
}''','''func checkExposure(ctx context.Context, z *zfs.Client, parent string, etab endpoint.EtabReader) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	mount, x, err := exposure(ctx, z, parent, etab)
	if err != nil {
		return err
	}
	if len(x) > 0 {
		return configErr("parent %s (mounted at %s) is under a crossmnt NFS export, which serves every volume to its clients as soon as it is created: %s; "+
			"unshare it (zfs set sharenfs=off on that dataset, or remove the /etc/exports line) and start again", parent, mount, strings.Join(x, "; "))
	}
	return nil
}

// exposure is the parent's mountpoint and the crossmnt etab lines that
// cover it (endpoint.ExposedByAncestor); none for a parent with no
// mountpoint (none, legacy).
func exposure(ctx context.Context, z *zfs.Client, parent string, etab endpoint.EtabReader) (string, []string, error) {
	ds, err := z.Get(ctx, parent, "mountpoint")
	if err != nil {
		return "", nil, fmt.Errorf("parent %s: reading its mountpoint: %w", parent, err)
	}
	mount := ds.Props["mountpoint"].Value
	if !filepath.IsAbs(mount) {
		return mount, nil, nil
	}
	b, err := etab.ReadEtab(ctx)
	if err != nil {
		return mount, nil, fmt.Errorf("reading the NFS export table: %w", err)
	}
	return mount, endpoint.ExposedByAncestor(b, mount), nil
}

// healthCheck is the health probe's check: the parent must answer zfs
// (a suspended or exported pool turns health NOT_SERVING). It also
// re-runs the startup exposure check (F5): a crossmnt export that came
// to cover the parent after startup is logged as a warning, never
// NOT_SERVING, which would refuse every CSI call, deletes included, for
// an export only an administrator can remove. The warning is logged
// when the covering lines appear or change, and an info line when they
// are gone; between, nothing repeats. Failing to read the exposure
// changes nothing either.
func healthCheck(z *zfs.Client, parent string, etab endpoint.EtabReader, log *slog.Logger) func(context.Context) error {
	var last string
	return func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, probeTimeout)
		defer cancel()
		if _, err := z.Get(ctx, parent, "type"); err != nil {
			return err
		}
		mount, x, err := exposure(ctx, z, parent, etab)
		if err != nil {
			log.Debug("health: exposure not checked", "error", err)
			return nil
		}
		now := strings.Join(x, "; ")
		switch {
		case now == last:
		case now != "":
			log.Warn("health: parent is under a crossmnt NFS export, which serves every volume to its clients; "+
				"unshare it (zfs set sharenfs=off on that dataset, or remove the /etc/exports line)",
				"parent", parent, "mountpoint", mount, "exports", now)
		default:
			log.Info("health: parent is no longer under a crossmnt NFS export", "parent", parent, "mountpoint", mount)
		}
		last = now
		return nil
	}
}''')
