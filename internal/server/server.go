// Package server exposes internal/endpoint over gRPC
// (api/zfsendpoint/v1). It only translates: every decision lives in the
// endpoint core.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/wommy/empty/gen/zfsendpoint/v1"
	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/version"
)

// Server implements pb.EndpointServiceServer.
type Server struct {
	pb.UnimplementedEndpointServiceServer
	EP     *endpoint.Endpoint
	Parent string
}

// grpcErr maps endpoint errors to gRPC status. endpoint.Code values are the
// gRPC codes by construction. The message is clamped (maxErrMsg) as a last
// bound on what a caller's input can grow into; the endpoint clamps its
// own messages and zfs stderr well below it.
func grpcErr(err error) error {
	if err == nil {
		return nil
	}
	return status.Error(codes.Code(endpoint.CodeOf(err)), clip(err.Error(), maxErrMsg))
}

// maxErrMsg bounds a returned error message; maxLogField a caller-supplied
// value logged before it is validated (a volume or snapshot id).
const (
	maxErrMsg   = 16 << 10
	maxLogField = 256
)

// clip cuts s to at most n bytes on a rune boundary and marks the cut.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := n
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return fmt.Sprintf("%s...[%d bytes cut]", s[:i], len(s)-i)
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
	// presence is meaning: unset leaves the root's owner or mode as made
	return &endpoint.NFSExport{Clients: n.GetClients(), ReadOnly: n.GetReadOnly(), Server: n.GetServer(),
		UID: n.Uid, GID: n.Gid, Mode: n.Mode}
}

func snapshotOut(s endpoint.Snapshot) *pb.Snapshot {
	return &pb.Snapshot{
		Id: s.ID, Name: s.Name, SourceVolumeId: s.SourceVolumeID,
		SizeBytes: s.SizeBytes, CreationUnix: s.CreationUnix, ReadyToUse: s.ReadyToUse,
	}
}

// MaxPageSize caps a list page. CSI lets the plugin page even when
// max_entries is 0 ("no limit"): it may return fewer entries plus a
// next_token. Without the cap, 100k volumes are a ~21 MB response, past
// every client's default 4 MiB receive limit.
const MaxPageSize = 1000

func maxEntries(n int32) (int, error) {
	if n < 0 {
		return 0, status.Error(codes.InvalidArgument, "max_entries must be >= 0")
	}
	if n == 0 || n > MaxPageSize {
		return MaxPageSize, nil
	}
	return int(n), nil
}

func (s *Server) GetCapabilities(ctx context.Context, _ *pb.GetCapabilitiesRequest) (*pb.GetCapabilitiesResponse, error) {
	caps := []pb.Capability{
		pb.Capability_CAPABILITY_VOLUMES,
		pb.Capability_CAPABILITY_SNAPSHOTS,
		pb.Capability_CAPABILITY_CLONE_FROM_SNAPSHOT,
		pb.Capability_CAPABILITY_CLONE_FROM_VOLUME,
		pb.Capability_CAPABILITY_EXPAND,
		pb.Capability_CAPABILITY_LIST,
		pb.Capability_CAPABILITY_CAPACITY,
		pb.Capability_CAPABILITY_PROPERTIES,
		pb.Capability_CAPABILITY_RECONCILE,
	}
	if s.EP.FileExportEnabled() {
		caps = append(caps, pb.Capability_CAPABILITY_FILE_EXPORT)
	}
	return &pb.GetCapabilitiesResponse{Parent: s.Parent, Version: version.Version, Capabilities: caps, NfsServers: s.EP.NFSServers()}, nil
}

func (s *Server) PublishFileExport(ctx context.Context, r *pb.PublishFileExportRequest) (*pb.PublishFileExportResponse, error) {
	path, err := s.EP.PublishFileExport(ctx, r.GetVolumeId(), r.GetClient(), r.GetReadOnly())
	if err != nil {
		return nil, grpcErr(err)
	}
	return &pb.PublishFileExportResponse{ExportPath: path}, nil
}

func (s *Server) UnpublishFileExport(ctx context.Context, r *pb.UnpublishFileExportRequest) (*pb.UnpublishFileExportResponse, error) {
	if err := s.EP.UnpublishFileExport(ctx, r.GetVolumeId(), r.GetClient()); err != nil {
		return nil, grpcErr(err)
	}
	return &pb.UnpublishFileExportResponse{}, nil
}

func (s *Server) CreateVolume(ctx context.Context, r *pb.CreateVolumeRequest) (*pb.CreateVolumeResponse, error) {
	v, err := s.EP.CreateVolume(ctx, endpoint.CreateVolumeRequest{
		Name: r.GetName(), Kind: kindIn(r.GetKind()),
		CapacityBytes: r.GetCapacityBytes(), LimitBytes: r.GetLimitBytes(),
		Sparse: r.GetSparse(), Reserve: r.GetReserve(),
		SourceSnapshotID: r.GetSourceSnapshotId(), SourceVolumeID: r.GetSourceVolumeId(),
		Properties: r.GetProperties(),
		NFS:        nfsIn(r.GetNfsExport()),
	})
	if err != nil {
		return nil, grpcErr(err)
	}
	return &pb.CreateVolumeResponse{Volume: volumeOut(v)}, nil
}

func (s *Server) DeleteVolume(ctx context.Context, r *pb.DeleteVolumeRequest) (*pb.DeleteVolumeResponse, error) {
	slog.InfoContext(ctx, "delete volume", "volume_id", clip(r.GetVolumeId(), maxLogField))
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
	slog.InfoContext(ctx, "delete snapshot", "snapshot_id", clip(r.GetSnapshotId(), maxLogField))
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
	slog.InfoContext(ctx, "reconcile", "desired", len(r.GetDesiredVolumeIds()), "min_age_seconds", age, "dry_run", r.GetDryRun())
	plan, err := s.EP.Reconcile(ctx, endpoint.ReconcileRequest{
		DesiredVolumeIDs:   r.GetDesiredVolumeIds(),
		MinAge:             time.Duration(age) * time.Second,
		DryRun:             r.GetDryRun(),
		MaxDeletions:       int(r.GetMaxDeletions()),
		UnlimitedDeletions: r.GetUnlimitedDeletions(),
		PlanDigest:         r.GetPlanDigest(),
	})
	if err != nil {
		return nil, grpcErr(err)
	}
	if !r.GetDryRun() {
		// what was destroyed and why; each destroy's argv and outcome is
		// also logged as it ran (zfs.Audit)
		for _, a := range plan.Actions {
			slog.InfoContext(ctx, "reconcile destroyed", "target", a.Target, "reason", a.Reason, "deferred", a.Op.Defer)
		}
	}
	out := &pb.ReconcileResponse{PlanDigest: plan.Digest}
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
