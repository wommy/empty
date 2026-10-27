package endpoint

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/wommy/empty/internal/zfs"
)

// Reconcile collects garbage off the request path. Three kinds arise:
//
//   - uncommitted volumes: owned rows without the provision_success marker,
//     left by a CreateVolume that crashed or failed and was never retried
//   - clone-source snapshots ("<src>@volume-source-for-volume-<id>") whose
//     clone is gone (a crash between destroying a clone and its origin, or a
//     clone that failed after the snapshot was cut)
//   - committed volumes the caller no longer wants
//
// The endpoint cannot know what is wanted: the caller supplies the complete
// desired set. Properties say what the endpoint made, never what is still
// wanted (docs/research/edges2/reconcile-gc.md).

// Reasons a reconcile acts. Stable strings: front ends may match on them.
const (
	ReasonUncommitted        = "uncommitted: provision never completed"
	ReasonUndesired          = "not in the desired set"
	ReasonOrphanCloneSource  = "clone-source snapshot without a clone"
	ReasonReleasedWithVolume = "clone source of a collected volume"
	ReasonWithVolume         = "the endpoint's snapshot of a collected volume"
)

// Reasons a reconcile leaves something alone (besides PlanDeleteVolume's
// refusals: CSI snapshots and dependent clones).
const (
	SkipTooYoung        = "younger than the minimum age"
	SkipNoCreation      = "creation time unknown"
	SkipEmptyDesired    = "desired set names no existing volume: undesired volumes are not collected"
	SkipDesiredMissing  = "desired volume does not exist"
	SkipUnmanagedSource = "clone-source snapshot on an unmanaged volume"
	// SkipUnstampedCloneSource: a snapshot with the reserved clone-source
	// name but without the endpoint's stamp (managed_resource and
	// clone_source_for set locally at creation) is not the endpoint's.
	SkipUnstampedCloneSource = "clone-source name without the endpoint's stamp: not ours"
	SkipUnaddressable        = "name the endpoint cannot address"
	// SkipFailed prefixes the error of a group whose apply failed (what
	// of it ran is in Actions); the run went on with the next group.
	SkipFailed = "failed"
	// SkipDependentFailed: a volume whose clone, collected in the same
	// run, failed; it is not attempted.
	SkipDependentFailed = "not attempted: a clone collected first failed"
	SkipNotAttempted    = "not attempted"
	// SkipBusy: another operation held one of the volumes; reconcile never
	// waits for a lock, so one slow call cannot stall the collection.
	SkipBusy             = "busy: another operation holds it; next reconcile"
	SkipChangedSinceScan = "changed since the scan, or not locked; next reconcile"
)

// Desired is the caller's statement of what to keep.
type Desired struct {
	// VolumeIDs is the complete set of volumes the caller wants. Every owned
	// volume outside it is undesired. Empty means "no statement": only
	// uncommitted rows and clone-source snapshots are collected, because an
	// empty set cannot be told apart from a caller that sent nothing. A set
	// in which no id names a volume the endpoint owns counts as empty.
	VolumeIDs []string
	// MinAge: nothing created less than MinAge before Now is touched. It
	// keeps in-flight provisions and volumes created after the caller took
	// its desired set out of reach.
	MinAge time.Duration
	Now    time.Time

	// held, when non-nil, limits actions to ones whose volumes are all
	// held (true: Reconcile holds the lock); the rest are skipped, as busy
	// when the lock was tried and taken by someone else (false), and their
	// volumes stay, so nothing that depends on them is planned either.
	held map[string]bool
	// planned, when non-nil, is the unlocked plan's target -> reason. The
	// locked re-plan keeps only actions the first plan already made for the
	// same reason: a row whose state changed in between (an uncommitted
	// volume a racing CreateVolume just committed, now "undesired") is
	// left for the next run, judged afresh (adv4 F1).
	planned map[string]string
}

// refuses says why a may not run under the locks: "" when its volumes
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
}

// targets is p's target -> reason, for Desired.planned.
func (r ReconcilePlan) targets() map[string]string {
	m := make(map[string]string, len(r.Actions))
	for _, a := range r.Actions {
		m[a.Target] = a.Reason
	}
	return m
}

// ReconcileAction is one planned mutation and why.
type ReconcileAction struct {
	Op     Op
	Target string // id relative to the parent: "<vol>" or "<vol>@<snap>"
	Reason string
	// Volumes whose locks cover this op.
	Volumes []string
}

// ReconcileSkip is something reconcile found and left alone.
type ReconcileSkip struct {
	Target string
	Reason string
}

// ReconcilePlan is a reconcile as data: what would be done, in order, and
// what was left alone. Dry runs return it unapplied.
type ReconcilePlan struct {
	Actions []ReconcileAction
	Skipped []ReconcileSkip

	// groups partitions Actions into what is applied, and fails, as one:
	// a volume with its snapshots and released origin, or one orphan.
	groups []reconcileGroup
}

type reconcileGroup struct {
	target string // what a failure is reported against
	n      int    // its actions, consecutive in Actions
	deps   []int  // earlier groups that must have succeeded (its clones)
}

// Plan is the ops of the actions, in order.
func (r ReconcilePlan) Plan() Plan {
	plan := make(Plan, len(r.Actions))
	for i, a := range r.Actions {
		plan[i] = a.Op
	}
	return plan
}

// PlanReconcile decides what to collect. Pure. It never targets:
//   - a row the endpoint does not own (ownership is locally set, table.go)
//   - a volume in the desired set
//   - a CSI snapshot, or a volume that has one or has dependent clones
//     (PlanDeleteVolume's refusals; reported as skipped)
//   - anything younger than d.MinAge
//
// Volume deletions are planned to a fixpoint: a volume blocked only by a
// clone that is itself collected goes in the same plan, after the clone, so
// reconciling the result again plans nothing.
func (p Planner) PlanReconcile(t Table, d Desired) (ReconcilePlan, error) {
	// everything not wanted is collected: only the whole parent says what
	// exists
	if err := t.mustBeWhole("reconcile"); err != nil {
		return ReconcilePlan{}, err
	}
	var out ReconcilePlan
	want := map[string]bool{}
	for _, id := range d.VolumeIDs {
		want[id] = true
	}
	skip := func(target, reason string) {
		out.Skipped = append(out.Skipped, ReconcileSkip{Target: target, Reason: reason})
	}
	// A set naming none of the endpoint's volumes says nothing about them:
	// a page of another list, another cluster's or another parent's set.
	// Like the empty set, it collects no committed volume (adv4 F2).
	stated := false
	for _, id := range slices.Sorted(maps.Keys(want)) {
		r, ok := t.Get(id)
		if !ok || r.Type == zfs.TypeSnapshot {
			skip(id, SkipDesiredMissing)
		}
		stated = stated || ok && r.OwnedVolume()
	}

	type candidate struct {
		id     string
		reason string
	}
	var pending []candidate
	for r := range t.Volumes() {
		if !r.OwnedVolume() || want[r.ID] {
			continue
		}
		reason := ReasonUncommitted
		if r.Committed {
			if !stated {
				skip(r.ID, SkipEmptyDesired)
				continue
			}
			reason = ReasonUndesired
		}
		if why := tooYoung(r, d); why != "" {
			skip(r.ID, why)
			continue
		}
		pending = append(pending, candidate{r.ID, reason})
	}

	// Deleting a volume only removes blockers of others, so each round
	// plans every volume deletable against the previous round's table.
	volGroup := map[string]int{}
	rows := slices.Collect(t.All())
	cur := t
	for len(pending) > 0 {
		var blocked []candidate
		var plans []Plan
		for _, c := range pending {
			// never the foreign-snapshot opt-in: it is an operator's answer
			// to a per-volume DeleteVolume, not to a bulk collection
			del, err := p.planDelete(cur, c.id, false)
			if err != nil {
				blocked = append(blocked, c)
				continue
			}
			plan := del.plan()
			var acts []ReconcileAction
			for _, op := range plan {
				a := ReconcileAction{Op: op, Target: p.rel(op.Target), Reason: c.reason, Volumes: []string{c.id}}
				switch {
				case del.origin != nil && op.Target == del.origin.Target:
					a.Reason = ReasonReleasedWithVolume
					a.Volumes = append(a.Volumes, del.src)
				case op.Target != del.vol.Target:
					a.Reason = ReasonWithVolume
				}
				acts = append(acts, a)
			}
			why := ""
			for _, a := range acts {
				if why = d.refuses(a); why != "" {
					break
				}
			}
			if why != "" {
				skip(c.id, why)
				continue
			}
			plans = append(plans, plan)
			out.Actions = append(out.Actions, acts...)
			volGroup[c.id] = len(out.groups)
			out.groups = append(out.groups, reconcileGroup{target: c.id, n: len(acts)})
		}
		if len(plans) == 0 {
			break
		}
		rows = simulateDestroyAll(rows, plans)
		cur = NewTable(slices.Clone(rows))
		pending = blocked
	}
	for _, c := range pending {
		if _, err := p.planDelete(cur, c.id, false); err != nil {
			skip(c.id, err.Error())
		}
	}

	// clone-source snapshots whose clone is gone, on volumes that stay.
	// Only ones the endpoint stamped at creation: the reserved name alone is
	// not ownership. Unstamped ones (an administrator's, another tool's, or
	// democratic-csi's, which cuts them bare) are reported, never collected;
	// a legacy one is still released with the clone it serves, where the
	// clone's origin proves it ours (PlanDeleteVolume).
	for r := range cur.All() {
		vol, name, ok := strings.Cut(r.ID, "@")
		if !ok || !strings.HasPrefix(name, cloneSourceSnapshotPrefix) || r.SnapshotName != "" || r.Deferred {
			continue
		}
		if hasAny(cur.ClonesOf(r.Name)) {
			continue // in use
		}
		clone, ours := r.OwnedCloneSource()
		if !ours {
			skip(r.ID, SkipUnstampedCloneSource)
			continue
		}
		if src, ok := cur.Get(vol); !ok || !src.OwnedVolume() {
			skip(r.ID, SkipUnmanagedSource)
			continue
		}
		// a name zfs accepted but the client refuses to address would fail
		// at apply on every run: report it instead
		if _, err := volumeID(clone); err != nil || !zfs.ValidSnapshot(r.Name) {
			skip(r.ID, SkipUnaddressable)
			continue
		}
		if why := tooYoung(r, d); why != "" {
			skip(r.ID, why)
			continue
		}
		// CreateVolume(clone) holds vol:<clone> while cutting it
		a := ReconcileAction{
			Op:     Op{Kind: OpDestroy, Target: r.Name, Defer: true, OkMissing: true},
			Target: r.ID, Reason: ReasonOrphanCloneSource, Volumes: []string{vol, clone},
		}
		if why := d.refuses(a); why != "" {
			skip(r.ID, why)
			continue
		}
		out.Actions = append(out.Actions, a)
		out.groups = append(out.groups, reconcileGroup{target: r.ID, n: 1})
	}

	// a volume planned in a later round waited for its clones: if one of
	// them fails at apply, the volume is not attempted
	for id, g := range volGroup {
		for s := range t.SnapshotsOf(id) {
			for c := range t.ClonesOf(s.Name) {
				if cg, ok := volGroup[c.ID]; ok {
					out.groups[g].deps = append(out.groups[g].deps, cg)
				}
			}
		}
	}
	return out, nil
}

// applyReconcile applies plan group by group. A group that fails is
// reported as skipped with the error, and the run continues: one stuck
// volume (a hold, an open zvol, a clone zfs can see and the scan cannot)
// neither hides what already ran nor starves every volume after it
// (adv4 F3). What ran is returned as Actions; a group is never retried
// within the run. done(g), if set, is called after group g.
func (e *Endpoint) applyReconcile(ctx context.Context, plan ReconcilePlan, done func(g int)) ReconcilePlan {
	out := ReconcilePlan{Skipped: plan.Skipped}
	failed := make([]bool, len(plan.groups))
	i := 0
	for g, grp := range plan.groups {
		acts := plan.Actions[i : i+grp.n]
		i += grp.n
		switch {
		case ctx.Err() != nil:
			failed[g] = true
			out.Skipped = append(out.Skipped, ReconcileSkip{Target: grp.target, Reason: SkipNotAttempted + ": " + ctx.Err().Error()})
		case slices.ContainsFunc(grp.deps, func(d int) bool { return failed[d] }):
			failed[g] = true
			out.Skipped = append(out.Skipped, ReconcileSkip{Target: grp.target, Reason: SkipDependentFailed})
		default:
			n, err := e.applyN(ctx, ReconcilePlan{Actions: acts}.Plan())
			out.Actions = append(out.Actions, acts[:n]...)
			if err != nil {
				failed[g] = true
				out.Skipped = append(out.Skipped, ReconcileSkip{Target: grp.target, Reason: SkipFailed + ": " + err.Error()})
			}
		}
		if done != nil {
			done(g)
		}
	}
	return out
}

func (p Planner) rel(name string) string { return strings.TrimPrefix(name, p.Parent+"/") }

// tooYoung ages a row from its zfs creation or, for a committed volume,
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
	if d.Now.Sub(time.Unix(born, 0)) < d.MinAge {
		return SkipTooYoung
	}
	return ""
}

func hasAny[T any](seq func(func(T) bool)) bool {
	for range seq {
		return true
	}
	return false
}

// simulateDestroyAll applies every destroy of a round's plans to rows the
// way zfs does, in one pass: -r takes a dataset and everything below it; a
// deferred snapshot destroy removes the snapshot once no clone remains, else
// marks it; removing a dataset reaps its origin when that was marked and now
// has no clones.
//
// It is O(rows + ops); applying the plans one at a time was O(rows) per op,
// which made PlanReconcile quadratic (31 s at 16k volumes, half of it under
// the locks). Destroys only remove rows, so reaping once at the end equals
// reaping after each op: the candidates are the same (defer targets and
// origins of removed rows), and a candidate with no clones left at the end
// lost its last clone during the round. TestSimulateDestroyAllEquivalent
// checks it against the sequential version.
func simulateDestroyAll(rows []Row, plans []Plan) []Row {
	exact, rec, deferT := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, plan := range plans {
		for _, op := range plan {
			if op.Kind != OpDestroy {
				continue
			}
			if op.Defer {
				deferT[op.Target] = true
			} else {
				exact[op.Target] = true
			}
			if op.Recursive {
				rec[op.Target] = true
			}
		}
	}
	under := func(name string) bool {
		if len(rec) == 0 {
			return false
		}
		for i := 0; i < len(name); i++ {
			if (name[i] == '@' || name[i] == '/') && rec[name[:i]] {
				return true
			}
		}
		return false
	}
	cand := maps.Clone(deferT)
	rows = slices.DeleteFunc(rows, func(r Row) bool {
		gone := exact[r.Name] || under(r.Name)
		if gone && r.Origin != "" {
			cand[r.Origin] = true
		}
		return gone
	})
	clones := map[string]int{}
	for i, r := range rows {
		if deferT[r.Name] {
			rows[i].Deferred = true
		}
		if r.Origin != "" {
			clones[r.Origin]++
		}
	}
	return slices.DeleteFunc(rows, func(r Row) bool { return cand[r.Name] && r.Deferred && clones[r.Name] == 0 })
}

// ReconcileRequest is what a caller asks a reconcile to keep.
type ReconcileRequest struct {
	DesiredVolumeIDs []string
	// MinAge; 0 means DefaultReconcileMinAge.
	MinAge time.Duration
	// DryRun returns the plan without locking or applying it.
	DryRun bool
}

// DefaultReconcileMinAge protects in-flight provisions and stale desired
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
const MinReconcileMinAge = 10 * time.Minute

// Reconcile plans garbage collection against the caller's desired set and,
// unless DryRun, applies it under the locks of every volume it touches. It
// returns what was (or would be) done and what was left alone.
//
// It scans, locks the planned targets, then re-scans and re-plans under the
// locks: only actions covered by the held locks run, so a CreateVolume that
// finished in between is seen, and anything new waits for the next run.
func (e *Endpoint) Reconcile(ctx context.Context, r ReconcileRequest) (ReconcilePlan, error) {
	for _, id := range r.DesiredVolumeIDs {
		if _, err := volumeID(id); err != nil {
			return ReconcilePlan{}, err
		}
	}
	if r.MinAge != 0 && r.MinAge < MinReconcileMinAge {
		return ReconcilePlan{}, errorf(InvalidArgument, "min age %v is below the floor of %v", r.MinAge, MinReconcileMinAge)
	}
	d := Desired{VolumeIDs: r.DesiredVolumeIDs, MinAge: r.MinAge, Now: e.cfg.Now()}
	if d.MinAge == 0 {
		d.MinAge = DefaultReconcileMinAge
	}
	t, err := e.scan(ctx, "")
	if err != nil {
		return ReconcilePlan{}, err
	}
	plan, err := e.plan.PlanReconcile(t, d)
	if err != nil {
		return ReconcilePlan{}, err
	}
	if r.DryRun || len(plan.Actions) == 0 {
		return plan, nil
	}

	// Try each volume's lock without waiting: a busy one skips only the
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
}
