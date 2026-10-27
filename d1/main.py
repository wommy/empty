p='cmd/zfs-csi/main.go'
s=open(p).read()
old_start=s.index('func run(args []string, stdout io.Writer) error {')
old_end=s.index('	addr, err := endpointAddress(*parent, *endpointAddr)')
new='''// options are zfs-csi's flags.
type options struct {
	parent, endpointAddr, csiAddr, name string
	drain                               time.Duration
	enableBlock, enableLocalFilesystem  bool
	showVersion                         bool
}

// parseFlags parses the command line; flag.ErrHelp after -help.
func parseFlags(args []string) (options, error) {
	var o options
	fs := flag.NewFlagSet("zfs-csi", flag.ContinueOnError)
	fs.StringVar(&o.parent, "parent", "", "parent dataset the endpoint serves, e.g. tank/k8s/vols: dials "+unixsock.EndpointAddr("<parent>")+" (systemd-escaped, as zfs-endpoint@.service serves it)")
	fs.StringVar(&o.endpointAddr, "endpoint", "", "zfs-endpoint socket (unix:///absolute/path), instead of -parent")
	fs.StringVar(&o.csiAddr, "csi-address", "unix:///csi/csi.sock", "CSI socket to serve (unix:///path), in the pod's emptyDir")
	fs.StringVar(&o.name, "driver-name", zcsi.DefaultDriverName, "CSI driver name reported by GetPluginInfo")
	drain := fs.Duration("drain", 30*time.Second, "max time to drain in-flight calls on shutdown")
	fs.BoolVar(&o.enableBlock, "enable-block", false, "provision raw block volumes (zvols); off until a node plugin can attach them (ADR 0006)")
	fs.BoolVar(&o.enableLocalFilesystem, "enable-local-filesystem", false, "provision filesystem volumes when the endpoint advertises no NFS server (zfs-endpoint -nfs-server); off because no node plugin mounts them (ADR 0009)")
	fs.BoolVar(&o.showVersion, "version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	o.drain = *drain
	if fs.NArg() > 0 {
		return o, fmt.Errorf("unexpected arguments %q", fs.Args())
	}
	return o, nil
}

// controller is the CSI controller the flags configure, on ep.
func (o options) controller(ep pb.EndpointServiceClient) *zcsi.Controller {
	return &zcsi.Controller{EP: ep, EnableBlock: o.enableBlock, EnableLocalFilesystem: o.enableLocalFilesystem}
}

func run(args []string, stdout io.Writer) error {
	o, err := parseFlags(args)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if o.showVersion {
		_, err := fmt.Fprintln(stdout, version.Version)
		return err
	}
	if err != nil {
		return err
	}
'''
s=s[:old_start]+new+s[old_end:]
def rep(a,b):
    global s
    assert s.count(a)==1, a
    s=s.replace(a,b)
rep('''	addr, err := endpointAddress(*parent, *endpointAddr)
	if err != nil {
		return err
	}
	*endpointAddr = addr
''','''	endpointAddr, err := endpointAddress(o.parent, o.endpointAddr)
	if err != nil {
		return err
	}
''')
s=s.replace('*endpointAddr','endpointAddr').replace('*csiAddr','o.csiAddr').replace('*drain','o.drain').replace('*name','o.name')
rep('''	csi.RegisterControllerServer(srv, &zcsi.Controller{EP: ep, EnableBlock: *enableBlock})''','''	csi.RegisterControllerServer(srv, o.controller(ep))''')
rep('''"enable_block", *enableBlock, "version"''','''"enable_block", o.enableBlock, "enable_local_filesystem", o.enableLocalFilesystem, "version"''')
open(p,'w').write(s)
