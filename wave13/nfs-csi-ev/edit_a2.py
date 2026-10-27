import sys
root = sys.argv[1]

def sub(path, old, new, count=1):
    p = root + '/' + path
    s = open(p).read()
    if old not in s:
        raise SystemExit('missing in %s: %s' % (path, old[:80]))
    s = s.replace(old, new, count)
    open(p, 'w').write(s)

f = 'internal/endpoint/export_test.go'
sub(f, '''		{name: "drifted sharenfs is rewritten", row: exported("a", "10.0.0.1/32=rw", "rw=10.0.0.1/32:10.6.6.6/32"), live: live1, c: c1,
			want: set(map[string]string{"sharenfs": "rw=10.0.0.1/32"})},''', '''		// an edit inside the grammar is accepted as truth (ADR 0004): the
		// stored list follows it, sharenfs is not rewritten
		{name: "in-grammar edit is accepted", row: exported("a", "10.0.0.1/32=rw", "rw=10.0.0.1/32:10.6.6.6/32"), live: live1, c: c1,
			want: set(map[string]string{clientsProp: "10.0.0.1/32=rw,10.6.6.6/32=rw"})},''')

def uninherit(enc, share):
    return '''func() Row {
				r := exported("a", "%s", "%s")
				r.ShareNFSLocal = false
				return r
			}()''' % (enc, share)

# the stored list is read only when sharenfs is not canonical: an inherited one
sub(f, '''		{name: "corrupt client list", c: c1, code: FailedPrecondition, row: exported("a", "x=yes", "off")},
		{name: "duplicate client list", c: c1, code: FailedPrecondition, row: exported("a", "10.0.0.1/32=rw,10.0.0.1/32=rw", "rw=10.0.0.1/32")},''',
    '''		// the stored list is read only when sharenfs is not canonical
		{name: "corrupt client list", c: c1, code: FailedPrecondition, row: %s},
		{name: "duplicate client list", c: c1, code: FailedPrecondition, row: %s},''' % (uninherit("x=yes", "off"), uninherit("10.0.0.1/32=rw,10.0.0.1/32=rw", "rw=10.0.0.1/32")))
sub(f, '''		{name: "corrupt", host: "10.0.0.1", row: exported("a", "x", "off"), code: FailedPrecondition},''',
    '''		{name: "corrupt", host: "10.0.0.1", row: %s, code: FailedPrecondition},''' % uninherit("x", "off"))
