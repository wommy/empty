import sys, re
root = sys.argv[1]

def sub(path, old, new, count=1):
    p = root + '/' + path
    s = open(p).read()
    if old not in s:
        raise SystemExit('missing in %s: %s' % (path, old[:100]))
    s = s.replace(old, new, count)
    open(p, 'w').write(s)

def cut(path, start, end):
    """remove from the line starting with start up to (not including) end"""
    p = root + '/' + path
    s = open(p).read()
    i = s.index(start)
    j = s.index(end, i)
    s = s[:i] + s[j:]
    open(p, 'w').write(s)

f = 'internal/endpoint/export.go'
sub(f, '''// File export (NFS). The desired client list of a volume is a locally set
// user property (<prefix>:nfs_clients); sharenfs is computed from it and
// never written in any other form. Publish and Unpublish change one client
// at a time under the volume's lock, so concurrent front ends cannot drop
// each other's clients.''', '''// File export (NFS). Export state lives in the dataset's sharenfs and
// nowhere else (ADR 0004): a local value in the canonical grammar
// (ParseShareNFS) is the volume's client list, and is the only form ever
// written. Any other local value, or a received one, is someone else's:
// served untouched, never written. Publish and Unpublish change one client
// at a time under the volume's lock, so concurrent front ends cannot drop
// each other's clients.''')

cut(f, '// encodeClients is the stored form of a client list', '// ShareNFS is the sharenfs value for a client list')

sub(f, '''// exportRow checks what both directions need: a managed filesystem whose
// stored client list is readable. A malformed id names no volume.''', '''// exportRow checks what both directions need: a managed filesystem, and
// its client list. A malformed id names no volume.''')
sub(f, '''	cs, err := clientsOf(row)
	if err != nil {
		return row, nil, true, &Error{Code: FailedPrecondition, Msg: "volume " + id + ": " + p.Names.nfsClients(), Err: err}
	}
	return row, cs, true, nil
}

// clientsOf is the client list a volume is exported to. A local sharenfs
// in the canonical grammar is the list itself, with or without the stored
// nfs_clients: a later version keeps export state in sharenfs alone (ADR
// 0004), so after a rollback to this one the stored list may be missing
// or stale, and sharenfs, which the kernel serves, wins (wave12 S7). The
// stored list is read only when sharenfs is not canonical.
func clientsOf(row Row) ([]NFSClient, error) {
	if row.ShareNFSLocal {
		if cs, ok := ParseShareNFS(row.ShareNFS); ok {
			return cs, nil
		}
	}
	if !row.NFSClientsSet {
		return nil, nil
	}
	return decodeClients(row.NFSClients)
}''', '''	return row, clientsOf(row), true, nil
}

// clientsOf is the client list a volume is exported to: its local
// sharenfs, when that is in the canonical grammar. Anything else (a
// foreign or received value, or one inherited from the parent) is no list
// of the endpoint's; foreign() tells those apart. A leftover
// <prefix>:nfs_clients, the stored list earlier versions computed sharenfs
// from, is never read here (Reconcile reports it).
func clientsOf(row Row) []NFSClient {
	if !row.ShareNFSLocal {
		return nil
	}
	cs, _ := ParseShareNFS(row.ShareNFS)
	return cs
}''')

sub(f, '''// foreign reports a sharenfs that exports something and that the endpoint
// did not write, so it never overwrites it:
//   - set locally by someone else (e.g. democratic-csi's driver-wide value):
//     outside the canonical grammar, with no stored client list. A local
//     canonical value is ours whoever typed it (ADR 0004);''', '''// foreign reports a sharenfs that exports something and that is not the
// endpoint's to write (ADR 0004: ownership of the value, not of entries):
//   - set locally outside the canonical grammar, whoever typed it: options,
//     names, "*", an empty host, "on", overlapping networks (e.g.
//     democratic-csi's driver-wide value, or an admin's "rw=*" over one of
//     ours). A local canonical value is ours, whoever typed it;''')
sub(f, '''	_, canonical := ParseShareNFS(row.ShareNFS)
	return !canonical && !row.NFSClientsSet
}''', '''	_, canonical := ParseShareNFS(row.ShareNFS)
	return !canonical
}''')

sub(f, '''func foreignErr(row Row, what string) error {
	src := "set outside the endpoint"''', '''func foreignErr(row Row, what string) error {
	src := "outside the endpoint's grammar (rw=/ro= lists of IPv4 networks with /N, nothing else)"''')

sub(f, '''// exportOps sets the stored list and sharenfs where they differ from want.
// An inherited sharenfs is always pinned locally: a parent shared to "*"
// would otherwise keep exporting the volume to everyone.
func (p Planner) exportOps(row Row, want []NFSClient) (Plan, error) {
	share, enc := ShareNFS(want), encodeClients(want)
	if len(share) > maxPropLen || len(enc) > maxPropLen {
		return nil, errorf(ResourceExhausted, "volume %s: %d clients exceed the %d-byte property limit (sharenfs %d, %s %d bytes); use a CIDR",
			row.ID, len(want), maxPropLen, len(share), p.Names.nfsClients(), len(enc))
	}
	set := map[string]string{}
	if !row.NFSClientsSet || row.NFSClients != enc {
		set[p.Names.nfsClients()] = enc
	}
	if !row.ShareNFSLocal || row.ShareNFS != share {
		set["sharenfs"] = share
	}
	if len(set) == 0 {
		return nil, nil
	}
	return Plan{{Kind: OpSet, Target: row.Name, Props: set}}, nil
}''', '''// exportOps sets sharenfs where it differs from want. An inherited sharenfs
// is always pinned locally: a parent shared to "*" would otherwise keep
// exporting the volume to everyone.
func (p Planner) exportOps(row Row, want []NFSClient) (Plan, error) {
	share := ShareNFS(want)
	if len(share) > maxPropLen {
		return nil, errorf(ResourceExhausted, "volume %s: %d clients exceed the %d-byte sharenfs limit (%d bytes); use a CIDR",
			row.ID, len(want), maxPropLen, len(share))
	}
	if row.ShareNFSLocal && row.ShareNFS == share {
		return nil, nil
	}
	return Plan{{Kind: OpSet, Target: row.Name, Props: map[string]string{"sharenfs": share}}}, nil
}''')

sub(f, '''	cs, _ := clientsOf(row)
	if _, ok := overlap(cs, c); ok {''', '''	cs := clientsOf(row)
	if _, ok := overlap(cs, c); ok {''')
sub(f, '''	cs, _ := clientsOf(row)
	h, err := parseClient(host)''', '''	cs := clientsOf(row)
	h, err := parseClient(host)''')

# unpublish: a foreign value that still serves the host is refused, never
# reported done
sub(f, '''	// served: its own line, or (on a volume whose sharenfs is ours) any
	// line beyond want that can match it, e.g. an admin's edit of our
	// sharenfs to "rw=*" (rewritten below). A foreign sharenfs is never
	// rewritten, and a client it covers was never granted by us.
	_, isLive := live.Lookup(row.Mount, c.Host)
	isLive = isLive || (!foreign(row) && len(live.covers(row.Mount, c.Host, want)) > 0)''', '''	// served: its own line, or any line beyond want that can match it,
	// e.g. an inherited "rw=" (empty host: everyone) or an admin's "rw=*".
	// A foreign sharenfs is never rewritten; while it serves the host,
	// Unpublish says so instead of reporting the client gone.
	_, isLive := live.Lookup(row.Mount, c.Host)
	isLive = isLive || len(live.covers(row.Mount, c.Host, want)) > 0''')

f = 'internal/endpoint/endpoint.go'
sub(f, '''// nfsClients is the endpoint's own property (not the baseline's): the
// desired NFS client list that sharenfs is computed from.
func (n props) nfsClients() string { return n.p + ":nfs_clients" }''', '''// nfsClients is the client list earlier versions stored and computed
// sharenfs from. Export state now lives in sharenfs alone (ADR 0004): the
// property is never written or decided on, only read so Reconcile can
// report a leftover one.
func (n props) nfsClients() string { return n.p + ":nfs_clients" }''')
sub(f, '''// mountpoint, %q) and a sharenfs the endpoint did not write (set
// locally by someone else, or received) are FailedPrecondition.''', '''XX''') if False else None
