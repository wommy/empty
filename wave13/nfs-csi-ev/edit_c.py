import sys
root = sys.argv[1]

def sub(path, old, new):
    p = root + '/' + path
    s = open(p).read()
    if old not in s:
        raise SystemExit('missing in %s: %s' % (path, old[:100]))
    s = s.replace(old, new, 1)
    open(p, 'w').write(s)

f = 'internal/endpoint/export.go'
sub(f, '''	plan, err := p.exportOps(row, want)
	if err != nil {
		return nil, Row{}, err
	}
	// unchanged but not served, or served beyond want (a stale etab after
	// a failed exportfs): re-share once; what a re-share cannot fix
	// (another exports file, an ancestor's crossmnt) the caller reports
	if len(plan) == 0 && (!live.serves(row.Mount, c) || len(live.exposure(row.Mount, want)) > 0) {
		plan = reshare(row, want)
	}
	return plan, row, nil
}''', '''	// served beyond want before anything changes: refused, and nothing is
	// planned, so a refused Publish never leaves the requested line
	// applied. Only on a local value (ours): its own lines are want's, so
	// a line beyond them is a stale etab (a failed exportfs, which a
	// re-share of the unchanged value clears; the caller tries that once,
	// ErrServedBeyond) or someone else's (another exports file). An
	// inherited value's lines go when ours is pinned; the caller's check
	// after the change covers the rest.
	if x := live.exposure(row.Mount, want); len(x) > 0 && row.ShareNFSLocal {
		return nil, row, &Error{Code: FailedPrecondition, Err: ErrServedBeyond, Msg: fmt.Sprintf(
			"volume %s is served beyond its client list by the NFS export table: %s; remove these exports (an ancestor dataset's sharenfs with crossmnt, e.g. \\"on\\", or /etc/exports) first",
			row.ID, strings.Join(x, "; "))}
	}
	plan, err := p.exportOps(row, want)
	if err != nil {
		return nil, Row{}, err
	}
	// unchanged but not served (a failed exportfs): re-share once
	if len(plan) == 0 && !live.serves(row.Mount, c) {
		plan = reshare(row, want)
	}
	return plan, row, nil
}

// ErrServedBeyond marks a PlanPublishExport refusal (FailedPrecondition)
// for a volume the export table serves beyond its client list before any
// change. The returned row's own value may clear it with a re-share
// (reshare of clientsOf(row): no property changes) when the extra lines
// are a stale table.
var ErrServedBeyond = errors.New("served beyond the client list")''')

f = 'internal/endpoint/endpoint.go'
sub(f, '''	live, err := e.exportTable(ctx, t, volID)
	if err != nil {
		return "", err
	}
	plan, row, err := e.plan.PlanPublishExport(t, live, volID, c)
	if err != nil {
		return "", err
	}
	if len(plan) > 0 {
		if err := e.apply(ctx, plan); err != nil {
			return "", err
		}
		if live, err = e.exportTable(ctx, t, volID); err != nil {
			return "", err
		}
		if !live.serves(row.Mount, c) {
			return "", errorf(Unavailable, "sharenfs on %s is set, but the NFS export table does not serve %s to %s %s: "+
				"zfs does not report exportfs failures; check exportfs and the NFS server on the storage host", row.Name, row.Mount, c.Host, c.mode())
		}
	}
	// served beyond the request: a line of another exports file for this
	// path, or an ancestor exported with crossmnt (an inherited
	// sharenfs=on) serves this mount to its own clients
	if x := live.exposure(realPath(row.Mount), publishWant(row, c)); len(x) > 0 {
		return "", errorf(FailedPrecondition, "volume %s is served beyond its client list by the NFS export table: %s; "+
			"remove these exports (an ancestor dataset's sharenfs with crossmnt, e.g. \\"on\\", or /etc/exports) first", row.ID, strings.Join(x, "; "))
	}''', '''	live, err := e.exportTable(ctx, t, volID)
	if err != nil {
		return "", err
	}
	plan, row, err := e.plan.PlanPublishExport(t, live, volID, c)
	if errors.Is(err, ErrServedBeyond) && row.Mounted {
		// a stale table (an earlier exportfs failed) goes with a re-share
		// of the unchanged value; what stays is someone else's, and the
		// refusal then leaves every property as it was
		if err := e.apply(ctx, reshare(row, clientsOf(row))); err != nil {
			return "", err
		}
		if live, err = e.exportTable(ctx, t, volID); err != nil {
			return "", err
		}
		plan, row, err = e.plan.PlanPublishExport(t, live, volID, c)
	}
	if err != nil {
		return "", err
	}
	if len(plan) > 0 {
		if err := e.apply(ctx, plan); err != nil {
			return "", err
		}
		if live, err = e.exportTable(ctx, t, volID); err != nil {
			return "", err
		}
		if !live.serves(row.Mount, c) {
			return "", errorf(Unavailable, "sharenfs on %s is set, but the NFS export table does not serve %s to %s %s: "+
				"zfs does not report exportfs failures; check exportfs and the NFS server on the storage host", row.Name, row.Mount, c.Host, c.mode())
		}
	}
	// served beyond the request after the change: what the planner could
	// not see (an inherited value's lines it replaced, an ancestor reached
	// only through the mountpoint's real path, a concurrent edit). The
	// change is undone, so a refused Publish leaves sharenfs as it was.
	if x := live.exposure(realPath(row.Mount), publishWant(row, c)); len(x) > 0 {
		refused := errorf(FailedPrecondition, "volume %s is served beyond its client list by the NFS export table: %s; "+
			"remove these exports (an ancestor dataset's sharenfs with crossmnt, e.g. \\"on\\", or /etc/exports) first", row.ID, strings.Join(x, "; "))
		if len(plan) > 0 {
			if rerr := e.restoreShareNFS(ctx, row); rerr != nil {
				return "", errors.Join(refused, fmt.Errorf("restoring sharenfs: %w", rerr))
			}
		}
		return "", refused
	}''')

sub(f, '''// nfsdRunning is Unavailable unless nfsd threads run.''', '''// restoreShareNFS puts back the sharenfs a refused Publish changed: the
// local value it had (always the endpoint's own: Publish refuses anything
// else before writing), or inheritance. It runs even when the caller's
// context ended, bounded by its own timeout, still under the volume lock.
func (e *Endpoint) restoreShareNFS(ctx context.Context, before Row) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
	defer cancel()
	if before.ShareNFSLocal {
		return e.zfs.Set(ctx, before.Name, map[string]string{"sharenfs": before.ShareNFS})
	}
	return e.zfs.Inherit(ctx, before.Name, "sharenfs")
}

// nfsdRunning is Unavailable unless nfsd threads run.''')
