package csi

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/wommy/empty/gen/zfsendpoint/v1"
)

// Controller implements csi.ControllerServer. Endpoint errors are returned
// unchanged: their codes are the CSI codes.
type Controller struct {
	csi.UnimplementedControllerServer
	EP pb.EndpointServiceClient
	// EnableBlock admits raw block access (zvols). Off by default: no node
	// plugin attaches a zvol to a node yet (ADR 0004, ADR 0006), so a block
	// PVC would bind and its pod never start. Off, CreateVolume refuses a
	// block capability with InvalidArgument and ValidateVolumeCapabilities
	// does not confirm one.
	EnableBlock bool
}

// errBlockDisabled is the reason a block capability is refused.
const errBlockDisabled = "block access is disabled: no node plugin can attach a zvol yet; start zfs-csi with -enable-block to provision block volumes anyway"

// StorageClass parameters understood here; each maps to an endpoint field.
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
const errNFSClientsRequired = ParamNFSClients + " is required: this driver serves volumes over NFS"

// k8sParamPrefix marks parameters the Kubernetes sidecars add or reserve
// (e.g. csi.storage.k8s.io/pvc/name with --extra-create-metadata).
const k8sParamPrefix = "csi.storage.k8s.io/"

// Volume context keys returned with every volume.
const (
	CtxDataset    = "dataset"
	CtxKind       = "kind" // "block" or "filesystem"
	CtxDevicePath = "devicePath"
	CtxMountpoint = "mountpoint"
	// csi-driver-nfs mounts "<server>:<share>" (ADR 0004). Both are present
	// iff the volume was created with an NFS export: server is the address
	// the endpoint recorded then, never recomputed (wave13 Q1), share the
	// mountpoint.
	CtxServer = "server"
	CtxShare  = "share"
)

func invalid(format string, a ...any) error {
	return status.Errorf(codes.InvalidArgument, format, a...)
}

// ---- capabilities ------------------------------------------------------------

// ControllerGetCapabilities translates what the endpoint reports. Cloning
// from a snapshot is implied by CREATE_DELETE_SNAPSHOT in CSI terms.
func (c *Controller) ControllerGetCapabilities(ctx context.Context, _ *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	got, err := c.EP.GetCapabilities(ctx, &pb.GetCapabilitiesRequest{})
	if err != nil {
		return nil, err
	}
	has := map[pb.Capability]bool{}
	for _, cp := range got.GetCapabilities() {
		has[cp] = true
	}
	var types []csi.ControllerServiceCapability_RPC_Type
	add := func(ok bool, t ...csi.ControllerServiceCapability_RPC_Type) {
		if ok {
			types = append(types, t...)
		}
	}
	add(has[pb.Capability_CAPABILITY_VOLUMES], csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME)
	add(has[pb.Capability_CAPABILITY_SNAPSHOTS], csi.ControllerServiceCapability_RPC_CREATE_DELETE_SNAPSHOT)
	add(has[pb.Capability_CAPABILITY_LIST], csi.ControllerServiceCapability_RPC_LIST_VOLUMES)
	add(has[pb.Capability_CAPABILITY_LIST] && has[pb.Capability_CAPABILITY_SNAPSHOTS], csi.ControllerServiceCapability_RPC_LIST_SNAPSHOTS)
	add(has[pb.Capability_CAPABILITY_CAPACITY], csi.ControllerServiceCapability_RPC_GET_CAPACITY)
	add(has[pb.Capability_CAPABILITY_CLONE_FROM_VOLUME], csi.ControllerServiceCapability_RPC_CLONE_VOLUME)
	add(has[pb.Capability_CAPABILITY_EXPAND], csi.ControllerServiceCapability_RPC_EXPAND_VOLUME)

	out := &csi.ControllerGetCapabilitiesResponse{}
	for _, t := range types {
		out.Capabilities = append(out.Capabilities, &csi.ControllerServiceCapability{
			Type: &csi.ControllerServiceCapability_Rpc{Rpc: &csi.ControllerServiceCapability_RPC{Type: t}},
		})
	}
	return out, nil
}

// ---- access modes and types --------------------------------------------------

// mode is the driver's mode, which the endpoint decides: NFS mode iff it
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
		default:
			return 0, invalid("volume capability needs an access type")
		}
		if kind != 0 && k != kind {
			return 0, invalid("block and mount access types in one request")
		}
		kind = k
	}
	return kind, nil
}

// capSupported reports whether an existing volume can serve vc. A zvol
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
		}
	default:
		return "volume capability needs an access type"
	}
	return ""
}

// ---- parameters --------------------------------------------------------------

type params struct {
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
		}
		b, err := strconv.ParseBool(v)
		if err != nil {
			return params{}, invalid("parameter %s=%q: not a boolean", k, v)
		}
		*dst = b
	}
	return p, nil
}

func noParams(in map[string]string) error {
	for k := range in {
		if !strings.HasPrefix(k, k8sParamPrefix) {
			return invalid("unsupported parameter %q", k)
		}
	}
	return nil
}

// ---- conversions -------------------------------------------------------------

func i64(n uint64) int64 {
	if n > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(n)
}

func capacityRange(r *csi.CapacityRange) (required, limit uint64, err error) {
	if r.GetRequiredBytes() < 0 || r.GetLimitBytes() < 0 {
		return 0, 0, invalid("capacity_range must not be negative")
	}
	required, limit = uint64(r.GetRequiredBytes()), uint64(r.GetLimitBytes())
	if limit > 0 && required > limit {
		return 0, 0, invalid("required_bytes %d exceeds limit_bytes %d", required, limit)
	}
	return required, limit, nil
}

func volumeOut(v *pb.Volume) *csi.Volume {
	out := &csi.Volume{
		VolumeId:      v.GetId(),
		CapacityBytes: i64(v.GetCapacityBytes()),
		VolumeContext: map[string]string{CtxDataset: v.GetDataset()},
	}
	if v.GetKind() == pb.VolumeKind_VOLUME_KIND_BLOCK {
		out.VolumeContext[CtxKind] = "block"
	} else {
		out.VolumeContext[CtxKind] = "filesystem"
	}
	if p := v.GetDevicePath(); p != "" {
		out.VolumeContext[CtxDevicePath] = p
	}
	if p := v.GetMountpoint(); p != "" {
		out.VolumeContext[CtxMountpoint] = p
	}
	if s := v.GetNfs().GetServer(); s != "" && v.GetMountpoint() != "" {
		out.VolumeContext[CtxServer], out.VolumeContext[CtxShare] = s, v.GetMountpoint()
	}
	switch v.GetSourceType() {
	case "snapshot":
		out.ContentSource = &csi.VolumeContentSource{Type: &csi.VolumeContentSource_Snapshot{
			Snapshot: &csi.VolumeContentSource_SnapshotSource{SnapshotId: v.GetSourceId()},
		}}
	case "volume":
		out.ContentSource = &csi.VolumeContentSource{Type: &csi.VolumeContentSource_Volume{
			Volume: &csi.VolumeContentSource_VolumeSource{VolumeId: v.GetSourceId()},
		}}
	}
	return out
}

// k8sContextPrefixes mark volume_context keys Kubernetes adds to a PV's
// attributes itself (external-provisioner's
// storage.kubernetes.io/csiProvisionerIdentity): never issued here, and
// not the caller's claim about the volume.
var k8sContextPrefixes = []string{"storage.kubernetes.io/", k8sParamPrefix}

// contextMismatch says why got is not (a subset of) the volume_context
// issued for the volume, or "" when every key it names has the issued
// value.
func contextMismatch(issued, got map[string]string) string {
	for k, v := range got {
		if slices.ContainsFunc(k8sContextPrefixes, func(p string) bool { return strings.HasPrefix(k, p) }) {
			continue
		}
		if want, ok := issued[k]; !ok || want != v {
			return fmt.Sprintf("volume_context %s=%q is not the volume's (%q)", k, v, want)
		}
	}
	return ""
}

func snapshotOut(s *pb.Snapshot) *csi.Snapshot {
	return &csi.Snapshot{
		SnapshotId:     s.GetId(),
		SourceVolumeId: s.GetSourceVolumeId(),
		SizeBytes:      i64(s.GetSizeBytes()),
		CreationTime:   &timestamppb.Timestamp{Seconds: s.GetCreationUnix()},
		ReadyToUse:     s.GetReadyToUse(),
	}
}

// ---- volumes -----------------------------------------------------------------

func (c *Controller) CreateVolume(ctx context.Context, r *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	if r.GetName() == "" {
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
	}
	src := r.GetVolumeContentSource()
	switch {
	case src == nil:
	// an empty id would reach the endpoint as "no source" (proto3 reads an
	// empty oneof string back as unset): a restore or clone would silently
	// provision an empty volume
	case src.GetSnapshot() != nil:
		if src.GetSnapshot().GetSnapshotId() == "" {
			return nil, invalid("volume_content_source.snapshot.snapshot_id is required")
		}
		req.Source = &pb.CreateVolumeRequest_SourceSnapshotId{SourceSnapshotId: src.GetSnapshot().GetSnapshotId()}
	case src.GetVolume() != nil:
		if src.GetVolume().GetVolumeId() == "" {
			return nil, invalid("volume_content_source.volume.volume_id is required")
		}
		req.Source = &pb.CreateVolumeRequest_SourceVolumeId{SourceVolumeId: src.GetVolume().GetVolumeId()}
	default:
		return nil, invalid("unsupported volume_content_source")
	}
	res, err := c.EP.CreateVolume(ctx, req)
	if err != nil {
		return nil, err
	}
	return &csi.CreateVolumeResponse{Volume: volumeOut(res.GetVolume())}, nil
}

func (c *Controller) DeleteVolume(ctx context.Context, r *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	if r.GetVolumeId() == "" {
		return nil, invalid("volume_id is required")
	}
	if _, err := c.EP.DeleteVolume(ctx, &pb.DeleteVolumeRequest{VolumeId: r.GetVolumeId()}); err != nil {
		return nil, err
	}
	return &csi.DeleteVolumeResponse{}, nil
}

func (c *Controller) ValidateVolumeCapabilities(ctx context.Context, r *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	if r.GetVolumeId() == "" {
		return nil, invalid("volume_id is required")
	}
	if len(r.GetVolumeCapabilities()) == 0 {
		return nil, invalid("volume_capabilities are required")
	}
	got, err := c.EP.GetVolume(ctx, &pb.GetVolumeRequest{VolumeId: r.GetVolumeId()})
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
	}
	// Parameters are only checked for being understood, not against the
	// volume: pb.Volume carries no sparse/reserve, so a sparse=true repeat
	// of a thick volume (which CreateVolume calls AlreadyExists) is still
	// confirmed. Comparing them needs those fields in the endpoint API
	// (a proto change), deferred.
	if _, err := parseParams(r.GetParameters(), m); err != nil {
		return &csi.ValidateVolumeCapabilitiesResponse{Message: status.Convert(err).Message()}, nil
	}
	return &csi.ValidateVolumeCapabilitiesResponse{Confirmed: &csi.ValidateVolumeCapabilitiesResponse_Confirmed{
		VolumeContext:      r.GetVolumeContext(),
		VolumeCapabilities: r.GetVolumeCapabilities(),
		Parameters:         r.GetParameters(),
	}}, nil
}

func (c *Controller) ListVolumes(ctx context.Context, r *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
	res, err := c.EP.ListVolumes(ctx, &pb.ListVolumesRequest{MaxEntries: r.GetMaxEntries(), StartingToken: r.GetStartingToken()})
	if err != nil {
		return nil, err
	}
	out := &csi.ListVolumesResponse{NextToken: res.GetNextToken()}
	for _, v := range res.GetVolumes() {
		out.Entries = append(out.Entries, &csi.ListVolumesResponse_Entry{Volume: volumeOut(v)})
	}
	return out, nil
}

func (c *Controller) GetCapacity(ctx context.Context, _ *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	res, err := c.EP.GetCapacity(ctx, &pb.GetCapacityRequest{})
	if err != nil {
		return nil, err
	}
	return &csi.GetCapacityResponse{AvailableCapacity: i64(res.GetAvailableBytes())}, nil
}

// ControllerExpandVolume grows the dataset or zvol. Only a zvol that carries
// a filesystem needs the node to grow that filesystem too: a raw block
// volume is resized by the controller alone, and a ZFS filesystem sees a new
// refquota at once. volume_capability is optional, so a zvol is assumed to
// carry a filesystem unless the capability says raw block: a needless node
// expansion is a no-op, a missed one leaves the filesystem small.
// (democratic-csi reports node expansion for raw block as well.)
func (c *Controller) ControllerExpandVolume(ctx context.Context, r *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	if r.GetVolumeId() == "" {
		return nil, invalid("volume_id is required")
	}
	if r.GetCapacityRange() == nil {
		return nil, invalid("capacity_range is required")
	}
	required, limit, err := capacityRange(r.GetCapacityRange())
	if err != nil {
		return nil, err
	}
	if required == 0 && limit == 0 {
		return nil, invalid("capacity_range needs required_bytes or limit_bytes")
	}
	if vc := r.GetVolumeCapability(); vc != nil {
		// checked against the volume before anything is resized, by the
		// rule ValidateVolumeCapabilities confirms with
		got, err := c.EP.GetVolume(ctx, &pb.GetVolumeRequest{VolumeId: r.GetVolumeId()})
		if err != nil {
			return nil, err
		}
		m, err := c.modeOf(ctx)
		if err != nil {
			return nil, err
		}
		if msg := capSupported(got.GetVolume(), vc, c.EnableBlock, m); msg != "" {
			return nil, invalid("volume_capability: %s", msg)
		}
	}
	res, err := c.EP.ExpandVolume(ctx, &pb.ExpandVolumeRequest{VolumeId: r.GetVolumeId(), CapacityBytes: required, LimitBytes: limit})
	if err != nil {
		return nil, err
	}
	v := res.GetVolume()
	return &csi.ControllerExpandVolumeResponse{
		CapacityBytes:         i64(v.GetCapacityBytes()),
		NodeExpansionRequired: v.GetKind() == pb.VolumeKind_VOLUME_KIND_BLOCK && r.GetVolumeCapability().GetBlock() == nil,
	}, nil
}

// ---- snapshots ---------------------------------------------------------------

func (c *Controller) CreateSnapshot(ctx context.Context, r *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
	if r.GetName() == "" {
		return nil, invalid("name is required")
	}
	if r.GetSourceVolumeId() == "" {
		return nil, invalid("source_volume_id is required")
	}
	if err := noParams(r.GetParameters()); err != nil {
		return nil, err
	}
	res, err := c.EP.CreateSnapshot(ctx, &pb.CreateSnapshotRequest{Name: r.GetName(), SourceVolumeId: r.GetSourceVolumeId()})
	if err != nil {
		return nil, err
	}
	return &csi.CreateSnapshotResponse{Snapshot: snapshotOut(res.GetSnapshot())}, nil
}

func (c *Controller) DeleteSnapshot(ctx context.Context, r *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
	if r.GetSnapshotId() == "" {
		return nil, invalid("snapshot_id is required")
	}
	if _, err := c.EP.DeleteSnapshot(ctx, &pb.DeleteSnapshotRequest{SnapshotId: r.GetSnapshotId()}); err != nil {
		return nil, err
	}
	return &csi.DeleteSnapshotResponse{}, nil
}

func (c *Controller) ListSnapshots(ctx context.Context, r *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	res, err := c.EP.ListSnapshots(ctx, &pb.ListSnapshotsRequest{
		SnapshotId: r.GetSnapshotId(), SourceVolumeId: r.GetSourceVolumeId(),
		MaxEntries: r.GetMaxEntries(), StartingToken: r.GetStartingToken(),
	})
	if err != nil {
		return nil, err
	}
	out := &csi.ListSnapshotsResponse{NextToken: res.GetNextToken()}
	for _, s := range res.GetSnapshots() {
		out.Entries = append(out.Entries, &csi.ListSnapshotsResponse_Entry{Snapshot: snapshotOut(s)})
	}
	return out, nil
}
