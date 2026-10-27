import sys
root = sys.argv[1]

def sub(path, old, new):
    p = root + '/' + path
    s = open(p).read()
    if old not in s:
        raise SystemExit('missing in %s: %s' % (path, old[:100]))
    s = s.replace(old, new, 1)
    open(p, 'w').write(s)

f = 'internal/endpoint/plan.go'
sub(f, '''	RequireMounted bool

	// now, when set,''', '''	RequireMounted bool
	// NFSServers and NFSMinPrefix are Config's: what a CreateVolume NFS
	// export is checked against.
	NFSServers   []string
	NFSMinPrefix int

	// now, when set,''')

sub(f, '''	if r.LimitBytes > 0 && !blockClone && p.want(r, props) > r.LimitBytes {
		return "", nil, errorf(OutOfRange, "capacity %d (rounded) exceeds limit %d", p.want(r, props), r.LimitBytes)
	}
	return id, props, nil
}''', '''	if r.LimitBytes > 0 && !blockClone && p.want(r, props) > r.LimitBytes {
		return "", nil, errorf(OutOfRange, "capacity %d (rounded) exceeds limit %d", p.want(r, props), r.LimitBytes)
	}
	if _, err := p.validateNFS(r); err != nil {
		return "", nil, err
	}
	return id, props, nil
}

// nfsWant is a validated CreateVolume NFS export.
type nfsWant struct {
	clients  []NFSClient // sorted, canonical
	server   string      // resolved: the request's, or the first advertised
	explicit bool        // the request named the server
}

// validateNFS checks a request's NFS export on its own: a filesystem, an
// endpoint that advertises NFS servers, client networks in the canonical
// grammar (each with an explicit /N, host bits zero, at or above the
// floor, none overlapping), never masked or completed, and an advertised
// server. Nil without an export.
func (p Planner) validateNFS(r CreateVolumeRequest) (*nfsWant, error) {
	if r.NFS == nil {
		return nil, nil
	}
	if r.Kind == Block {
		return nil, errorf(InvalidArgument, "an NFS export needs a filesystem volume: a zvol cannot be exported over NFS")
	}
	if len(p.NFSServers) == 0 {
		return nil, errorf(FailedPrecondition, "this endpoint advertises no NFS server (-nfs-server), so it takes no NFS export")
	}
	if len(r.NFS.Clients) == 0 {
		return nil, errorf(InvalidArgument, "an NFS export needs at least one client network")
	}
	floor := cmp.Or(p.NFSMinPrefix, DefaultNFSMinPrefix)
	var cs []NFSClient
	for _, h := range r.NFS.Clients {
		if h == "" {
			return nil, errorf(InvalidArgument, "empty NFS client entry (an empty host exports to everyone)")
		}
		c, err := ParseClient(h, r.NFS.ReadOnly)
		if err != nil {
			return nil, err
		}
		if !strings.Contains(h, "/") {
			return nil, errorf(InvalidArgument, "NFS client %q: write the network with its prefix length (%s): nfs-utils resolves and renames a bare address", h, c.Host)
		}
		if err := checkFloor(c, floor); err != nil {
			return nil, err
		}
		if x, ok := overlap(cs, c); ok {
			return nil, errorf(InvalidArgument, "NFS clients %s and %s overlap: nfs-utils applies the first matching line, so both cannot hold", x.Host, c.Host)
		}
		cs = append(cs, c)
	}
	cs = sortClients(cs)
	if n := len(ShareNFS(cs)); n > maxPropLen {
		return nil, errorf(InvalidArgument, "%d NFS clients make a %d-byte sharenfs, over the %d-byte limit; use wider networks", len(cs), n, maxPropLen)
	}
	server := r.NFS.Server
	if server == "" {
		server = p.NFSServers[0]
	} else if !slices.Contains(p.NFSServers, server) {
		return nil, errorf(InvalidArgument, "NFS server %q is not one this endpoint advertises (%s)", server, strings.Join(p.NFSServers, ", "))
	}
	return &nfsWant{clients: cs, server: server, explicit: r.NFS.Server != ""}, nil
}

// nfsCompatible applies CreateVolume idempotency to a committed volume's
// export. The value is compared as parsed (canonical form is unique, so
// order never matters); an admin's edit inside the grammar is the volume's
// value, and the message names it. The server is fixed at create: another
// one named explicitly is AlreadyExists, an omitted one takes the recorded
// one, even when the advertised list changed since (csi-nfs-design §6 as
// amended by wave13 Q1).
func (p Planner) nfsCompatible(row Row, w *nfsWant) error {
	if w == nil {
		return nil
	}
	want := ShareNFS(w.clients)
	if !row.ShareNFSLocal || foreign(row) {
		src := "inherited"
		switch {
		case row.ShareNFSRecv:
			src = "received"
		case row.ShareNFSLocal:
			src = "set outside the endpoint's grammar"
		}
		return errorf(AlreadyExists, "volume %s exists with sharenfs=%q (%s), requested %s", row.ID, row.ShareNFS, src, want)
	}
	if row.ShareNFS != want {
		return errorf(AlreadyExists, "volume %s exists exported %s, requested %s", row.ID, row.ShareNFS, want)
	}
	if w.explicit && row.NFSServer != w.server {
		return errorf(AlreadyExists, "volume %s was created with NFS server %q, requested %q: a volume's server never changes (its PVs hold it)", row.ID, row.NFSServer, w.server)
	}
	return nil
}''')

sub(f, '''	id, props, err := p.validateCreate(r)
	if err != nil {
		return nil, err
	}
	want := p.want(r, props)
	srcType, srcID := sourceOf(r)''', '''	id, props, err := p.validateCreate(r)
	if err != nil {
		return nil, err
	}
	nfs, err := p.validateNFS(r)
	if err != nil {
		return nil, err
	}
	want := p.want(r, props)
	srcType, srcID := sourceOf(r)''')

sub(f, '''			plan := p.sizeOps(id, row, r, size)
			if set := propSet(row, props); len(set) > 0 {
				plan = append(plan, Op{Kind: OpSet, Target: p.full(id), Props: set})
			}''', '''			plan := p.sizeOps(id, row, r, size)
			set := propSet(row, props)
			if nfs != nil {
				// nobody holds an uncommitted volume's context yet: it
				// converges to the request, server included
				if share := ShareNFS(nfs.clients); !row.ShareNFSLocal || row.ShareNFS != share {
					set["sharenfs"] = share
				}
				if row.NFSServer != nfs.server {
					set[p.Names.nfsServer()] = nfs.server
				}
			}
			if len(set) > 0 {
				plan = append(plan, Op{Kind: OpSet, Target: p.full(id), Props: set})
			}''')

sub(f, '''			return nil, errorf(AlreadyExists, "volume %s exists without %s=%s set locally", id, k, props[k])
		}
		return nil, nil
	}''', '''			return nil, errorf(AlreadyExists, "volume %s exists without %s=%s set locally", id, k, props[k])
		}
		return nil, p.nfsCompatible(row, nfs)
	}''')

sub(f, '''		own["sharenfs"], own["sharesmb"] = "off", "off"
	}''', '''		own["sharenfs"], own["sharesmb"] = "off", "off"
	}
	if nfs != nil {
		// set in the create (or clone) itself: no committed volume can
		// exist without its export or its server (csi-nfs-design §4); a
		// clone takes the request's export, never its source's
		own["sharenfs"], own[p.Names.nfsServer()] = ShareNFS(nfs.clients), nfs.server
	}''')

sub(f, '''import (
	"crypto/sha256"''', '''import (
	"cmp"
	"crypto/sha256"''')

f = 'internal/endpoint/table.go'
sub(f, '''	ShareNFSRecv bool   // sharenfs came from `zfs recv` (source "received")''', '''	// NFSServer is the NFS server address recorded at create (local
	// <prefix>:nfs_server); NFSServerElsewhere: one is set, but inherited
	// or received, so not this volume's (Reconcile reports it).
	NFSServer          string
	NFSServerElsewhere bool

	ShareNFSRecv bool   // sharenfs came from `zfs recv` (source "received")''')
sub(f, '''		n.snapshotName(), n.snapshotSource(), n.nfsClients(), n.cloneSourceFor(), n.committedAt(),
	}, propNames[:]...)''', '''		n.snapshotName(), n.snapshotSource(), n.nfsClients(), n.cloneSourceFor(), n.committedAt(), n.nfsServer(),
	}, propNames[:]...)''')
sub(f, '''	cCloneFor, cCommittedAt := j(n.cloneSourceFor()), j(n.committedAt())''', '''	cCloneFor, cCommittedAt, cNFSServer := j(n.cloneSourceFor()), j(n.committedAt()), j(n.nfsServer())''')
sub(f, '''			NFSClientsSet:  cell(cClients).Local(),''', '''			NFSClientsSet:  cell(cClients).Local(),
			NFSServer:      own(cNFSServer),
			NFSServerElsewhere: !cell(cNFSServer).Local() && userValue(cell(cNFSServer)) != "",''')

f = 'internal/endpoint/reconcile.go'
sub(f, '''	SkipLegacyNFSClients = "leftover nfs_clients property: ignored, sharenfs alone is the export state (ADR 0004); zfs inherit it to clear"
)''', '''	SkipLegacyNFSClients = "leftover nfs_clients property: ignored, sharenfs alone is the export state (ADR 0004); zfs inherit it to clear"
	// SkipNFSServerNotLocal: <prefix>:nfs_server is inherited or received,
	// so it is not the address this volume was created with; it is
	// ignored (the volume reports no server).
	SkipNFSServerNotLocal = "nfs_server is inherited or received, not set on the volume: ignored"
	// SkipNFSServerUnadvertised: the server a volume was created with is
	// no longer one the endpoint advertises (-nfs-server). Its PVs still
	// mount it and can never be changed (a PV's attributes are
	// immutable); re-create the PVs, or advertise the address again.
	SkipNFSServerUnadvertised = "recorded NFS server is no longer advertised (-nfs-server): its PVs still mount it"
)''')
sub(f, '''	for r := range cur.Volumes() {
		if r.OwnedVolume() && r.NFSClientsSet {
			skip(r.ID, SkipLegacyNFSClients)
		}
	}''', '''	for r := range cur.Volumes() {
		if !r.OwnedVolume() {
			continue
		}
		if r.NFSClientsSet {
			skip(r.ID, SkipLegacyNFSClients)
		}
		if r.NFSServerElsewhere {
			skip(r.ID, SkipNFSServerNotLocal)
		}
		if r.Committed && r.NFSServer != "" && !slices.Contains(p.NFSServers, r.NFSServer) {
			skip(r.ID, SkipNFSServerUnadvertised)
		}
	}''')
