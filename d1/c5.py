p='internal/csi/controller.go'
s=open(p).read()
def rep(a,b):
    global s
    assert s.count(a)==1, a
    s=s.replace(a,b)
rep('''// StorageClass parameters understood here''','''// errLocalFilesystemDisabled is the reason mount access outside NFS mode
// is refused (ADR 0009).
const errLocalFilesystemDisabled = "mount access without NFS is disabled: the endpoint advertises no NFS server, so no node plugin can mount the volume; start zfs-endpoint with -nfs-server (NFS mode), or zfs-csi with -enable-local-filesystem to provision node-local filesystems anyway"

// StorageClass parameters understood here''')
rep('''// kindOf maps the request's access type: block to BLOCK, mount to
// FILESYSTEM. Mixed or missing access types are invalid, and so is block
// unless enabled, and always in NFS mode.
func kindOf(caps []*csi.VolumeCapability, enableBlock bool, m mode) (pb.VolumeKind, error) {''','''// kindOf maps the request's access type: block to BLOCK, mount to
// FILESYSTEM. Mixed or missing access types are invalid, and so is block
// unless enabled, and always in NFS mode, and mount outside NFS mode
// unless local filesystems are enabled.
func (c *Controller) kindOf(caps []*csi.VolumeCapability, m mode) (pb.VolumeKind, error) {''')
rep('''			if !enableBlock {
				return 0, invalid(errBlockDisabled)
			}
			k = pb.VolumeKind_VOLUME_KIND_BLOCK
		case vc.GetMount() != nil:
''','''			if !c.EnableBlock {
				return 0, invalid(errBlockDisabled)
			}
			k = pb.VolumeKind_VOLUME_KIND_BLOCK
		case vc.GetMount() != nil:
			if !m.nfs && !c.EnableLocalFilesystem {
				return 0, invalid(errLocalFilesystemDisabled)
			}
''')
rep('''// is never confirmed unless enabled, as CreateVolume never grants it. In
// NFS mode only an exported filesystem is mountable, and an export with
// only ro= clients takes no writer.
func capSupported(v *pb.Volume, vc *csi.VolumeCapability, enableBlock bool, m mode) string {''','''// is never confirmed unless enabled, as CreateVolume never grants it, and
// neither is mount access outside NFS mode unless local filesystems are. In
// NFS mode only an exported filesystem is mountable, and an export with
// only ro= clients takes no writer.
func (c *Controller) capSupported(v *pb.Volume, vc *csi.VolumeCapability, m mode) string {''')
rep('''		if !enableBlock {
			return errBlockDisabled
		}
		if kind''','''		if !c.EnableBlock {
			return errBlockDisabled
		}
		if kind''')
rep('''		if !m.nfs {
			if kind''','''		if !m.nfs {
			if !c.EnableLocalFilesystem {
				return errLocalFilesystemDisabled
			}
			if kind''')
rep('''kind, err := kindOf(r.GetVolumeCapabilities(), c.EnableBlock, m)''','''kind, err := c.kindOf(r.GetVolumeCapabilities(), m)''')
rep('''if msg := capSupported(got.GetVolume(), vc, c.EnableBlock, m); msg != "" {''','''if msg := c.capSupported(got.GetVolume(), vc, m); msg != "" {''')
open(p,'w').write(s)
