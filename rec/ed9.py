import os
os.chdir('/home/user/empty/.claude/worktrees/agent-ad45c3ab7d0b822f7')
def sub(p, pairs):
    s=open(p).read()
    for old,new in pairs:
        assert s.count(old)==1, (p, old)
        s=s.replace(old,new)
    open(p,'w').write(s)

sub('internal/endpoint/endpoint.go', [
('''		n.snapshotName(), n.snapshotSource(), n.nfsClients(), n.cloneSourceFor()}''',
 '''		n.snapshotName(), n.snapshotSource(), n.nfsClients(), n.cloneSourceFor(), n.committedAt()}'''),
('''// nfsClients is the endpoint's own property''','''// committedAt is the endpoint's own property: the Unix time (endpoint
// clock) CreateVolume committed the volume, set with provision_success.
// Reconcile ages a committed volume from it, not from its zfs creation: a
// provision retried long after its first attempt is new to the caller.
func (n props) committedAt() string { return n.p + ":committed_at" }

// nfsClients is the endpoint's own property'''),
('''	plan, err := e.plan.PlanCreateVolume(t, r)''','''	p := e.plan
	p.now = e.cfg.Now().Unix()
	plan, err := p.PlanCreateVolume(t, r)'''),
])

sub('internal/endpoint/table.go', [
('''	CloneSourceFor string // internal clone-source snapshots: the clone's id
}''','''	CloneSourceFor string // internal clone-source snapshots: the clone's id
	CommittedAt    int64  // committed_at, 0 if unset or unreadable
}'''),
('''		n.snapshotName(), n.snapshotSource(), n.nfsClients(), n.cloneSourceFor(),
	}, propNames[:]...)''','''		n.snapshotName(), n.snapshotSource(), n.nfsClients(), n.cloneSourceFor(), n.committedAt(),
	}, propNames[:]...)'''),
('''	cCloneFor := j(n.cloneSourceFor())''','''	cCloneFor, cCommittedAt := j(n.cloneSourceFor()), j(n.committedAt())'''),
('''		if v, err := strconv.ParseInt(cell(cCreation).Value, 10, 64); err == nil {
			r.Creation = v
		}''','''		if v, err := strconv.ParseInt(cell(cCreation).Value, 10, 64); err == nil {
			r.Creation = v
		}
		if v, err := strconv.ParseInt(own(cCommittedAt), 10, 64); err == nil && v > 0 {
			r.CommittedAt = v
		}'''),
])

sub('internal/endpoint/plan.go', [
('''	// DestroyForeignSnapshots: Config.DestroyForeignSnapshots.
	DestroyForeignSnapshots bool
}''','''	// DestroyForeignSnapshots: Config.DestroyForeignSnapshots.
	DestroyForeignSnapshots bool

	// now, when set, is stamped as committed_at by a create's commit: the
	// caller's clock reading, so planning stays pure.
	now int64
}'''),
('''	commit := Op{Kind: OpSet, Target: p.full(id), Props: map[string]string{p.Names.success(): "true"}}''','''	commit := Op{Kind: OpSet, Target: p.full(id), Props: map[string]string{p.Names.success(): "true"}}
	if p.now > 0 {
		commit.Props[p.Names.committedAt()] = strconv.FormatInt(p.now, 10)
	}'''),
])

sub('internal/endpoint/reconcile.go', [
('''func tooYoung(r Row, d Desired) string {
	if r.Creation <= 0 {
		return SkipNoCreation
	}
	if d.Now.Sub(time.Unix(r.Creation, 0)) < d.MinAge {''','''// tooYoung ages a row from its zfs creation or, for a committed volume,
// from its commit when that is later: a CreateVolume retried long after its
// first attempt reported the volume to its caller only now, after the
// caller may have taken its desired set (adv4 F1).
func tooYoung(r Row, d Desired) string {
	if r.Creation <= 0 {
		return SkipNoCreation
	}
	born := r.Creation
	if r.Committed && r.CommittedAt > born {
		born = r.CommittedAt
	}
	if d.Now.Sub(time.Unix(born, 0)) < d.MinAge {'''),
# F1a: first plan
('''	// held, when non-nil, limits actions to ones whose volumes are all in
	// it (the locks Reconcile holds); the rest are skipped and their
	// volumes stay, so nothing that depends on them is planned either.
	held map[string]bool
}

func (d Desired) covers(vols []string) bool {
	return d.held == nil || !slices.ContainsFunc(vols, func(v string) bool { return !d.held[v] })
}''','''	// held, when non-nil, limits actions to ones whose volumes are all in
	// it (the locks Reconcile holds); the rest are skipped and their
	// volumes stay, so nothing that depends on them is planned either.
	held map[string]bool
	// planned, when non-nil, is the unlocked plan's target -> reason. The
	// locked re-plan keeps only actions the first plan already made for the
	// same reason: a row whose state changed in between (an uncommitted
	// volume a racing CreateVolume just committed, now "undesired") is
	// left for the next run, judged afresh (adv4 F1).
	planned map[string]string
}

// allows reports whether a's volumes are all held and a was planned, for
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
}

// targets is p's target -> reason, for Desired.planned.
func (r ReconcilePlan) targets() map[string]string {
	m := make(map[string]string, len(r.Actions))
	for _, a := range r.Actions {
		m[a.Target] = a.Reason
	}
	return m
}'''),
('''			if slices.ContainsFunc(acts, func(a ReconcileAction) bool { return !d.covers(a.Volumes) }) {''','''			if slices.ContainsFunc(acts, func(a ReconcileAction) bool { return !d.allows(a) }) {'''),
('''		// CreateVolume(clone) holds vol:<clone> while cutting it
		vols := []string{vol, clone}
		if !d.covers(vols) {
			skip(r.ID, SkipChangedSinceScan)
			continue
		}
		out.Actions = append(out.Actions, ReconcileAction{
			Op:     Op{Kind: OpDestroy, Target: r.Name, Defer: true, OkMissing: true},
			Target: r.ID, Reason: ReasonOrphanCloneSource, Volumes: vols,
		})''','''		// CreateVolume(clone) holds vol:<clone> while cutting it
		a := ReconcileAction{
			Op:     Op{Kind: OpDestroy, Target: r.Name, Defer: true, OkMissing: true},
			Target: r.ID, Reason: ReasonOrphanCloneSource, Volumes: []string{vol, clone},
		}
		if !d.allows(a) {
			skip(r.ID, SkipChangedSinceScan)
			continue
		}
		out.Actions = append(out.Actions, a)'''),
('''	SkipChangedSinceScan = "appeared after the targets were locked; next reconcile"''','''	SkipChangedSinceScan = "changed since the scan, or not locked; next reconcile"'''),
('''// DefaultReconcileMinAge protects in-flight provisions and stale desired
// sets when the caller names no age.
const DefaultReconcileMinAge = time.Hour''','''// DefaultReconcileMinAge protects in-flight provisions and stale desired
// sets when the caller names no age.
const DefaultReconcileMinAge = time.Hour

// MinReconcileMinAge is the smallest MinAge a caller may ask for. A
// CreateVolume that failed leaves an uncommitted row that
// external-provisioner retries with exponential backoff capped at 5
// minutes (--retry-interval-max), and a caller's desired set (its PV list)
// can only include a volume after CreateVolume returned. Twice that cap
// keeps an in-flight provision and a just-committed volume out of reach
// even across one missed retry; anything shorter turns a slow retry into
// a destroy. Zero still means DefaultReconcileMinAge.
const MinReconcileMinAge = 10 * time.Minute'''),
('''	d := Desired{VolumeIDs: r.DesiredVolumeIDs, MinAge: r.MinAge, Now: e.cfg.Now()}
	if d.MinAge <= 0 {
		d.MinAge = DefaultReconcileMinAge
	}''','''	if r.MinAge != 0 && r.MinAge < MinReconcileMinAge {
		return ReconcilePlan{}, errorf(InvalidArgument, "min age %v is below the floor of %v", r.MinAge, MinReconcileMinAge)
	}
	d := Desired{VolumeIDs: r.DesiredVolumeIDs, MinAge: r.MinAge, Now: e.cfg.Now()}
	if d.MinAge == 0 {
		d.MinAge = DefaultReconcileMinAge
	}'''),
('''	d.Now, d.held = e.cfg.Now(), held''','''	d.Now, d.held, d.planned = e.cfg.Now(), held, plan.targets()'''),
])
