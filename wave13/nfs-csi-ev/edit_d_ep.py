import sys
root = sys.argv[1]

def sub(path, old, new):
    p = root + '/' + path
    s = open(p).read()
    if old not in s:
        raise SystemExit('missing in %s: %s' % (path, old[:100]))
    s = s.replace(old, new, 1)
    open(p, 'w').write(s)

f = 'internal/endpoint/endpoint.go'
sub(f, '''		n.snapshotName(), n.snapshotSource(), n.nfsClients(), n.cloneSourceFor(), n.committedAt()}
}''', '''		n.snapshotName(), n.snapshotSource(), n.nfsClients(), n.cloneSourceFor(), n.committedAt(), n.nfsServer()}
}''')
sub(f, '''// baseline's name for the internal snapshot''', '''// nfsServer is the endpoint's own property: the NFS server address a
// volume exported at create was created with, set in the same `zfs create`
// (or clone) as its sharenfs. CSI and Kubernetes hold it fixed for the
// volume's life (a PV's volume attributes are immutable), and OpenZFS
// keeps no such address, so it is recorded once. It is addressing
// metadata, not export state (ADR 0004: who may mount is sharenfs alone).
// Read only when set locally: an inherited or received one is not this
// volume's (Reconcile reports it).
func (n props) nfsServer() string { return n.p + ":nfs_server" }

// baseline's name for the internal snapshot''')

sub(f, '''	// NFSMinPrefix is the broadest client network PublishFileExport
	// accepts: an IPv4 prefix length, 1-32. Default DefaultNFSMinPrefix
	// (/16). Unpublish takes any network, so raising it never strands an
	// export.
	NFSMinPrefix int''', '''	// NFSMinPrefix is the broadest client network PublishFileExport and
	// CreateVolume's NFS export accept: an IPv4 prefix length, 1-32.
	// Default DefaultNFSMinPrefix (/16). Unpublish takes any network, and
	// the floor is not part of the ownership grammar (ParseShareNFS), so
	// raising it never strands an export.
	NFSMinPrefix int
	// NFSServers are the addresses (IPv4 or DNS names) NFS clients mount
	// this endpoint's volumes from, in order of preference: what
	// CreateVolumeRequest.NFS.Server chooses among, and the first is the
	// default. Advertise stable names (a VIP or a DNS name): a volume
	// records the address it was created with, and the PVs that hold it
	// can never be changed. Needs FileExport. Empty: CreateVolume takes no
	// NFS export.
	NFSServers []string''')

sub(f, '''	if cfg.NFSMinPrefix < 1 || cfg.NFSMinPrefix > 32 {
		return nil, fmt.Errorf("NFS minimum prefix /%d: want 1-32", cfg.NFSMinPrefix)
	}
	names := props{cfg.PropertyPrefix}
	return &Endpoint{
		zfs:   z,
		cfg:   cfg,
		names: names,
		plan: Planner{Parent: cfg.Parent, Names: names, VolBlockSize: cfg.VolBlockSize, DestroyForeignSnapshots: cfg.DestroyForeignSnapshots,
			RequireMounted: cfg.FileExport},
	}, nil
}''', '''	if cfg.NFSMinPrefix < 1 || cfg.NFSMinPrefix > 32 {
		return nil, fmt.Errorf("NFS minimum prefix /%d: want 1-32", cfg.NFSMinPrefix)
	}
	if len(cfg.NFSServers) > 0 && !cfg.FileExport {
		return nil, errors.New("NFS servers are advertised, but file export is off (it needs root)")
	}
	for i, s := range cfg.NFSServers {
		if err := ValidNFSServer(s); err != nil {
			return nil, err
		}
		if slices.Contains(cfg.NFSServers[:i], s) {
			return nil, fmt.Errorf("NFS server %q is listed twice", s)
		}
	}
	cfg.NFSServers = slices.Clone(cfg.NFSServers)
	names := props{cfg.PropertyPrefix}
	return &Endpoint{
		zfs:   z,
		cfg:   cfg,
		names: names,
		plan: Planner{Parent: cfg.Parent, Names: names, VolBlockSize: cfg.VolBlockSize, DestroyForeignSnapshots: cfg.DestroyForeignSnapshots,
			RequireMounted: cfg.FileExport, NFSServers: cfg.NFSServers, NFSMinPrefix: cfg.NFSMinPrefix},
	}, nil
}

// ValidNFSServer checks an NFS server address as -nfs-server takes it: an
// IPv4 address in canonical form or a DNS name (letters, digits, '-' and
// '.', labels of 1-63 bytes, 253 bytes at most). csi-driver-nfs mounts
// "<server>:<share>", so nothing else may reach it.
func ValidNFSServer(s string) error {
	if a, err := netip.ParseAddr(s); err == nil {
		if a.Is4() && a.String() == s {
			return nil
		}
		return fmt.Errorf("NFS server %q: want an IPv4 address or a DNS name (IPv6 is not supported yet)", s)
	}
	bad := fmt.Errorf("NFS server %q: want an IPv4 address or a DNS name", s)
	if s == "" || len(s) > 253 {
		return bad
	}
	for _, l := range strings.Split(strings.TrimSuffix(s, "."), ".") {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return bad
		}
		for _, c := range []byte(l) {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return bad
			}
		}
	}
	return nil
}''')

sub(f, '''	// DevicePath for Block (on the storage host), Mountpoint for Filesystem.
	DevicePath string
	Mountpoint string
}''', '''	// DevicePath for Block (on the storage host), Mountpoint for Filesystem.
	DevicePath string
	Mountpoint string
	// NFS, for a filesystem with a recorded NFS server or an export in
	// the endpoint's grammar: Server is the address recorded at create,
	// Clients and ReadOnly the local sharenfs (ReadOnly: every client).
	NFS *NFSExport
}

// NFSExport is a static NFS export, given with CreateVolume.
type NFSExport struct {
	// Clients are IPv4 networks with an explicit prefix length and host
	// bits zero ("10.20.0.0/24", "10.20.0.5/32"), at or above
	// Config.NFSMinPrefix, none overlapping.
	Clients  []string
	ReadOnly bool
	// Server is one of Config.NFSServers; "" is the first.
	Server string
}''')

sub(f, '''	// Properties are native ZFS properties applied at create, checked
	// against a per-kind allowlist (props.go). A committed volume is
	// compatible only if each one is set locally to the same value.
	Properties map[string]string
}''', '''	// Properties are native ZFS properties applied at create, checked
	// against a per-kind allowlist (props.go). A committed volume is
	// compatible only if each one is set locally to the same value.
	Properties map[string]string
	// NFS exports a filesystem at create (needs Config.NFSServers): the
	// volume is committed only once the NFS server serves every client.
	// A committed volume is compatible only with the same clients and
	// mode, and with the same server or none.
	NFS *NFSExport
}''')

sub(f, '''	if v.Kind == Block {
		v.DevicePath = "/dev/zvol/" + r.Name
	} else {
		v.Mountpoint = r.Mount
	}
	return v
}''', '''	if v.Kind == Block {
		v.DevicePath = "/dev/zvol/" + r.Name
		return v
	}
	v.Mountpoint = r.Mount
	if cs := clientsOf(r); r.NFSServer != "" || len(cs) > 0 {
		v.NFS = &NFSExport{Server: r.NFSServer, ReadOnly: len(cs) > 0}
		for _, c := range cs {
			v.NFS.Clients = append(v.NFS.Clients, c.Host)
			v.NFS.ReadOnly = v.NFS.ReadOnly && c.ReadOnly
		}
	}
	return v
}''')

sub(f, '''		if err := e.apply(ctx, plan); err != nil {
			if row, ok := t.Get(id); ok && !row.Committed {
				return Volume{}, resumeErr(err)
			}
			return Volume{}, err
		}
		if pass > 0 || len(plan) == 0 || p.commits(plan) {
			break
		}
	}
	return e.readVolume(ctx, id)
}''', '''		commit := Plan(nil)
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
		if pass > 0 || len(plan)+len(commit) == 0 || commit != nil || p.commits(plan) {
			break
		}
	}
	return e.readVolume(ctx, id)
}

// verifyExport holds an NFS volume's commit until the NFS server serves
// its export: `zfs create -o sharenfs` exits 0 when the share or exportfs
// fails (csi-nfs-design §4, Fact A). Each client must be served in its
// mode, by its own line, and nothing beyond the export; a miss re-shares
// once (the value unchanged), then is Unavailable, uncommitted, and a
// retry converges. nfsd must run: etab alone is no proof of a server.
func (e *Endpoint) verifyExport(ctx context.Context, id string) error {
	for try := 0; ; try++ {
		t, err := e.scan(ctx, id)
		if err != nil {
			return err
		}
		row, ok := t.Get(id)
		if !ok || !row.OwnedVolume() {
			return errorf(Aborted, "volume %s vanished or changed owner during provisioning", id)
		}
		want := clientsOf(row)
		live, err := e.exportTable(ctx, t, id)
		if err != nil {
			return err
		}
		var missing []string
		for _, c := range want {
			if !live.serves(row.Mount, c) {
				missing = append(missing, c.Host+" "+c.mode())
			}
		}
		if x := live.exposure(realPath(row.Mount), want); len(x) > 0 && len(missing) == 0 {
			return errorf(FailedPrecondition, "volume %s is served beyond its export by the NFS export table: %s; "+
				"remove these exports (an ancestor dataset's sharenfs with crossmnt, e.g. \\"on\\", or /etc/exports); the volume is not committed", id, strings.Join(x, "; "))
		}
		if len(missing) == 0 && len(want) > 0 {
			return e.nfsdRunning(ctx)
		}
		if try > 0 {
			return errorf(Unavailable, "sharenfs on %s is set, but the NFS export table does not serve %s to %s: "+
				"zfs does not report exportfs failures; check exportfs and the NFS server on the storage host; the volume is not committed",
				row.Name, row.Mount, strings.Join(missing, ", "))
		}
		if err := e.apply(ctx, reshare(row, want)); err != nil {
			return resumeErr(err)
		}
	}
}''')

sub(f, '''// FileExportEnabled reports whether this endpoint offers NFS export.
func (e *Endpoint) FileExportEnabled() bool { return e.cfg.FileExport }''', '''// FileExportEnabled reports whether this endpoint offers NFS export.
func (e *Endpoint) FileExportEnabled() bool { return e.cfg.FileExport }

// NFSServers are the NFS server addresses this endpoint advertises
// (Config.NFSServers), in order.
func (e *Endpoint) NFSServers() []string { return slices.Clone(e.cfg.NFSServers) }''')

sub(f, '''import (
	"context"
	"errors"
	"fmt"
	"slices"''', '''import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"''')
