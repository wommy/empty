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
)

// Reasons a reconcile leaves something alone (besides PlanDeleteVolume's
// refusals: CSI snapshots and dependent clones).
const (
	SkipTooYoung        = "younger than the minimum age"
	SkipNoCreation      = "creation time unknown"
	SkipEmptyDesired    = "desired set is empty: undesired volumes are not collected"
	SkipDesiredMissing  = "desired volume does not exist"
	SkipUnmanagedSource = "clone-source snapshot on an unmanaged volume"
	// SkipUnstampedCloneSource: a snapshot with the reserved clone-source
	// name but without the endpoint's stamp (managed_resource and
	// clone_source_for set locally at creation) is not the endpoint's.
	SkipUnstampedCloneSource = "clone-source name without the endpoint's stamp: not ours"
	SkipUnaddressable        = "name the endpoint cannot address"
	SkipChangedSinceScan     = "appeared after the targets were locked; next reconcile"
)

// Desired is the caller's statement of what to keep.
type Desired struct {
	// VolumeIDs is the complete set of volumes the caller wants. Every owned
	// volume outside it is undesired. Empty means "no statement": only
	// uncommitted rows and clone-source snapshots are collected, because an
	// empty set cannot be told apart from a caller that sent nothing.
	VolumeIDs []string
	// MinAge: nothing created less than MinAge before Now is touched. It
	// keeps in-flight provisions and volumes created after the caller took
	// its desired set out of reach.
	MinAge time.Duration
	Now    time.Time

	// held, when non-nil, limits actions to ones whose volumes are all in
	// it (the locks Reconcile holds); the rest are skipped and their
	// volumes stay, so nothing that depends on them is planned either.
	held map[string]bool
}

func (d Desired) covers(vols []string) bool {
	return d.held == nil || !slices.ContainsFunc(vols, func(v string) bool { return !d.held[v] })
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
	for _, id := range slices.Sorted(maps.Keys(want)) {
		if r, ok := t.Get(id); !ok || r.Type == zfs.TypeSnapshot {
			skip(id, SkipDesiredMissing)
		}
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
			if len(want) == 0 {
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
	rows := slices.Collect(t.All())
	cur := t
	for len(pending) > 0 {
		var blocked []candidate
		var plans []Plan
		for _, c := range pending {
			plan, err := p.PlanDeleteVolume(cur, c.id)
			if err != nil {
				blocked = append(blocked, c)
				continue
			}
			var acts []ReconcileAction
			for i, op := range plan {
				a := ReconcileAction{Op: op, Target: p.rel(op.Target), Reason: c.reason, Volumes: []string{c.id}}
				if i > 0 { // the released clone-source origin
					a.Reason = ReasonReleasedWithVolume
					src, _, _ := strings.Cut(a.Target, "@")
					a.Volumes = append(a.Volumes, src)
				}
				if !d.covers(a.Volumes) {
					acts = nil
					break
				}
				acts = append(acts, a)
			}
			if acts == nil {
				skip(c.id, SkipChangedSinceScan)
				continue
			}
			plans = append(plans, plan)
			out.Actions = append(out.Actions, acts...)
		}
		if len(plans) == 0 {
			break
		}
		rows = simulateDestroyAll(rows, plans)
		cur = NewTable(slices.Clone(rows))
		pending = blocked
	}
	for _, c := range pending {
		if _, err := p.PlanDeleteVolume(cur, c.id); err != nil {
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
		vols := []string{vol, clone}
		if !d.covers(vols) {
			skip(r.ID, SkipChangedSinceScan)
			continue
		}
		out.Actions = append(out.Actions, ReconcileAction{
			Op:     Op{Kind: OpDestroy, Target: r.Name, Defer: true, OkMissing: true},
			Target: r.ID, Reason: ReasonOrphanCloneSource, Volumes: vols,
		})
	}
	return out, nil
}

func (p Planner) rel(name string) string { return strings.TrimPrefix(name, p.Parent+"/") }

func tooYoung(r Row, d Desired) string {
	if r.Creation <= 0 {
		return SkipNoCreation
	}
	if d.Now.Sub(time.Unix(r.Creation, 0)) < d.MinAge {
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
	d := Desired{VolumeIDs: r.DesiredVolumeIDs, MinAge: r.MinAge, Now: e.cfg.Now()}
	if d.MinAge <= 0 {
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

	held := map[string]bool{}
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
	d.Now, d.held = e.cfg.Now(), held
	if plan, err = e.plan.PlanReconcile(t, d); err != nil {
		return ReconcilePlan{}, err
	}
	if err := e.apply(ctx, plan.Plan()); err != nil {
		return ReconcilePlan{}, err
	}
	return plan, nil
}
