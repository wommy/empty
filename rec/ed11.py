import os
os.chdir('/home/user/empty/.claude/worktrees/agent-ad45c3ab7d0b822f7')
def sub(p, pairs):
    s=open(p).read()
    for old,new in pairs:
        assert s.count(old)==1, (p, old)
        s=s.replace(old,new)
    open(p,'w').write(s)

sub('internal/endpoint/reconcile.go', [
('''	// held, when non-nil, limits actions to ones whose volumes are all in
	// it (the locks Reconcile holds); the rest are skipped and their
	// volumes stay, so nothing that depends on them is planned either.
	held map[string]bool''','''	// held, when non-nil, limits actions to ones whose volumes are all
	// held (true: Reconcile holds the lock); the rest are skipped, as busy
	// when the lock was tried and taken by someone else (false), and their
	// volumes stay, so nothing that depends on them is planned either.
	held map[string]bool'''),
('''// allows reports whether a's volumes are all held and a was planned, for
// the same reason, before the locks were taken.
func (d Desired) allows(a ReconcileAction) bool {
	if d.held != nil && slices.ContainsFunc(a.Volumes, func(v string) bool { return !d.held[v] }) {
		return false
	}
	if d.planned != nil {
		if why, ok := d.planned[a.Target]; !ok || why != a.Reason {
			return false
		}
	}
	return true
}''','''// refuses says why a may not run under the locks: "" when its volumes
// are all held and it was planned, for the same reason, before they were
// taken.
func (d Desired) refuses(a ReconcileAction) string {
	if d.held != nil {
		for _, v := range a.Volumes {
			if held, tried := d.held[v]; !held {
				if tried {
					return SkipBusy
				}
				return SkipChangedSinceScan
			}
		}
	}
	if d.planned != nil {
		if why, ok := d.planned[a.Target]; !ok || why != a.Reason {
			return SkipChangedSinceScan
		}
	}
	return ""
}'''),
('''			if slices.ContainsFunc(acts, func(a ReconcileAction) bool { return !d.allows(a) }) {
				skip(c.id, SkipChangedSinceScan)
				continue
			}''','''			why := ""
			for _, a := range acts {
				if why = d.refuses(a); why != "" {
					break
				}
			}
			if why != "" {
				skip(c.id, why)
				continue
			}'''),
('''		if !d.allows(a) {
			skip(r.ID, SkipChangedSinceScan)
			continue
		}''','''		if why := d.refuses(a); why != "" {
			skip(r.ID, why)
			continue
		}'''),
('''	SkipNotAttempted     = "not attempted"''','''	SkipNotAttempted    = "not attempted"
	// SkipBusy: another operation held one of the volumes; reconcile never
	// waits for a lock, so one slow call cannot stall the collection.
	SkipBusy = "busy: another operation holds it; next reconcile"'''),
('''	held := map[string]bool{}
	var keys []string
	for _, a := range plan.Actions {
		for _, v := range a.Volumes {
			if !held[v] {
				held[v] = true
				keys = append(keys, "vol:"+v)
			}
		}
	}
	release, err := e.lock(ctx, keys...)
	if err != nil {
		return ReconcilePlan{}, err
	}
	defer release()

	if t, err = e.scan(ctx, ""); err != nil {
		return ReconcilePlan{}, err
	}
	d.Now, d.held, d.planned = e.cfg.Now(), held, plan.targets()
	if plan, err = e.plan.PlanReconcile(t, d); err != nil {
		return ReconcilePlan{}, err
	}
	return e.applyReconcile(ctx, plan, nil), nil
}''','''	// Try each volume's lock without waiting: a busy one skips only the
	// groups that need it (adv4 F8). Waiting for every key at once let one
	// slow call abort the whole run, and Aborted every CSI call on the
	// keys already taken meanwhile. Never waiting means no wait cycle.
	held := map[string]bool{}
	releases := map[string]func(){}
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	for _, a := range plan.Actions {
		for _, v := range a.Volumes {
			if _, tried := held[v]; !tried {
				release, ok := e.locks.TryAcquire("vol:" + v)
				held[v] = ok
				if ok {
					releases[v] = release
				}
			}
		}
	}

	if t, err = e.scan(ctx, ""); err != nil {
		return ReconcilePlan{}, err
	}
	d.Now, d.held, d.planned = e.cfg.Now(), held, plan.targets()
	if plan, err = e.plan.PlanReconcile(t, d); err != nil {
		return ReconcilePlan{}, err
	}
	// release each lock right after the last group that needs it
	last := map[string]int{}
	i := 0
	for g, grp := range plan.groups {
		for _, a := range plan.Actions[i : i+grp.n] {
			for _, v := range a.Volumes {
				last[v] = g
			}
		}
		i += grp.n
	}
	for v, release := range releases {
		if _, used := last[v]; !used {
			release()
			delete(releases, v)
		}
	}
	return e.applyReconcile(ctx, plan, func(g int) {
		for v, lg := range last {
			if lg == g {
				releases[v]()
				delete(releases, v)
			}
		}
	}), nil
}'''),
])
