import os
os.chdir('/home/user/empty/.claude/worktrees/agent-a641f1e792d2b8587/internal/endpoint')

def sub(p, a, b, n=1):
    s = open(p).read()
    assert s.count(a) == n, (p, a, s.count(a))
    s = s.replace(a, b)
    open(p, 'w').write(s)

# errors_test.go: the row, and the pin's deliberate deviations
sub('errors_test.go',
'''		{opRead, &zfs.Error{Kind: zfs.KindUnknown}, true, Internal}, // a read changes nothing
''',
'''		// a read after a change: the change is what a retry must finish
		{opRead, &zfs.Error{Kind: zfs.KindUnknown}, true, Aborted},
		{opReadParent, &zfs.Error{Kind: zfs.KindUnknown}, true, Aborted},
''')
sub('errors_test.go',
'''				got, want = zfsFail(opRead, err, c.mutated, "scan tank"), headZfsErr(err, "scan tank")
''',
'''				got, want = zfsFail(opRead, err, c.mutated, "scan tank"), sinceHead(c, headZfsErr(err, "scan tank"), "")
''')
sub('errors_test.go',
'''				got, want = zfsFail(opReadParent, err, c.mutated, "scan tank"), headReadParent(err, "scan tank")
''',
'''				got, want = zfsFail(opReadParent, err, c.mutated, "scan tank"), sinceHead(c, headReadParent(err, "scan tank"), "")
''')
sub('errors_test.go',
'''// headCode is the code HEAD gave the cell c.
func headCode(c errCell) Code {
	err := errsOfKind(c.kind)[0]
''',
'''// changedSinceHead are the cells changed on purpose since 7f5637f. The
// rule is "an unclassified failure after a change is Aborted"; wave 25
// widened it to reads (a scan or get after a change: what a retry must
// finish is the change, whatever op failed).
func changedSinceHead(c errCell) bool {
	return c.mutated && c.kind == errKind(zfs.KindUnknown) && (c.op == opRead || c.op == opReadParent)
}

// sinceHead is the head mapping err of the cell c, amended where c changed
// on purpose since (changedSinceHead): Aborted, its message extended by
// because, as remapMutated does.
func sinceHead(c errCell, err error, because string) error {
	if !changedSinceHead(c) {
		return err
	}
	e := err.(*Error)
	return &Error{Code: Aborted, Msg: e.Msg + because, Err: e.Err}
}

// headCode is the code HEAD gave the cell c, amended by changedSinceHead.
func headCode(c errCell) Code {
	if changedSinceHead(c) {
		return Aborted
	}
	err := errsOfKind(c.kind)[0]
''')

# errors.go: the rule covers reads
sub('errors.go',
'''// DeleteVolume and
// DeleteSnapshot report as FAILED_PRECONDITION. An unclassified failure
// after a change is Aborted, not Internal: a retry re-scans and converges,
// while a final code would strand the half-applied plan
// (external-provisioner never deletes what it never got). Before any
// change it stays Internal.''',
'''// DeleteVolume and
// DeleteSnapshot report as FAILED_PRECONDITION. An unclassified failure
// after a change is Aborted, not Internal: a retry re-scans and converges,
// while a final code would strand the half-applied plan
// (external-provisioner never deletes what it never got). That holds for a
// read too (a scan or get after the change, opRead mutated): the change
// is what the retry must finish, whatever op failed. Before any change it
// stays Internal.''')
sub('errors.go',
'''	{ops: mutatingOps, kinds: kinds(zfs.KindUnknown), after: true, code: Aborted},
''',
'''	{kinds: kinds(zfs.KindUnknown), after: true, code: Aborted},
''')
