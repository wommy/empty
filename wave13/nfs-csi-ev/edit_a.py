import sys
root = sys.argv[1]

def sub(path, old, new, count=1):
    p = root + '/' + path
    s = open(p).read()
    if old not in s:
        raise SystemExit('missing in %s: %s' % (path, old[:80]))
    s = s.replace(old, new, count)
    open(p, 'w').write(s)

sub('internal/endpoint/nfs_diff_test.go', '''		share := ShareNFS(cs)
		if err := nfsref.ValidateSharenfs(share); err != nil {''', '''		share := ShareNFS(cs)
		// every list the stored form held is in sharenfs, losslessly: the
		// canonical grammar reads it back (ADR 0004 drops the stored form)
		if back, ok := ParseShareNFS(share); !ok || !slices.Equal(back, cs) {
			t.Fatalf("sharenfs %q of %v reads back as %v, canonical=%v", share, cs, back, ok)
		}
		if err := nfsref.ValidateSharenfs(share); err != nil {''')

sub('internal/endpoint/export_test.go', '''// F14: the order is total,''', '''// ADR 0004, csi-nfs-design §2: the canonical grammar. Exactly the values
// ShareNFS writes parse; anything else (options, names, "*", empty hosts,
// overlaps, another order) is someone else's.
func TestParseShareNFS(t *testing.T) {
	t.Parallel()
	ok := map[string][]NFSClient{
		"off":                           nil,
		"rw=10.0.0.1/32":                {{Host: "10.0.0.1/32"}},
		"ro=10.0.0.0/16":                {{Host: "10.0.0.0/16", ReadOnly: true}},
		"rw=10.9.0.5/32:10.10.0.0/16":   {{Host: "10.9.0.5/32"}, {Host: "10.10.0.0/16"}},
		"rw=10.0.0.1/32,ro=10.1.0.0/16": {{Host: "10.0.0.1/32"}, {Host: "10.1.0.0/16", ReadOnly: true}},
		"rw=10.0.0.0/8":                 {{Host: "10.0.0.0/8"}}, // below the default floor: still ours
	}
	for s, want := range ok {
		got, canonical := ParseShareNFS(s)
		if !canonical || !reflect.DeepEqual(got, want) {
			t.Errorf("ParseShareNFS(%q) = %v, %v; want %v", s, got, canonical, want)
		}
		if ShareNFS(got) != s {
			t.Errorf("ShareNFS(ParseShareNFS(%q)) = %q", s, ShareNFS(got))
		}
	}
	for _, s := range []string{
		"", "on", "rw", "ro", "rw=", "rw=*", "rw=10.0.0.0/24:", "rw=:10.0.0.0/24", "rw=10.0.0.0/24::10.1.0.0/24",
		"rw=10.0.0.1", "rw=node-a", "rw=@10.0.0.0/8", "rw=10.0.0.1/24", "rw=0.0.0.0/0", "rw=010.0.0.0/8",
		"rw=10.0.0.1/32,no_root_squash", "sec=sys,rw=10.0.0.1/32", "rw=10.0.0.1/32,sec=krb5,ro=10.1.0.0/16",
		"ro=10.1.0.0/16,rw=10.0.0.1/32", "rw=10.0.0.1/32,rw=10.0.0.2/32", "rw=10.10.0.0/16:10.9.0.5/32",
		"rw=10.0.0.0/24,ro=10.0.0.5/32", "rw=10.0.0.1/32:10.0.0.1/32", "RW=10.0.0.1/32", "rw=10.0.0.1/32 ",
		"rw=fd00::/64", "Off",
	} {
		if cs, canonical := ParseShareNFS(s); canonical {
			t.Errorf("ParseShareNFS(%q) accepted as %v", s, cs)
		}
	}
}

// F14: the order is total,''')

sub('internal/endpoint/export_test.go', '''		{name: "foreign local sharenfs", c: c1, code: FailedPrecondition,''', '''		// wave12 S7: a later version keeps export state in sharenfs alone
		// (ADR 0004). After a rollback its volumes have a canonical local
		// sharenfs and no stored list, and are still the endpoint's
		{name: "canonical sharenfs without a stored list is ours", c: NFSClient{Host: "10.0.0.2/32"}, live: live1,
			row:  fsv("a", func(r *Row) { r.ShareNFS = "rw=10.0.0.1/32" }),
			want: set(map[string]string{clientsProp: "10.0.0.1/32=rw,10.0.0.2/32=rw", "sharenfs": "rw=10.0.0.1/32:10.0.0.2/32"})},
		// ... and a stale stored list never wins over the served value: the
		// later version granted 10.0.0.2 without updating it
		{name: "stale stored list: sharenfs wins", c: c1, live: etab("/tank/v/a\\t10.0.0.1/32(rw)\\n/tank/v/a\\t10.0.0.2/32(rw)\\n"),
			row:  exported("a", "10.0.0.1/32=rw", "rw=10.0.0.1/32:10.0.0.2/32"),
			want: set(map[string]string{clientsProp: "10.0.0.1/32=rw,10.0.0.2/32=rw"})},
		{name: "foreign local sharenfs", c: c1, code: FailedPrecondition,''')

sub('internal/endpoint/export_test.go', '''		{name: "served by a foreign sharenfs", host: "10.0.0.1", live: live, code: FailedPrecondition,
			row: fsv("a", func(r *Row) { r.ShareNFS = "rw=10.0.0.1/32" })},''', '''		{name: "served by a foreign sharenfs", host: "10.0.0.1", live: live, code: FailedPrecondition,
			row: fsv("a", func(r *Row) { r.ShareNFS = "rw=10.0.0.1/32,no_root_squash" })},
		// wave12 S7: canonical without a stored list is ours, so a detach
		// after a rollback revokes instead of failing for good
		{name: "canonical sharenfs without a stored list is ours", host: "10.0.0.1", live: live,
			row:  fsv("a", func(r *Row) { r.ShareNFS = "rw=10.0.0.1/32,ro=10.0.0.2/32" }),
			want: set(map[string]string{clientsProp: "10.0.0.2/32=ro", "sharenfs": "ro=10.0.0.2/32"})},
		{name: "stale stored list: sharenfs wins", host: "10.0.0.1", live: live,
			row:  exported("a", "10.0.0.1/32=rw", "rw=10.0.0.1/32,ro=10.0.0.2/32"),
			want: set(map[string]string{clientsProp: "10.0.0.2/32=ro", "sharenfs": "ro=10.0.0.2/32"})},''')

sub('internal/endpoint/export_test.go', '''		{name: "foreign but not served", host: "10.0.0.9", live: live,
			row: fsv("a", func(r *Row) { r.ShareNFS = "rw=10.0.0.1/32" })},''', '''		{name: "foreign but not served", host: "10.0.0.9", live: live,
			row: fsv("a", func(r *Row) { r.ShareNFS = "rw=10.0.0.1/32,no_root_squash" })},''')
