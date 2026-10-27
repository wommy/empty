import os
os.chdir('/home/user/empty/.claude/worktrees/agent-ad45c3ab7d0b822f7')
def sub(p, pairs):
    s=open(p).read()
    for old,new in pairs:
        assert s.count(old)==1, (p, old)
        s=s.replace(old,new)
    open(p,'w').write(s)

p='internal/endpoint/plan.go'
s=open(p).read()
i=s.index('// PlanDeleteVolume: missing is done')
j=s.index('// releasedOrigin is the internal')
s=s[:i]+'''// PlanDeleteVolume: missing is done; unmanaged, snapshotted (like the
// baseline: CSI snapshots must outlive the call or it fails unchanged) or
// cloned volumes are refused; an internal clone-source origin is released.
// A volume with snapshots the endpoint did not cut is refused too, unless
// DestroyForeignSnapshots (ADR 0003).
//
// Never `destroy -r`: the volume's own snapshots are destroyed one by one,
// then the volume plainly, so zfs itself refuses ("has children") on
// anything the scan cannot see, such as a dataset someone created below a
// filesystem volume, rather than taking it along.
func (p Planner) PlanDeleteVolume(t Table, volID string) (Plan, error) {
	d, err := p.planDelete(t, volID, p.DestroyForeignSnapshots)
	return d.plan(), err
}

// deletion is a volume delete as parts: the clone-source origin it
// releases, the volume's snapshots, and the volume (plan gives the order).
type deletion struct {
	snaps  Plan
	vol    *Op
	origin *Op
	src    string // the volume the origin hangs off
}

func (d deletion) plan() Plan {
	if d.vol == nil {
		return nil
	}
	// 1. the released origin first, deferred: zfs keeps it while the clone
	//    lives and reaps it with the clone, so no crash point leaves an
	//    orphan that a retry (volume row gone) could no longer see
	// 2. exactly the snapshots this plan judged, then the dataset without
	//    -r: a snapshot or child made since the scan (sanoid, an admin)
	//    fails the last op ("has children") instead of going with it
	var plan Plan
	if d.origin != nil {
		plan = append(plan, *d.origin)
	}
	plan = append(plan, d.snaps...)
	return append(plan, *d.vol)
}

// planDelete is PlanDeleteVolume with the foreign-snapshot opt-in explicit:
// Reconcile never passes it (no per-volume request asked for anything).
func (p Planner) planDelete(t Table, volID string, destroyForeign bool) (deletion, error) {
	id, err := issuedVolumeID(volID)
	if err != nil {
		return deletion{}, err
	}
	if err := t.mustCover(id); err != nil {
		return deletion{}, err
	}
	row, ok := t.Get(id)
	if !ok {
		return deletion{}, nil
	}
	if !row.OwnedVolume() {
		return deletion{}, errorf(FailedPrecondition, "dataset %s is not managed", row.Name)
	}
	var d deletion
	var foreign []string
	for s := range t.SnapshotsOf(id) {
		if s.SnapshotName != "" && !s.Deferred {
			return deletion{}, errorf(FailedPrecondition, "volume %s has snapshot %s", id, s.ID)
		}
		for c := range t.ClonesOf(s.Name) {
			return deletion{}, errorf(FailedPrecondition, "volume %s has dependent clone %s", id, c.ID)
		}
		// the endpoint's own: an internal clone-source snapshot, or one
		// already pending deferred destruction (a deleted CSI snapshot)
		if _, ours := s.OwnedCloneSource(); !ours && !s.Deferred {
			foreign = append(foreign, s.ID)
		}
		// plain, never -d: a snapshot still in use must make zfs refuse
		d.snaps = append(d.snaps, Op{Kind: OpDestroy, Target: s.Name, OkMissing: true})
	}
	if len(foreign) > 0 && !destroyForeign {
		return deletion{}, errorf(FailedPrecondition, "volume %s has snapshots the endpoint did not make: %s (delete them, or run with -destroy-foreign-snapshots)",
			id, strings.Join(foreign, ", "))
	}
	d.vol = &Op{Kind: OpDestroy, Target: row.Name, OkMissing: true}
	if origin, src, ok := p.releasedOrigin(t, row); ok {
		d.origin = &Op{Kind: OpDestroy, Target: origin.Name, Defer: true, OkMissing: true}
		d.src = src
	}
	return d, nil
}

'''+s[j:]
s=s.replace('''import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
''','''import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
''')
open(p,'w').write(s)

sub('internal/endpoint/reconcile.go', [
('''		for _, c := range pending {
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
					row, _ := cur.Get(c.id)
					_, src, _ := p.releasedOrigin(cur, row)
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
		}''','''		for _, c := range pending {
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
			if !slices.ContainsFunc(acts, func(a ReconcileAction) bool { return d.covers(a.Volumes) }) ||
				slices.ContainsFunc(acts, func(a ReconcileAction) bool { return !d.covers(a.Volumes) }) {
				skip(c.id, SkipChangedSinceScan)
				continue
			}
			plans = append(plans, plan)
			out.Actions = append(out.Actions, acts...)
		}'''),
('''	for _, c := range pending {
		if _, err := p.PlanDeleteVolume(cur, c.id); err != nil {
			skip(c.id, err.Error())
		}
	}''','''	for _, c := range pending {
		if _, err := p.planDelete(cur, c.id, false); err != nil {
			skip(c.id, err.Error())
		}
	}'''),
('''	ReasonReleasedWithVolume = "clone source of a collected volume"''','''	ReasonReleasedWithVolume = "clone source of a collected volume"
	ReasonWithVolume         = "the endpoint's snapshot of a collected volume"'''),
])
