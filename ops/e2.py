import sys
root = sys.argv[1]

def sub(path, pairs):
    p = root + '/' + path
    s = open(p).read()
    for old, new in pairs:
        assert old in s, (path, old[:80])
        s = s.replace(old, new, 1)
    open(p, 'w').write(s)

sub('deploy/systemd/zfs-endpoint-reconcile@.service', [
('''# ADR 0001 D3). The desired set is empty, which collects no committed
# volume. A failed call (endpoint down, admin socket missing) fails the
# unit.
#''', '''# ADR 0001 D3). The desired set is empty, which collects no committed
# volume. A failed call (endpoint down, admin socket missing) fails the
# unit with exit 1.
#
# -fail-on-findings: a report whose "summary:" line counts a finding (an
# abandoned create, an orphan clone source, an uncommitted create still
# exported, drift; not young or busy volumes) exits 3, so the unit shows
# as failed in `systemctl --failed` and can drive OnFailure=. The
# endpoint's own journal carries the same counts as one JSON
# "reconcile result" line.
#'''),
('''ExecStart=/usr/local/sbin/zfs-endpoint reconcile -parent %I -timeout 5m
''', '''ExecStart=/usr/local/sbin/zfs-endpoint reconcile -parent %I -timeout 5m -fail-on-findings
'''),
('''#   ExecStart=/usr/local/sbin/zfs-endpoint reconcile -admin-socket unix:///run/zfs-endpoint/admin/vols/endpoint.sock -timeout 5m''',
'''#   ExecStart=/usr/local/sbin/zfs-endpoint reconcile -admin-socket unix:///run/zfs-endpoint/admin/vols/endpoint.sock -timeout 5m -fail-on-findings'''),
])

sub('deploy/reconcile_timer_test.go', [
('''	if v, ok := flagValue(args, "-parent"); !ok || v != "%I" {
		t.Errorf("-parent %q, want %%I (the endpoint unit's instance)", v)
	}''', '''	if v, ok := flagValue(args, "-parent"); !ok || v != "%I" {
		t.Errorf("-parent %q, want %%I (the endpoint unit's instance)", v)
	}
	// a report with findings fails the unit (exit 3), so it is seen
	if !slices.Contains(args, "-fail-on-findings") {
		t.Errorf("ExecStart=%q lacks -fail-on-findings: a report with findings would exit 0 unseen", args)
	}'''),
('''	if got := timerForbidden([]string{"zfs-endpoint", "reconcile", "-parent", "%I", "-timeout", "5m"}); got != nil {''',
'''	if got := timerForbidden([]string{"zfs-endpoint", "reconcile", "-parent", "%I", "-timeout", "5m", "-fail-on-findings"}); got != nil {'''),
])
