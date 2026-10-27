import sys
root = sys.argv[1]
f = root + '/internal/endpoint/export_test.go'
s = open(f).read()

def sub(old, new):
    global s
    if old not in s:
        raise SystemExit('missing: ' + old[:120])
    s = s.replace(old, new, 1)

sub('''		// an edit inside the grammar is accepted as truth (ADR 0004): the
		// stored list follows it, sharenfs is not rewritten
		{name: "in-grammar edit is accepted", row: exported("a", "rw=10.0.0.1/32:10.6.6.6/32"), live: live1, c: c1,
			want: set(map[string]string{})},''', '''		// an edit inside the grammar is accepted as truth (ADR 0004):
		// sharenfs is not rewritten
		{name: "in-grammar edit is accepted", row: exported("a", "rw=10.0.0.1/32:10.6.6.6/32"), live: live1, c: c1},''')

sub('''		// ... and a stale stored list never wins over the served value: the
		// later version granted 10.0.0.2 without updating it
		{name: "stale stored list: sharenfs wins", c: c1, live: etab("/tank/v/a\\t10.0.0.1/32(rw)\\n/tank/v/a\\t10.0.0.2/32(rw)\\n"),
			row:  exported("a", "rw=10.0.0.1/32:10.0.0.2/32"),
			want: set(map[string]string{})},''', '''		// a leftover stored list is never read, and never written
		{name: "leftover stored list is ignored", c: c1, live: etab("/tank/v/a\\t10.0.0.1/32(rw)\\n/tank/v/a\\t10.0.0.2/32(rw)\\n"),
			row: leftover(exported("a", "rw=10.0.0.1/32:10.0.0.2/32"))},''')

sub('''		// the stored list is read only when sharenfs is not canonical
		{name: "corrupt client list", c: c1, code: FailedPrecondition, row: func() Row {
			r := exported("a", "off")
			r.ShareNFSLocal = false
			return r
		}()},
		{name: "duplicate client list", c: c1, code: FailedPrecondition, row: func() Row {
			r := exported("a", "rw=10.0.0.1/32")
			r.ShareNFSLocal = false
			return r
		}()},''', '''		{name: "leftover stored list, inherited sharenfs: pinned", c: c1,
			row:  leftover(fsv("a", func(r *Row) { r.ShareNFSLocal = false })),
			want: set(map[string]string{"sharenfs": "rw=10.0.0.1/32"})},
		// ADR 0004: an admin's "*" over the endpoint's value is someone
		// else's, never rewritten, leftover stored list or not
		{name: "admin's * over our list is foreign", c: c1, code: FailedPrecondition, live: etab("/tank/v/a\\t*(rw)\\n"),
			row: leftover(exported("a", "rw=*"))},''')

sub('''// F7: the limit is ZFS_MAXPROPLEN-1 on sharenfs and on the stored list,
// which costs more per client (",host=rw" versus ":host").
func TestExportSizeLimit(t *testing.T) {
	var cs []NFSClient
	for i := 0; len(ShareNFS(cs)) <= maxPropLen; i++ {
		cs = append(cs, NFSClient{Host: fmt.Sprintf("10.%d.%d.%d/32", i/62500, i/250%250, i%250)})
	}
	if maxPropLen != 8191 {
		t.Fatalf("maxPropLen %d", maxPropLen)
	}
	// both under the limit: planned
	n := 0
	for len(encodeClients(cs[:n+1])) <= maxPropLen {
		n++
	}
	if _, err := pl.exportOps(fsv("a", nil), cs[:n]); err != nil {
		t.Fatalf("%d clients: %v", n, err)
	}
	// sharenfs still fits, the stored list does not
	if len(ShareNFS(cs[:n+1])) > maxPropLen {
		t.Fatalf("test setup: sharenfs %d", len(ShareNFS(cs[:n+1])))
	}
	if _, err := pl.exportOps(fsv("a", nil), cs[:n+1]); CodeOf(err) != ResourceExhausted || !strings.Contains(err.Error(), "nfs_clients") {
		t.Fatalf("%d clients: %v", n+1, err)
	}
}''', '''// F7: the limit is ZFS_MAXPROPLEN-1, on sharenfs alone (ADR 0004).
func TestExportSizeLimit(t *testing.T) {
	t.Parallel()
	if maxPropLen != 8191 {
		t.Fatalf("maxPropLen %d", maxPropLen)
	}
	var cs []NFSClient
	for i := 0; len(ShareNFS(cs)) <= maxPropLen; i++ {
		cs = append(cs, NFSClient{Host: fmt.Sprintf("10.%d.%d.%d/32", i/62500, i/250%250, i%250)})
	}
	n := len(cs) - 1 // the most that fit
	if _, err := pl.exportOps(fsv("a", nil), cs[:n]); err != nil {
		t.Fatalf("%d clients: %v", n, err)
	}
	if _, err := pl.exportOps(fsv("a", nil), cs); CodeOf(err) != ResourceExhausted || !strings.Contains(err.Error(), "sharenfs") {
		t.Fatalf("%d clients: %v", n+1, err)
	}
}''')

sub('''		{name: "stale stored list: sharenfs wins", host: "10.0.0.1", live: live,
			row:  exported("a", "rw=10.0.0.1/32,ro=10.0.0.2/32"),
			want: set(map[string]string{"sharenfs": "ro=10.0.0.2/32"})},''', '''		{name: "leftover stored list is ignored", host: "10.0.0.1", live: live,
			row:  leftover(exported("a", "rw=10.0.0.1/32,ro=10.0.0.2/32")),
			want: set(map[string]string{"sharenfs": "ro=10.0.0.2/32"})},''')

sub('''		// D3 (wave9 nfs-diff): a line beyond the stored list that can
		// match the host still serves it; on a volume whose sharenfs is
		// ours it is rewritten, not reported gone. "Ours" is today's
		// nfs_clients model, as in "drifted sharenfs is rewritten" above;
		// when nfs_clients goes (ADR 0004) "rw=*" is foreign and this
		// case becomes a FailedPrecondition
		{name: "our sharenfs widened to *: rewritten", host: "10.0.0.1",
			row: exported("a", "rw=*"), live: etab("/tank/v/a\\t*(rw)\\n"),
			want: set(map[string]string{"sharenfs": "ro=10.0.0.2/32"})},''', '''		// D3 (wave9 nfs-diff): a line beyond the client list that can
		// match the host still serves it, so the host is not reported
		// gone. An admin's "rw=*" over the endpoint's value is foreign
		// (ADR 0004): it is never rewritten, and Unpublish refuses
		{name: "admin's * over our list: refused, not rewritten", host: "10.0.0.1",
			row: leftover(exported("a", "rw=*")), live: etab("/tank/v/a\\t*(rw)\\n"), code: FailedPrecondition},''')

sub('''		{name: "foreign sharenfs to * is not ours to revoke", host: "10.0.0.1", live: etab("/tank/v/a\\t*(rw)\\n"),
			row: fsv("a", func(r *Row) { r.ShareNFS = "rw=*" })},''', '''		// not ours to revoke, and still serving the host: never "done"
		{name: "foreign sharenfs to * still serves the host", host: "10.0.0.1", live: etab("/tank/v/a\\t*(rw)\\n"),
			row: fsv("a", func(r *Row) { r.ShareNFS = "rw=*" }), code: FailedPrecondition},''')

sub('''		{name: "corrupt", host: "10.0.0.1", row: func() Row {
			r := exported("a", "off")
			r.ShareNFSLocal = false
			return r
		}(), code: FailedPrecondition},''', '''		{name: "leftover stored list, inherited off", host: "10.0.0.1",
			row: leftover(fsv("a", func(r *Row) { r.ShareNFSLocal = false }))},''')

sub('''func applyRow(r Row, plan Plan) (Row, Etab) {
	for _, op := range plan {
		if v, ok := op.Props[clientsProp]; ok {
			r.NFSClients, r.NFSClientsSet = v, true
		}
		if v, ok := op.Props["sharenfs"]; ok {
			r.ShareNFS, r.ShareNFSLocal = v, true
		}
	}
	e := Etab{}
	cs, _ := decodeClients(r.NFSClients)
	if r.ShareNFS == ShareNFS(cs) && r.ShareNFS != "off" {
		e[r.Mount] = cs
	}
	return r, e
}''', '''func applyRow(r Row, plan Plan) (Row, Etab) {
	for _, op := range plan {
		if v, ok := op.Props["sharenfs"]; ok {
			r.ShareNFS, r.ShareNFSLocal = v, true
		}
	}
	e := Etab{}
	if cs, ok := ParseShareNFS(r.ShareNFS); ok && len(cs) > 0 {
		e[r.Mount] = cs
	}
	return r, e
}''')

sub('''// exported is a volume the endpoint already exports: share is its local
// sharenfs.
func exported(id, share string) Row {
	return fsv(id, func(r *Row) { r.ShareNFS = share })
}''', '''// exported is a volume the endpoint already exports: share is its local
// sharenfs.
func exported(id, share string) Row {
	return fsv(id, func(r *Row) { r.ShareNFS = share })
}

// leftover marks r with a locally set <prefix>:nfs_clients, the client
// list earlier versions stored (ADR 0004: ignored, reported by Reconcile).
func leftover(r Row) Row {
	r.NFSClientsSet = true
	return r
}''')
open(f, 'w').write(s)
