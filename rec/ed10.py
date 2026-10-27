import os
os.chdir('/home/user/empty/.claude/worktrees/agent-ad45c3ab7d0b822f7')
def sub(p, pairs):
    s=open(p).read()
    for old,new in pairs:
        assert s.count(old)==1, (p, old)
        s=s.replace(old,new)
    open(p,'w').write(s)

sub('internal/endpoint/apply.go', [
('''func (e *Endpoint) apply(ctx context.Context, plan Plan) error {
	// an unknown kind is a planner bug: refuse the plan before any op runs,
	// rather than half-apply it or skip the op as if it were done
	for i, op := range plan {
		if _, ok := opNames[op.Kind]; !ok {
			return &Error{Code: Internal, Msg: fmt.Sprintf("op %d/%d: unknown kind %v on %s", i+1, len(plan), op.Kind, op.Target)}
		}
	}''','''func (e *Endpoint) apply(ctx context.Context, plan Plan) error {
	_, err := e.applyN(ctx, plan)
	return err
}

// applyN is apply that also says how many ops completed before an error.
func (e *Endpoint) applyN(ctx context.Context, plan Plan) (int, error) {
	// an unknown kind is a planner bug: refuse the plan before any op runs,
	// rather than half-apply it or skip the op as if it were done
	for i, op := range plan {
		if _, ok := opNames[op.Kind]; !ok {
			return 0, &Error{Code: Internal, Msg: fmt.Sprintf("op %d/%d: unknown kind %v on %s", i+1, len(plan), op.Kind, op.Target)}
		}
	}'''),
('''		default:
			// unreachable after the check above; never treat it as done
			return &Error{Code: Internal, Msg: "unknown op kind " + op.Kind.String()}
		}''','''		default:
			// unreachable after the check above; never treat it as done
			return i, &Error{Code: Internal, Msg: "unknown op kind " + op.Kind.String()}
		}'''),
('''		case op.Kind == OpClone && zfs.IsNotFound(err):
			return &Error{Code: NotFound, Msg: "clone source " + op.Source + " not found", Err: err}
		default:
			return opErr(err, i, plan)
		}
	}
	return nil
}''','''		case op.Kind == OpClone && zfs.IsNotFound(err):
			return i, &Error{Code: NotFound, Msg: "clone source " + op.Source + " not found", Err: err}
		default:
			return i, opErr(err, i, plan)
		}
	}
	return len(plan), nil
}'''),
])

sub('internal/endpoint/reconcile.go', [
('''type ReconcilePlan struct {
	Actions []ReconcileAction
	Skipped []ReconcileSkip
}''','''type ReconcilePlan struct {
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
}'''),
('''			plans = append(plans, plan)
			out.Actions = append(out.Actions, acts...)
		}''','''			plans = append(plans, plan)
			out.Actions = append(out.Actions, acts...)
			volGroup[c.id] = len(out.groups)
			out.groups = append(out.groups, reconcileGroup{target: c.id, n: len(acts)})
		}'''),
('''	// Deleting a volume only removes blockers of others, so each round
	// plans every volume deletable against the previous round's table.
	rows := slices.Collect(t.All())''','''	// Deleting a volume only removes blockers of others, so each round
	// plans every volume deletable against the previous round's table.
	volGroup := map[string]int{}
	rows := slices.Collect(t.All())'''),
('''		if !d.allows(a) {
			skip(r.ID, SkipChangedSinceScan)
			continue
		}
		out.Actions = append(out.Actions, a)
	}
	return out, nil
}''','''		if !d.allows(a) {
			skip(r.ID, SkipChangedSinceScan)
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
}'''),
('''	if plan, err = e.plan.PlanReconcile(t, d); err != nil {
		return ReconcilePlan{}, err
	}
	if err := e.apply(ctx, plan.Plan()); err != nil {
		return ReconcilePlan{}, err
	}
	return plan, nil
}''','''	if plan, err = e.plan.PlanReconcile(t, d); err != nil {
		return ReconcilePlan{}, err
	}
	return e.applyReconcile(ctx, plan, nil), nil
}'''),
('''	SkipUnaddressable        = "name the endpoint cannot address"''','''	SkipUnaddressable        = "name the endpoint cannot address"
	// SkipFailed prefixes the error of a group whose apply failed (what
	// of it ran is in Actions); the run went on with the next group.
	SkipFailed = "failed"
	// SkipDependentFailed: a volume whose clone, collected in the same
	// run, failed; it is not attempted.
	SkipDependentFailed = "not attempted: a clone collected first failed"
	SkipNotAttempted    = "not attempted"'''),
])
