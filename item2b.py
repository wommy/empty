import os
os.chdir('/home/user/empty/.claude/worktrees/agent-a641f1e792d2b8587/internal/endpoint')

def sub(p, a, b, n=1):
    s = open(p).read()
    assert s.count(a) == n, (p, a, s.count(a))
    s = s.replace(a, b)
    open(p, 'w').write(s)

sub('endpoint.go', '''	// A create plan that leaves its commit to a re-scan (a filesystem
	// whose mount only a scan can confirm) takes a second pass: the
	// resume, which commits or says why not. Two passes at most.
	for pass := 0; ; pass++ {
		t, missing, err := e.scanForUnproven(ctx, id, r.SourceVolumeID, r.SourceSnapshotID)
		if err != nil {
			return Volume{}, err
		}
''', '''	// A create plan that leaves its commit to a re-scan (a filesystem
	// whose mount only a scan can confirm) takes a second pass: the
	// resume, which commits or says why not. Two passes at most.
	//
	// Once the volume is changed (a pass applied an op, or the scan found
	// it uncommitted: an earlier attempt's), every failure goes through
	// resumeErr, reads included: an unclassified one is Aborted, as the
	// error table has it after a change, since a final code strands the
	// half-made volume. What is refused on purpose keeps its code.
	changed := false
	fail := func(err error) (Volume, error) {
		if changed {
			return Volume{}, resumeErr(err)
		}
		return Volume{}, err
	}
	for pass := 0; ; pass++ {
		t, missing, err := e.scanForUnproven(ctx, id, r.SourceVolumeID, r.SourceSnapshotID)
		if err != nil {
			return fail(err)
		}
		if row, ok := t.Get(id); ok && !row.Committed {
			changed = true
		}
''')
sub('endpoint.go', '''			if p.parentMount, err = e.parentMountpoint(ctx); err != nil {
				return Volume{}, err
			}
			missing = nil
		}
		if missing != nil {
			// nothing read so far proves the parent: absent is not yet
			// "create" (nor a source NotFound)
			if err := e.proveParent(ctx, missing); err != nil {
				return Volume{}, err
			}
		}
		plan, err := p.PlanCreateVolume(t, r)
		if err != nil {
			return Volume{}, err
		}
		if err := e.headroom(ctx, t, plan, "create volume "+id); err != nil {
			return Volume{}, err
		}
		commit := Plan(nil)
		if r.NFS != nil && p.commits(plan) {
			// an NFS volume commits only once it is served (verifyExport)
			plan, commit = plan[:len(plan)-1], plan[len(plan)-1:]
		}
		if err := e.apply(ctx, plan); err != nil {
			if row, ok := t.Get(id); ok && !row.Committed {
				return Volume{}, resumeErr(err)
			}
			return Volume{}, err
		}
		if commit != nil {
			if err := e.verifyExport(ctx, id); err != nil {
				return Volume{}, err
			}
			if err := e.apply(ctx, commit); err != nil {
				return Volume{}, resumeErr(err)
			}
		}
		if pass == 0 && len(plan)+len(commit) == 0 {
			return e.servedFrom(ctx, t, id) // a compatible repeat
		}
		if pass > 0 || commit != nil || p.commits(plan) {
			break
		}
	}
	return e.readVolume(ctx, id)
}''', '''			if p.parentMount, err = e.parentMountpoint(ctx); err != nil {
				return fail(err)
			}
			missing = nil
		}
		if missing != nil {
			// nothing read so far proves the parent: absent is not yet
			// "create" (nor a source NotFound)
			if err := e.proveParent(ctx, missing); err != nil {
				return fail(err)
			}
		}
		plan, err := p.PlanCreateVolume(t, r)
		if err != nil {
			return fail(err)
		}
		if err := e.headroom(ctx, t, plan, "create volume "+id); err != nil {
			return fail(err)
		}
		commit := Plan(nil)
		if r.NFS != nil && p.commits(plan) {
			// an NFS volume commits only once it is served (verifyExport)
			plan, commit = plan[:len(plan)-1], plan[len(plan)-1:]
		}
		if err := e.apply(ctx, plan); err != nil {
			return fail(err)
		}
		changed = changed || len(plan) > 0
		if commit != nil {
			if err := e.verifyExport(ctx, id); err != nil {
				return fail(err)
			}
			if err := e.apply(ctx, commit); err != nil {
				return Volume{}, resumeErr(err)
			}
		}
		if pass == 0 && len(plan)+len(commit) == 0 {
			v, err := e.servedFrom(ctx, t, id) // a compatible repeat
			if err != nil {
				return fail(err)
			}
			return v, nil
		}
		if pass > 0 || commit != nil || p.commits(plan) {
			break
		}
	}
	v, err := e.readVolume(ctx, id)
	if err != nil {
		// committed or not, this attempt changed the volume (an empty
		// plan in both passes returns above)
		changed = true
		return fail(err)
	}
	return v, nil
}''')
sub('endpoint.go', '''// resumeErr maps the failure of a resumed provision. An earlier attempt
// already changed the volume, so an unclassified zfs failure is Aborted
// even on the resume's first op, as it was on the attempt that left the
// row (opErr): a final Internal would strand the half-made volume, and
// flip the code a caller saw for the same state.
func resumeErr(err error) error {
	return remapMutated(err, " (resuming an earlier attempt; a retry converges)")
}''', '''// resumeErr maps the failure of a resumed provision. An earlier attempt
// already changed the volume, so an unclassified zfs failure is Aborted
// even on the resume's first op, as it was on the attempt that left the
// row (opErr): a final Internal would strand the half-made volume, and
// flip the code a caller saw for the same state. A read is keyed the same
// way (the error table's after-change row covers every op), and an
// Internal that is no zfs failure at all (a scan's output that does not
// decode) is unclassified too: Aborted. Every other code is a refusal
// made on purpose and is kept.
func resumeErr(err error) error {
	const because = " (resuming an earlier attempt; a retry converges)"
	err = remapMutated(err, because)
	if err != nil && CodeOf(err) == Internal {
		return &Error{Code: Aborted, Msg: "unclassified failure" + because, Err: err}
	}
	return err
}''')
