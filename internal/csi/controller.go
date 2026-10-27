package csi

import (
	"context"
	"fmt"
	"math"
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
}

// StorageClass parameters understood here; each maps to an endpoint field.
const (
	ParamSparse  = "sparse"  // BLOCK: zvol without refreservation
	ParamReserve = "reserve" // FILESYSTEM: refreservation = capacity
)

// k8sParamPrefix marks parameters the Kubernetes sidecars add or reserve
// (e.g. csi.storage.k8s.io/pvc/name with --extra-create-metadata).
const k8sParamPrefix = "csi.storage.k8s.io/"

// Volume context keys returned with every volume.
const (
	CtxDataset    = "dataset"
	CtxKind       = "kind" // "block" or "filesystem"
	CtxDevicePath = "devicePath"
	CtxMountpoint = "mountpoint"
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

// No export or attach exists yet, so a volume is usable from one node only.
func modeSupported(m csi.VolumeCapability_AccessMode_Mode) bool {
	return m == csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER ||
		m == csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY
}

// kindOf maps the request's access type: block to BLOCK, mount to
// FILESYSTEM. Mixed or missing access types are invalid.
func kindOf(caps []*csi.VolumeCapability) (pb.VolumeKind, error) {
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
			k = pb.VolumeKind_VOLUME_KIND_BLOCK
		case vc.GetMount() != nil:
			if fs := vc.GetMount().GetFsType(); fs != "" && fs != "zfs" {
				return 0, invalid("fs_type %q: mount volumes are zfs filesystems", fs)
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

// capSupported reports whether an existing volume of kind can serve vc. A
// zvol may also carry a filesystem (mount access, e.g. one adopted from the
// baseline); a ZFS filesystem can never be a raw block device.
func capSupported(kind pb.VolumeKind, vc *csi.VolumeCapability) string {
	if !modeSupported(vc.GetAccessMode().GetMode()) {
		return fmt.Sprintf("access mode %v is not supported", vc.GetAccessMode().GetMode())
	}
	switch {
	case vc.GetBlock() != nil:
		if kind != pb.VolumeKind_VOLUME_KIND_BLOCK {
			return "a filesystem volume has no block access"
		}
	case vc.GetMount() != nil:
		if fs := vc.GetMount().GetFsType(); kind != pb.VolumeKind_VOLUME_KIND_BLOCK && fs != "" && fs != "zfs" {
			return fmt.Sprintf("fs_type %q: filesystem volumes are zfs", fs)
		}
	default:
		return "volume capability needs an access type"
	}
	return ""
}

// ---- parameters --------------------------------------------------------------

type params struct{ sparse, reserve bool }

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
	kind, err := kindOf(r.GetVolumeCapabilities())
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
	}
	src := r.GetVolumeContentSource()
	switch {
	case src == nil:
	case src.GetSnapshot() != nil:
		req.Source = &pb.CreateVolumeRequest_SourceSnapshotId{SourceSnapshotId: src.GetSnapshot().GetSnapshotId()}
	case src.GetVolume() != nil:
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
	for _, vc := range r.GetVolumeCapabilities() {
		if msg := capSupported(got.GetVolume().GetKind(), vc); msg != "" {
			return &csi.ValidateVolumeCapabilitiesResponse{Message: msg}, nil
		}
	}
	if _, err := parseParams(r.GetParameters()); err != nil {
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
