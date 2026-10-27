import sys
p=sys.argv[1]
s=open(p).read()
def rep(old,new):
    global s
    assert s.count(old)==1, old
    s=s.replace(old,new)
rep('''	ParamNFSClients = "nfs.clients"
	ParamNFSServer  = "nfs.server"
)''','''	ParamNFSClients = "nfs.clients"
	ParamNFSServer  = "nfs.server"
	// nfs.uid, nfs.gid (decimal, 0-2147483647) and nfs.mode (octal
	// permission bits, at most 7777, e.g. "2775") are the owner and mode
	// the endpoint gives the volume's root directory before committing it
	// (wave9 N11): under root_squash nobody can chown it afterwards. Each
	// one unset is left as the create made it: root:root 0755, or a
	// clone's or restore's source's.
	ParamNFSUID  = "nfs.uid"
	ParamNFSGID  = "nfs.gid"
	ParamNFSMode = "nfs.mode"
)

// maxRootID is the endpoint's MaxRootID: the largest uid or gid
// Kubernetes accepts (runAsUser, runAsGroup).
const maxRootID = 1<<31 - 1''')
rep('''type params struct {
	sparse, reserve bool
	nfsClients      []string
	nfsServer       string
}''','''type params struct {
	sparse, reserve bool
	nfsClients      []string
	nfsServer       string
	// the root's owner and mode: nil leaves one as the create made it
	nfsUID, nfsGID, nfsMode *uint32
}

// rootParam parses nfs.uid or nfs.gid (decimal) or nfs.mode (octal digits
// only, no sign or "0o"), within its range.
func rootParam(k, v string) (*uint32, error) {
	base, limit, want := 10, uint64(maxRootID), "a decimal id, 0-2147483647"
	if k == ParamNFSMode {
		base, limit, want = 8, 0o7777, "octal permission bits, at most 7777 (e.g. 0775, 2775)"
	}
	n, err := strconv.ParseUint(v, base, 32)
	if err != nil || n > limit {
		return nil, invalid("parameter %s=%q: want %s", k, v, want)
	}
	u := uint32(n)
	return &u, nil
}''')
rep('''		case ParamNFSClients, ParamNFSServer:
			if !m.nfs {
				return params{}, invalid("parameter %s: the endpoint advertises no NFS server (zfs-endpoint -nfs-server), so this driver serves no NFS volumes", k)
			}
			if k == ParamNFSServer {
				p.nfsServer = v
			} else {
				// split as written: an empty entry reaches the endpoint,
				// which refuses it (an empty host exports to everyone)
				p.nfsClients = strings.Split(v, ",")
			}
			continue''','''		case ParamNFSClients, ParamNFSServer, ParamNFSUID, ParamNFSGID, ParamNFSMode:
			if !m.nfs {
				return params{}, invalid("parameter %s: the endpoint advertises no NFS server (zfs-endpoint -nfs-server), so this driver serves no NFS volumes", k)
			}
			var err error
			switch k {
			case ParamNFSServer:
				p.nfsServer = v
			case ParamNFSClients:
				// split as written: an empty entry reaches the endpoint,
				// which refuses it (an empty host exports to everyone)
				p.nfsClients = strings.Split(v, ",")
			case ParamNFSUID:
				p.nfsUID, err = rootParam(k, v)
			case ParamNFSGID:
				p.nfsGID, err = rootParam(k, v)
			case ParamNFSMode:
				p.nfsMode, err = rootParam(k, v)
			}
			if err != nil {
				return params{}, err
			}
			continue''')
rep('''		req.NfsExport = &pb.NFSExport{Clients: p.nfsClients, ReadOnly: readOnly(r.GetVolumeCapabilities()), Server: p.nfsServer}''','''		req.NfsExport = &pb.NFSExport{Clients: p.nfsClients, ReadOnly: readOnly(r.GetVolumeCapabilities()), Server: p.nfsServer,
			Uid: p.nfsUID, Gid: p.nfsGID, Mode: p.nfsMode}''')
open(p,'w').write(s)
