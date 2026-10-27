// Package server exposes internal/endpoint over gRPC
// (api/zfsendpoint/v1). It only translates: every decision lives in the
// endpoint core.
package server

import (
	"context"
	"math"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/wommy/empty/gen/zfsendpoint/v1"
	"github.com/wommy/empty/internal/endpoint"
)

// Version is reported by GetCapabilities.
var Version = "dev"

// Server implements pb.EndpointServiceServer.
type Server struct {
	pb.UnimplementedEndpointServiceServer
	EP     *endpoint.Endpoint
	Parent string
}

// grpcErr maps endpoint errors to gRPC status. endpoint.Code values are the
// gRPC codes by construction.
func grpcErr(err error) error {
	if err == nil {
		return nil
	}
	return status.Error(codes.Code(endpoint.CodeOf(err)), err.Error())
}

func kindIn(k pb.VolumeKind) endpoint.VolumeKind {
	if k == pb.VolumeKind_VOLUME_KIND_BLOCK {
		return endpoint.Block
	}
	return endpoint.Filesystem
}

func kindOut(k endpoint.VolumeKind) pb.VolumeKind {
	if k == endpoint.Block {
		return pb.VolumeKind_VOLUME_KIND_BLOCK
	}
	return pb.VolumeKind_VOLUME_KIND_FILESYSTEM
}

func volumeOut(v endpoint.Volume) *pb.Volume {
	return &pb.Volume{
		Id: v.ID, Name: v.Name, Dataset: v.Dataset, Kind: kindOut(v.Kind),
		CapacityBytes: v.CapacityBytes, SourceType: v.SourceType, SourceId: v.SourceID,
		DevicePath: v.DevicePath, Mountpoint: v.Mountpoint,
	}
}

func snapshotOut(s endpoint.Snapshot) *pb.Snapshot {
	return &pb.Snapshot{
		Id: s.ID, Name: s.Name, SourceVolumeId: s.SourceVolumeID,
		SizeBytes: s.SizeBytes, CreationUnix: s.CreationUnix, ReadyToUse: s.ReadyToUse,
	}
}

func maxEntries(n int32) (int, error) {
	if n < 0 {
		return 0, status.Error(codes.InvalidArgument, "max_entries must be >= 0")
	}
	return int(n), nil
}

func (s *Server) GetCapabilities(ctx context.Context, _ *pb.GetCapabilitiesRequest) (*pb.GetCapabilitiesResponse, error) {
	return &pb.GetCapabilitiesResponse{
		Parent:  s.Parent,
		Version: Version,
		Capabilities: []pb.Capability{
			pb.Capability_CAPABILITY_VOLUMES,
			pb.Capability_CAPABILITY_SNAPSHOTS,
			pb.Capability_CAPABILITY_CLONE_FROM_SNAPSHOT,
			pb.Capability_CAPABILITY_CLONE_FROM_VOLUME,
			pb.Capability_CAPABILITY_EXPAND,
			pb.Capability_CAPABILITY_LIST,
			pb.Capability_CAPABILITY_CAPACITY,
			pb.Capability_CAPABILITY_RECONCILE,
		},
	}, nil
}

func (s *Server) CreateVolume(ctx context.Context, r *pb.CreateVolumeRequest) (*pb.CreateVolumeResponse, error) {
	v, err := s.EP.CreateVolume(ctx, endpoint.CreateVolumeRequest{
		Name: r.GetName(), Kind: kindIn(r.GetKind()),
		CapacityBytes: r.GetCapacityBytes(), LimitBytes: r.GetLimitBytes(),
		Sparse: r.GetSparse(), Reserve: r.GetReserve(),
		SourceSnapshotID: r.GetSourceSnapshotId(), SourceVolumeID: r.GetSourceVolumeId(),
	})
	if err != nil {
		return nil, grpcErr(err)
	}
	return &pb.CreateVolumeResponse{Volume: volumeOut(v)}, nil
}

func (s *Server) DeleteVolume(ctx context.Context, r *pb.DeleteVolumeRequest) (*pb.DeleteVolumeResponse, error) {
	if err := s.EP.DeleteVolume(ctx, r.GetVolumeId()); err != nil {
		return nil, grpcErr(err)
	}
	return &pb.DeleteVolumeResponse{}, nil
}

func (s *Server) ExpandVolume(ctx context.Context, r *pb.ExpandVolumeRequest) (*pb.ExpandVolumeResponse, error) {
	v, err := s.EP.ExpandVolume(ctx, r.GetVolumeId(), r.GetCapacityBytes(), r.GetLimitBytes())
	if err != nil {
		return nil, grpcErr(err)
	}
	return &pb.ExpandVolumeResponse{Volume: volumeOut(v)}, nil
}

func (s *Server) GetVolume(ctx context.Context, r *pb.GetVolumeRequest) (*pb.GetVolumeResponse, error) {
	v, err := s.EP.GetVolume(ctx, r.GetVolumeId())
	if err != nil {
		return nil, grpcErr(err)
	}
	return &pb.GetVolumeResponse{Volume: volumeOut(v)}, nil
}

func (s *Server) ListVolumes(ctx context.Context, r *pb.ListVolumesRequest) (*pb.ListVolumesResponse, error) {
	n, err := maxEntries(r.GetMaxEntries())
	if err != nil {
		return nil, err
	}
	vols, next, err := s.EP.ListVolumes(ctx, n, r.GetStartingToken())
	if err != nil {
		return nil, grpcErr(err)
	}
	out := &pb.ListVolumesResponse{NextToken: next}
	for _, v := range vols {
		out.Volumes = append(out.Volumes, volumeOut(v))
	}
	return out, nil
}

func (s *Server) CreateSnapshot(ctx context.Context, r *pb.CreateSnapshotRequest) (*pb.CreateSnapshotResponse, error) {
	snap, err := s.EP.CreateSnapshot(ctx, r.GetName(), r.GetSourceVolumeId())
	if err != nil {
		return nil, grpcErr(err)
	}
	return &pb.CreateSnapshotResponse{Snapshot: snapshotOut(snap)}, nil
}

func (s *Server) DeleteSnapshot(ctx context.Context, r *pb.DeleteSnapshotRequest) (*pb.DeleteSnapshotResponse, error) {
	if err := s.EP.DeleteSnapshot(ctx, r.GetSnapshotId()); err != nil {
		return nil, grpcErr(err)
	}
	return &pb.DeleteSnapshotResponse{}, nil
}

func (s *Server) ListSnapshots(ctx context.Context, r *pb.ListSnapshotsRequest) (*pb.ListSnapshotsResponse, error) {
	n, err := maxEntries(r.GetMaxEntries())
	if err != nil {
		return nil, err
	}
	snaps, next, err := s.EP.ListSnapshots(ctx, r.GetSnapshotId(), r.GetSourceVolumeId(), n, r.GetStartingToken())
	if err != nil {
		return nil, grpcErr(err)
	}
	out := &pb.ListSnapshotsResponse{NextToken: next}
	for _, sn := range snaps {
		out.Snapshots = append(out.Snapshots, snapshotOut(sn))
	}
	return out, nil
}

func (s *Server) GetCapacity(ctx context.Context, _ *pb.GetCapacityRequest) (*pb.GetCapacityResponse, error) {
	n, err := s.EP.GetCapacity(ctx)
	if err != nil {
		return nil, grpcErr(err)
	}
	return &pb.GetCapacityResponse{AvailableBytes: n}, nil
}

func (s *Server) Reconcile(ctx context.Context, r *pb.ReconcileRequest) (*pb.ReconcileResponse, error) {
	age := r.GetMinAgeSeconds()
	if age > math.MaxInt64/uint64(time.Second) {
		return nil, status.Error(codes.InvalidArgument, "min_age_seconds out of range")
	}
	plan, err := s.EP.Reconcile(ctx, endpoint.ReconcileRequest{
		DesiredVolumeIDs: r.GetDesiredVolumeIds(),
		MinAge:           time.Duration(age) * time.Second,
		DryRun:           r.GetDryRun(),
	})
	if err != nil {
		return nil, grpcErr(err)
	}
	out := &pb.ReconcileResponse{}
	for _, a := range plan.Actions {
		op := "destroy"
		if a.Op.Defer {
			op = "destroy-deferred"
		}
		out.Actions = append(out.Actions, &pb.ReconcileAction{Op: op, Target: a.Target, Reason: a.Reason})
	}
	for _, sk := range plan.Skipped {
		out.Skipped = append(out.Skipped, &pb.ReconcileSkip{Target: sk.Target, Reason: sk.Reason})
	}
	return out, nil
}
