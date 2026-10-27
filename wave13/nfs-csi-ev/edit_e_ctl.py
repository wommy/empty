import sys
p = sys.argv[1] + '/internal/csi/controller.go'
s = open(p).read()


def sub(old, new):
    global s
    if old not in s:
        raise SystemExit('missing: ' + old[:90])
    s = s.replace(old, new, 1)


sub('''// StorageClass parameters understood here; each maps to an endpoint field.
const (
	ParamSparse  = "sparse"  // BLOCK: zvol without refreservation
	ParamReserve = "reserve" // FILESYSTEM: refreservation = capacity
)''', '''// StorageClass parameters understood here; each maps to an endpoint field.
const (
	ParamSparse  = "sparse"  // BLOCK: zvol without refreservation
	ParamReserve = "reserve" // FILESYSTEM: refreservation = capacity
	// NFS mode only (csi-nfs-design §3). nfs.clients is required there: a
	// comma-separated list of IPv4 networks, each with its /N
	// ("10.20.0.0/24,10.20.1.0/24"), checked by the endpoint and never
	// masked. nfs.server is one of the endpoint's -nfs-server addresses;
	// default the first.
	ParamNFSClients = "nfs.clients"
	ParamNFSServer  = "nfs.server"
)

// errNFSClientsRequired is wave13 Q2: csi-driver-nfs cannot mount a volume
// without an export, so in NFS mode none is created.
const errNFSClientsRequired = ParamNFSClients + " is required: this driver serves volumes over NFS"''')

sub('''	CtxDevicePath = "devicePath"
	CtxMountpoint = "mountpoint"
)''', '''	CtxDevicePath = "devicePath"
	CtxMountpoint = "mountpoint"
	// csi-driver-nfs mounts "<server>:<share>" (ADR 0004). Both are present
	// iff the volume was created with an NFS export: server is the address
	// the endpoint recorded then, never recomputed (wave13 Q1), share the
	// mountpoint.
	CtxServer = "server"
	CtxShare  = "share"
)''')

# modes
sub('''// No export or attach exists yet, so a volume is usable from one node only.
func modeSupported(m csi.VolumeCapability_AccessMode_Mode) bool {
	return m == csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER ||
		m == csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY
}

// kindOf maps the request's access type: block to BLOCK, mount to
// FILESYSTEM. Mixed or missing access types are invalid, and so is block
// unless enabled.
func kindOf(caps []*csi.VolumeCapability, enableBlock bool) (pb.VolumeKind, error) {
	if len(caps) == 0 {
		return 0, invalid("volume_capabilities are required")
	}
	var kind pb.VolumeKind
	for _, vc := range caps {
		if !modeSupported(vc.GetAccessMode().GetMode()) {
			return 0, invalid("access mode %v is not supported", vc.GetAccessMode().GetMode())
		}
		var k pb.VolumeKind
		switch {
		case vc.GetBlock() != nil:
			if !enableBlock {
				return 0, invalid(errBlockDisabled)
			}
			k = pb.VolumeKind_VOLUME_KIND_BLOCK
		case vc.GetMount() != nil:
			if fs := vc.GetMount().GetFsType(); fs != "" && fs != "zfs" {
				return 0, invalid("fs_type %q: mount volumes are zfs filesystems", fs)
			}
			k = pb.VolumeKind_VOLUME_KIND_FILESYSTEM
		default:''', '''// mode is the driver's mode, which the endpoint decides: NFS mode iff it
// advertises NFS servers (wave13 Q2), so no controller flag can disagree
// with it.
type mode struct {
	nfs     bool
	servers []string
}

// modeOf asks the endpoint, on every call that depends on it: a restarted
// endpoint may advertise other servers.
func (c *Controller) modeOf(ctx context.Context) (mode, error) {
	got, err := c.EP.GetCapabilities(ctx, &pb.GetCapabilitiesRequest{})
	if err != nil {
		return mode{}, err
	}
	return mode{nfs: len(got.GetNfsServers()) > 0, servers: got.GetNfsServers()}, nil
}

// errNFSBlock: csi-driver-nfs cannot publish a zvol (ADR 0004).
const errNFSBlock = "block access: this driver serves volumes over NFS, which cannot carry a zvol"

// Without NFS no export or attach exists, so a volume is usable from one
// node only. Over NFS a filesystem takes every mode: the export is a
// network, and SINGLE_NODE_* is advisory (csi-nfs-design §7).
func (m mode) supports(am csi.VolumeCapability_AccessMode_Mode) bool {
	switch am {
	case csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER, csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY:
		return true
	case csi.VolumeCapability_AccessMode_UNKNOWN:
		return false
	}
	return m.nfs && csi.VolumeCapability_AccessMode_Mode_name[int32(am)] != ""
}

// fsType is the mount fs_type a filesystem volume serves: zfs, or nfs in
// NFS mode ("" is either).
func (m mode) fsType() string {
	if m.nfs {
		return "nfs"
	}
	return "zfs"
}

// readOnly reports whether every mode is a reader-only one: the export is
// then ro= (csi-nfs-design §3).
func readOnly(caps []*csi.VolumeCapability) bool {
	for _, vc := range caps {
		switch vc.GetAccessMode().GetMode() {
		case csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY, csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY:
		default:
			return false
		}
	}
	return true
}

// kindOf maps the request's access type: block to BLOCK, mount to
// FILESYSTEM. Mixed or missing access types are invalid, and so is block
// unless enabled, and always in NFS mode.
func kindOf(caps []*csi.VolumeCapability, enableBlock bool, m mode) (pb.VolumeKind, error) {
	if len(caps) == 0 {
		return 0, invalid("volume_capabilities are required")
	}
	var kind pb.VolumeKind
	for _, vc := range caps {
		if !m.supports(vc.GetAccessMode().GetMode()) {
			return 0, invalid("access mode %v is not supported", vc.GetAccessMode().GetMode())
		}
		var k pb.VolumeKind
		switch {
		case vc.GetBlock() != nil:
			if m.nfs {
				return 0, invalid(errNFSBlock)
			}
			if !enableBlock {
				return 0, invalid(errBlockDisabled)
			}
			k = pb.VolumeKind_VOLUME_KIND_BLOCK
		case vc.GetMount() != nil:
			if fs := vc.GetMount().GetFsType(); fs != "" && fs != m.fsType() {
				return 0, invalid("fs_type %q: mount volumes are %s", fs, m.fsType())
			}
			k = pb.VolumeKind_VOLUME_KIND_FILESYSTEM
		default:''')

sub('''// capSupported reports whether an existing volume of kind can serve vc. A
// zvol may also carry a filesystem (mount access, e.g. one adopted from the
// baseline); a ZFS filesystem can never be a raw block device. Block access
// is never confirmed unless enabled, as CreateVolume never grants it.
func capSupported(kind pb.VolumeKind, vc *csi.VolumeCapability, enableBlock bool) string {
	if !modeSupported(vc.GetAccessMode().GetMode()) {
		return fmt.Sprintf("access mode %v is not supported", vc.GetAccessMode().GetMode())
	}
	switch {
	case vc.GetBlock() != nil:
		if !enableBlock {
			return errBlockDisabled
		}
		if kind != pb.VolumeKind_VOLUME_KIND_BLOCK {
			return "a filesystem volume has no block access"
		}
	case vc.GetMount() != nil:
		if fs := vc.GetMount().GetFsType(); kind != pb.VolumeKind_VOLUME_KIND_BLOCK && fs != "" && fs != "zfs" {
			return fmt.Sprintf("fs_type %q: filesystem volumes are zfs", fs)
		}''', '''// capSupported reports whether an existing volume can serve vc. A zvol
// may also carry a filesystem (mount access, e.g. one adopted from the
// baseline); a ZFS filesystem can never be a raw block device. Block access
// is never confirmed unless enabled, as CreateVolume never grants it. In
// NFS mode only an exported filesystem is mountable, and an export with
// only ro= clients takes no writer.
func capSupported(v *pb.Volume, vc *csi.VolumeCapability, enableBlock bool, m mode) string {
	kind := v.GetKind()
	if !m.supports(vc.GetAccessMode().GetMode()) {
		return fmt.Sprintf("access mode %v is not supported", vc.GetAccessMode().GetMode())
	}
	switch {
	case vc.GetBlock() != nil:
		if m.nfs {
			return errNFSBlock
		}
		if !enableBlock {
			return errBlockDisabled
		}
		if kind != pb.VolumeKind_VOLUME_KIND_BLOCK {
			return "a filesystem volume has no block access"
		}
	case vc.GetMount() != nil:
		fs := vc.GetMount().GetFsType()
		if !m.nfs {
			if kind != pb.VolumeKind_VOLUME_KIND_BLOCK && fs != "" && fs != "zfs" {
				return fmt.Sprintf("fs_type %q: filesystem volumes are zfs", fs)
			}
			break
		}
		if fs != "" && fs != "nfs" {
			return fmt.Sprintf("fs_type %q: volumes are mounted over nfs", fs)
		}
		n := v.GetNfs()
		if kind == pb.VolumeKind_VOLUME_KIND_BLOCK || n.GetServer() == "" {
			return "the volume was not created with an NFS export: csi-driver-nfs cannot mount it"
		}
		if !readOnly([]*csi.VolumeCapability{vc}) && n.GetReadOnly() {
			return fmt.Sprintf("access mode %v writes, but the volume is exported read-only", vc.GetAccessMode().GetMode())
		}''')

# params
sub('''type params struct{ sparse, reserve bool }

// parseParams accepts only parameters with an endpoint field; anything else
// is refused rather than silently dropped.
func parseParams(in map[string]string) (params, error) {
	var p params
	for k, v := range in {
		if strings.HasPrefix(k, k8sParamPrefix) {
			continue
		}
		var dst *bool
		switch k {
		case ParamSparse:
			dst = &p.sparse
		case ParamReserve:
			dst = &p.reserve
		default:
			return params{}, invalid("unsupported parameter %q", k)
		}''', '''type params struct {
	sparse, reserve bool
	nfsClients      []string
	nfsServer       string
}

// parseParams accepts only parameters with an endpoint field; anything else
// is refused rather than silently dropped. In NFS mode nfs.clients is
// required, and refused first when missing (wave13 Q2); outside it the
// nfs.* keys are refused for the mode, not as unknown.
func parseParams(in map[string]string, m mode) (params, error) {
	var p params
	if _, ok := in[ParamNFSClients]; m.nfs && !ok {
		return params{}, invalid(errNFSClientsRequired)
	}
	for k, v := range in {
		if strings.HasPrefix(k, k8sParamPrefix) {
			continue
		}
		var dst *bool
		switch k {
		case ParamSparse:
			dst = &p.sparse
		case ParamReserve:
			dst = &p.reserve
		case ParamNFSClients, ParamNFSServer:
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
			continue
		default:
			return params{}, invalid("unsupported parameter %q", k)
		}''')

sub('''	if p := v.GetMountpoint(); p != "" {
		out.VolumeContext[CtxMountpoint] = p
	}
	switch v.GetSourceType() {''', '''	if p := v.GetMountpoint(); p != "" {
		out.VolumeContext[CtxMountpoint] = p
	}
	if s := v.GetNfs().GetServer(); s != "" && v.GetMountpoint() != "" {
		out.VolumeContext[CtxServer], out.VolumeContext[CtxShare] = s, v.GetMountpoint()
	}
	switch v.GetSourceType() {''')

sub('''	if r.GetName() == "" {
		return nil, invalid("name is required")
	}
	kind, err := kindOf(r.GetVolumeCapabilities(), c.EnableBlock)
	if err != nil {
		return nil, err
	}
	required, limit, err := capacityRange(r.GetCapacityRange())
	if err != nil {
		return nil, err
	}
	p, err := parseParams(r.GetParameters())
	if err != nil {
		return nil, err
	}
	req := &pb.CreateVolumeRequest{
		Name: r.GetName(), Kind: kind,
		CapacityBytes: required, LimitBytes: limit,
		Sparse: p.sparse, Reserve: p.reserve,
	}''', '''	if r.GetName() == "" {
		return nil, invalid("name is required")
	}
	m, err := c.modeOf(ctx)
	if err != nil {
		return nil, err
	}
	kind, err := kindOf(r.GetVolumeCapabilities(), c.EnableBlock, m)
	if err != nil {
		return nil, err
	}
	required, limit, err := capacityRange(r.GetCapacityRange())
	if err != nil {
		return nil, err
	}
	p, err := parseParams(r.GetParameters(), m)
	if err != nil {
		return nil, err
	}
	req := &pb.CreateVolumeRequest{
		Name: r.GetName(), Kind: kind,
		CapacityBytes: required, LimitBytes: limit,
		Sparse: p.sparse, Reserve: p.reserve,
	}
	if m.nfs {
		req.NfsExport = &pb.NFSExport{Clients: p.nfsClients, ReadOnly: readOnly(r.GetVolumeCapabilities()), Server: p.nfsServer}
	}''')

sub('''	got, err := c.EP.GetVolume(ctx, &pb.GetVolumeRequest{VolumeId: r.GetVolumeId()})
	if err != nil {
		return nil, err
	}
	for _, vc := range r.GetVolumeCapabilities() {
		if msg := capSupported(got.GetVolume().GetKind(), vc, c.EnableBlock); msg != "" {
			return &csi.ValidateVolumeCapabilitiesResponse{Message: msg}, nil
		}
	}
	// the context must be the one CreateVolume issued for this volume:
	// confirming any context echoed the caller's claims back
	if msg := contextMismatch(volumeOut(got.GetVolume()).GetVolumeContext(), r.GetVolumeContext()); msg != "" {
		return &csi.ValidateVolumeCapabilitiesResponse{Message: msg}, nil
	}''', '''	got, err := c.EP.GetVolume(ctx, &pb.GetVolumeRequest{VolumeId: r.GetVolumeId()})
	if err != nil {
		return nil, err
	}
	m, err := c.modeOf(ctx)
	if err != nil {
		return nil, err
	}
	for _, vc := range r.GetVolumeCapabilities() {
		if msg := capSupported(got.GetVolume(), vc, c.EnableBlock, m); msg != "" {
			return &csi.ValidateVolumeCapabilitiesResponse{Message: msg}, nil
		}
	}
	// the context must be the one CreateVolume issued for this volume,
	// key by key (Kubernetes adds its own): confirming any context echoed
	// the caller's claims back
	if msg := contextMismatch(volumeOut(got.GetVolume()).GetVolumeContext(), r.GetVolumeContext()); msg != "" {
		return &csi.ValidateVolumeCapabilitiesResponse{Message: msg}, nil
	}
	// its PVs still mount the recorded server, and no new one will be
	// issued for it (wave13 Q1: reconcile reports it too)
	if s := got.GetVolume().GetNfs().GetServer(); m.nfs && s != "" && !slices.Contains(m.servers, s) {
		return &csi.ValidateVolumeCapabilitiesResponse{Message: fmt.Sprintf("the volume's NFS server %s is no longer advertised by the endpoint (%s)", s, strings.Join(m.servers, ", "))}, nil
	}''')

sub('''	if _, err := parseParams(r.GetParameters()); err != nil {''', '''	if _, err := parseParams(r.GetParameters(), m); err != nil {''')

sub('''		got, err := c.EP.GetVolume(ctx, &pb.GetVolumeRequest{VolumeId: r.GetVolumeId()})
		if err != nil {
			return nil, err
		}
		if msg := capSupported(got.GetVolume().GetKind(), vc, c.EnableBlock); msg != "" {''', '''		got, err := c.EP.GetVolume(ctx, &pb.GetVolumeRequest{VolumeId: r.GetVolumeId()})
		if err != nil {
			return nil, err
		}
		m, err := c.modeOf(ctx)
		if err != nil {
			return nil, err
		}
		if msg := capSupported(got.GetVolume(), vc, c.EnableBlock, m); msg != "" {''')
open(p, 'w').write(s)
