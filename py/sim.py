import sys
p = sys.argv[1]
s = open(p).read()


def rep(a, b):
    global s
    assert s.count(a) == 1, a
    s = s.replace(a, b)


rep('''var defaults = map[string]string{
	"compression": "on", "refquota": "0", "quota": "0", "reservation": "0",
	"refreservation": "0", "volblocksize": "16384", "used": "0",
	"sharenfs": "off", "sharesmb": "off",
}''', '''// defaults are zfs_prop.c's registered defaults (Linux values where the
// platform matters: relatime on, acltype off).
var defaults = map[string]string{
	"compression": "on", "refquota": "0", "quota": "0", "reservation": "0",
	"refreservation": "0", "volblocksize": "16384", "used": "0",
	"sharenfs": "off", "sharesmb": "off",
	"recordsize": "131072", "atime": "on", "relatime": "on", "sync": "standard",
	"logbias": "latency", "dedup": "off", "xattr": "sa", "acltype": "off",
	"snapdir": "hidden", "copies": "1",
}''')
rep('''	"compression":    {zfs.TypeFilesystem, zfs.TypeVolume},
''', '''	"compression":    {zfs.TypeFilesystem, zfs.TypeVolume},
	"recordsize":     {zfs.TypeFilesystem},
	"atime":          {zfs.TypeFilesystem},
	"relatime":       {zfs.TypeFilesystem},
	"sync":           {zfs.TypeFilesystem, zfs.TypeVolume},
	"logbias":        {zfs.TypeFilesystem, zfs.TypeVolume},
	"dedup":          {zfs.TypeFilesystem, zfs.TypeVolume},
	"copies":         {zfs.TypeFilesystem, zfs.TypeVolume},
	"xattr":          {zfs.TypeFilesystem, zfs.TypeSnapshot},
	"acltype":        {zfs.TypeFilesystem, zfs.TypeSnapshot},
	"snapdir":        {zfs.TypeFilesystem},
''')
rep('''func known(prop string) bool {''', '''// misfit reports a native property that does not exist on typ
// (libzfs zfs_valid_proplist: "does not apply to datasets of this type").
func misfit(props map[string]string, typ zfs.Type) (string, bool) {
	for k := range props {
		if types, ok := applies[k]; ok && !slices.Contains(types, typ) {
			return k, true
		}
	}
	return "", false
}

func known(prop string) bool {''')
rep('''	case "creation":
		return "1700000000", "-", true
	}''', '''	case "creation":
		return "1700000000", "-", true
	case "volblocksize":
		// zvol_get_stats reports the object's block size through
		// dsl_prop_nvlist_add_uint64: source "default" when it equals the
		// default, else none ("-"); never "local"
		v := s.volblocksize(n)
		if v == defaults["volblocksize"] {
			return v, "default", true
		}
		return v, "-", true
	}''')
rep('''	n := &node{typ: zfs.TypeFilesystem, props: props}
	if volsize != "" {''', '''	typ := zfs.TypeFilesystem
	if volsize != "" {
		typ = zfs.TypeVolume
	}
	if k, bad := misfit(props, typ); bad {
		return exit("cannot create '%s': '%s' does not apply to datasets of this type", name, k)
	}
	n := &node{typ: zfs.TypeFilesystem, props: props}
	if volsize != "" {''')
rep('''	typ := s.ds[parent(snap)].typ
	n := &node{typ: typ, props: props, origin: snap}''', '''	typ := s.ds[parent(snap)].typ
	if _, ok := props["volblocksize"]; ok && typ == zfs.TypeVolume {
		// zfs_valid_proplist with the origin's handle: set-once is readonly
		return exit("cannot create '%s': 'volblocksize' is readonly", name)
	}
	if k, bad := misfit(props, typ); bad {
		return exit("cannot create '%s': '%s' does not apply to datasets of this type", name, k)
	}
	n := &node{typ: typ, props: props, origin: snap}''')
rep('''		if p[0] == "volsize" && n.typ != zfs.TypeVolume {
			return exit("cannot set property for '%s': 'volsize' does not apply to datasets of this type", name)
		}''', '''		if k, bad := misfit(map[string]string{p[0]: p[1]}, n.typ); bad {
			return exit("cannot set property for '%s': '%s' does not apply to datasets of this type", name, k)
		}
		if p[0] == "volblocksize" {
			return exit("cannot set property for '%s': 'volblocksize' is readonly", name)
		}''')
open(p, 'w').write(s)
