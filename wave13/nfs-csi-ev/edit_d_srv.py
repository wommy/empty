import sys
root = sys.argv[1]

def sub(path, old, new):
    p = root + '/' + path
    s = open(p).read()
    if old not in s:
        raise SystemExit('missing in %s: %s' % (path, old[:100]))
    s = s.replace(old, new, 1)
    open(p, 'w').write(s)

f = 'internal/server/server.go'
sub(f, '''func volumeOut(v endpoint.Volume) *pb.Volume {
	return &pb.Volume{
		Id: v.ID, Name: v.Name, Dataset: v.Dataset, Kind: kindOut(v.Kind),
		CapacityBytes: v.CapacityBytes, SourceType: v.SourceType, SourceId: v.SourceID,
		DevicePath: v.DevicePath, Mountpoint: v.Mountpoint,
	}
}''', '''func volumeOut(v endpoint.Volume) *pb.Volume {
	out := &pb.Volume{
		Id: v.ID, Name: v.Name, Dataset: v.Dataset, Kind: kindOut(v.Kind),
		CapacityBytes: v.CapacityBytes, SourceType: v.SourceType, SourceId: v.SourceID,
		DevicePath: v.DevicePath, Mountpoint: v.Mountpoint,
	}
	if n := v.NFS; n != nil {
		out.Nfs = &pb.NFSExport{Clients: n.Clients, ReadOnly: n.ReadOnly, Server: n.Server}
	}
	return out
}

func nfsIn(n *pb.NFSExport) *endpoint.NFSExport {
	if n == nil {
		return nil
	}
	return &endpoint.NFSExport{Clients: n.GetClients(), ReadOnly: n.GetReadOnly(), Server: n.GetServer()}
}''')
sub(f, '''	return &pb.GetCapabilitiesResponse{Parent: s.Parent, Version: version.Version, Capabilities: caps}, nil''', '''	return &pb.GetCapabilitiesResponse{Parent: s.Parent, Version: version.Version, Capabilities: caps, NfsServers: s.EP.NFSServers()}, nil''')
sub(f, '''		Properties: r.GetProperties(),
	})''', '''		Properties: r.GetProperties(),
		NFS:        nfsIn(r.GetNfsExport()),
	})''')

f = 'cmd/zfs-endpoint/main.go'
sub(f, '''	destroyForeign := fs.Bool("destroy-foreign-snapshots", false, "let DeleteVolume destroy snapshots the endpoint did not make along with their volume")''', '''	destroyForeign := fs.Bool("destroy-foreign-snapshots", false, "let DeleteVolume destroy snapshots the endpoint did not make along with their volume")
	var nfsServers []string
	fs.Func("nfs-server", "an NFS server address (IPv4 or DNS name) clients mount volumes from; repeat for more, the first is the default. "+
		"Advertise stable names (a VIP or DNS name): a volume keeps the address it was created with for its life. Needs root", func(s string) error {
		if err := endpoint.ValidNFSServer(s); err != nil {
			return err
		}
		nfsServers = append(nfsServers, s)
		return nil
	})''')
sub(f, '''	if !root && !*allowNonRoot {
		return configErr("must run as root (euid is %d); pass -allow-non-root to run with delegated zfs permissions, NFS export off", d.euid)
	}''', '''	if !root && !*allowNonRoot {
		return configErr("must run as root (euid is %d); pass -allow-non-root to run with delegated zfs permissions, NFS export off", d.euid)
	}
	if !root && len(nfsServers) > 0 {
		return configErr("-nfs-server needs root: NFS export is off without it")
	}''')
sub(f, '''DestroyForeignSnapshots: *destroyForeign, NFSMinPrefix: *nfsMinPrefix, Settle: d.settle})''', '''DestroyForeignSnapshots: *destroyForeign, NFSMinPrefix: *nfsMinPrefix, NFSServers: nfsServers, Settle: d.settle})''')
