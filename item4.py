import os
os.chdir('/home/user/empty/.claude/worktrees/agent-a641f1e792d2b8587/internal/endpoint')

def sub(p, a, b, n=1):
    s = open(p).read()
    assert s.count(a) == n, (p, a, s.count(a))
    s = s.replace(a, b)
    open(p, 'w').write(s)

sub('errors_test.go', '''				got, want = opErr(err, i, plan), headApplyErr(err, i, plan)
				// a resumed provision: an earlier attempt changed the pool
				first, _ := planAt(errCell{op: c.op})
				g, w := resumeErr(opErr(err, 0, first)), headResumeErr(headApplyErr(err, 0, first))
''', '''				got, want = opErr(err, i, plan), sinceHead(c, headApplyErr(err, i, plan), " (earlier ops applied; a retry converges)")
				// a resumed provision: an earlier attempt changed the pool
				first, _ := planAt(errCell{op: c.op})
				resumed := errCell{op: c.op, kind: c.kind, mutated: true}
				g, w := resumeErr(opErr(err, 0, first)), sinceHead(resumed, headResumeErr(headApplyErr(err, 0, first)), " (resuming an earlier attempt; a retry converges)")
''')
sub('errors_test.go', '''// changedSinceHead are the cells changed on purpose since 7f5637f. The
// rule is "an unclassified failure after a change is Aborted"; wave 25
// widened it to reads (a scan or get after a change: what a retry must
// finish is the change, whatever op failed).
func changedSinceHead(c errCell) bool {
	return c.mutated && c.kind == errKind(zfs.KindUnknown) && (c.op == opRead || c.op == opReadParent)
}''', '''// changedSinceHead are the cells changed on purpose since 7f5637f. The
// rule is "an unclassified failure after a change is Aborted"; wave 25
// widened it to reads (a scan or get after a change: what a retry must
// finish is the change, whatever op failed), and made no space after a
// change Aborted (ResourceExhausted is final for external-provisioner
// under Immediate binding, and strands the half-made volume).
func changedSinceHead(c errCell) bool {
	if !c.mutated {
		return false
	}
	switch c.kind {
	case errKind(zfs.KindUnknown):
		return c.op == opRead || c.op == opReadParent
	case errKind(zfs.KindNoSpace):
		return slicesContains(mutatingOps, c.op)
	}
	return false
}''')
sub('errors_test.go', '''		{opRead, &zfs.Error{Kind: zfs.KindNoSpace}, false, ResourceExhausted},
''', '''		{opRead, &zfs.Error{Kind: zfs.KindNoSpace}, false, ResourceExhausted},
		{OpCreateVol, &zfs.Error{Kind: zfs.KindNoSpace}, false, ResourceExhausted},
		// no space after a change: the clone a reservation failed on
		{OpSet, &zfs.Error{Kind: zfs.KindNoSpace}, true, Aborted},
''')
