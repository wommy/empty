import sys, re
root = sys.argv[1]

def sub(path, old, new):
    p = root + '/' + path
    s = open(p).read()
    if old not in s:
        raise SystemExit('missing in %s: %s' % (path, old[:100]))
    s = s.replace(old, new, 1)
    open(p, 'w').write(s)

f = 'internal/endpoint/export_contract_test.go'
sub(f, '''// D1 (wave9 nfs-diff): Publish re-checks the table after applying. A line
// of another exports file for the volume's path serves it beyond the
// request whatever sharenfs says, so Publish fails instead of reporting
// success.''', '''// D1 (wave9 nfs-diff): a line of another exports file for the volume's
// path serves it beyond the request whatever sharenfs says, so Publish
// fails instead of reporting success. A refused Publish changes nothing
// (wave13 verifier): the requested client is not left in sharenfs, nor
// served, whether the refusal comes before the change (a local value) or
// after it (an inherited one, put back).''')
sub(f, '''	foreignLine = mp + "\\t*(rw,sync,no_root_squash,sec=sys,rw)\\n"
	_, err = ep.PublishFileExport(ctx, "pvc-d1", "10.0.0.1", false)
	wantCode(t, err, endpoint.FailedPrecondition)
	if !strings.Contains(err.Error(), "beyond its client list") {
		t.Fatalf("unclear error: %v", err)
	}
	foreignLine = ""
	if _, err := ep.PublishFileExport(ctx, "pvc-d1", "10.0.0.1", false); err != nil {
		t.Fatalf("publish once the foreign line is gone: %v", err)
	}
}''', '''	refused := func(host, sharenfs, source string) {
		t.Helper()
		foreignLine = mp + "\\t*(rw,sync,no_root_squash,sec=sys,rw)\\n"
		_, err := ep.PublishFileExport(ctx, "pvc-d1", host, false)
		wantCode(t, err, endpoint.FailedPrecondition)
		if !strings.Contains(err.Error(), "beyond its client list") {
			t.Fatalf("unclear error: %v", err)
		}
		if val, src := h.propSource(t, v.Dataset, "sharenfs"); val != sharenfs || src != source {
			t.Fatalf("refused publish of %s left sharenfs %q (%s), was %q (%s)", host, val, src, sharenfs, source)
		}
		if _, ok := h.served(t, v.Mountpoint, host); ok {
			t.Fatalf("refused publish of %s left it served", host)
		}
		foreignLine = ""
	}
	refused("10.0.0.1", "off", "local")
	if _, err := ep.PublishFileExport(ctx, "pvc-d1", "10.0.0.1", false); err != nil {
		t.Fatalf("publish once the foreign line is gone: %v", err)
	}
	refused("10.0.0.2", "rw=10.0.0.1/32", "local")
	// inherited (the parent is off): the planner cannot tell the parent's
	// lines from someone else's, so the check after the change refuses,
	// and inheritance is put back
	if err := h.Z.Inherit(ctx, v.Dataset, "sharenfs"); err != nil {
		t.Fatal(err)
	}
	refused("10.0.0.3", "off", "inherited from "+h.Parent)
}''')

f = 'internal/endpoint/export_test.go'
s = open(root + '/' + f).read()
for name in ['served in the other mode', 'served with no mode', 'served only under a name', 'served with no_root_squash', 'served to more clients']:
    i = s.index('{name: "%s: reshare"' % name)
    j = s.index('},\n', s.index('want: set(', i)) + 3
    row = s[i:j]
    row = row.replace(': reshare"', ': refused before any change"')
    row = re.sub(r'want: set\(map\[string\]string\{"sharenfs": "[^"]*"\}\)\}', 'code: FailedPrecondition, beyond: true}', row)
    s = s[:i] + row + s[j:]
s = s.replace('''		// D2: served, but beyond the request: re-share once (the caller
		// reports what a re-share cannot fix)''', '''		// D2: served, but beyond the request: refused before anything
		// changes (ErrServedBeyond; the endpoint re-shares the unchanged
		// value once and plans again, so a stale table still converges)''')
s = s.replace('''		want Plan
		code Code
	}{
		{name: "first client", row: fsv("a", nil), c: c1,''', '''		want Plan
		code Code
		// beyond: refused as ErrServedBeyond
		beyond bool
	}{
		{name: "first client", row: fsv("a", nil), c: c1,''')
s = s.replace('''			plan, row, err := pl.PlanPublishExport(NewTable([]Row{tc.row}), tc.live, "a", tc.c)
			if CodeOf(err) != tc.code {
				t.Fatalf("err %v, want code %d", err, tc.code)
			}''', '''			plan, row, err := pl.PlanPublishExport(NewTable([]Row{tc.row}), tc.live, "a", tc.c)
			if CodeOf(err) != tc.code {
				t.Fatalf("err %v, want code %d", err, tc.code)
			}
			if errors.Is(err, ErrServedBeyond) != tc.beyond {
				t.Fatalf("err %v: served beyond = %v, want %v", err, !tc.beyond, tc.beyond)
			}''')
s = s.replace('import (\n\t"fmt"', 'import (\n\t"errors"\n\t"fmt"')
open(root + '/' + f, 'w').write(s)
