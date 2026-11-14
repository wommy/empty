// Package csi is a thin CSI front end (Identity and Controller services)
// over the ZFS endpoint's gRPC API (api/zfsendpoint/v1).
//
// It only translates. Idempotency, locking, validation of names and sizes
// and every lifecycle decision live in the endpoint; its status codes are
// already CSI codes and pass through unchanged (ABORTED in particular must
// reach the sidecars as ABORTED, which they retry). The checks made here are
// the ones that only exist in CSI terms: required CSI fields, access modes
// and access types, and StorageClass parameters.
package csi

import (
	"context"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"

	pb "github.com/wommy/empty/gen/zfsendpoint/v1"
	"github.com/wommy/empty/internal/version"
)

// DefaultDriverName is the CSI driver name reported by GetPluginInfo.
const DefaultDriverName = "zfs-endpoint.wommy.github.io"

// Identity implements csi.IdentityServer.
type Identity struct {
	csi.UnimplementedIdentityServer
	// Health is the endpoint's gRPC health service, on the endpoint's
	// connection: Probe reports what it says about the endpoint service.
	Health healthpb.HealthClient
	Name   string // defaults to DefaultDriverName
}

func (s *Identity) GetPluginInfo(context.Context, *csi.GetPluginInfoRequest) (*csi.GetPluginInfoResponse, error) {
	name := s.Name
	if name == "" {
		name = DefaultDriverName
	}
	return &csi.GetPluginInfoResponse{Name: name, VendorVersion: version.Version}, nil
}

func (s *Identity) GetPluginCapabilities(context.Context, *csi.GetPluginCapabilitiesRequest) (*csi.GetPluginCapabilitiesResponse, error) {
	return &csi.GetPluginCapabilitiesResponse{Capabilities: []*csi.PluginCapability{
		{Type: &csi.PluginCapability_Service_{Service: &csi.PluginCapability_Service{
			Type: csi.PluginCapability_Service_CONTROLLER_SERVICE,
		}}},
		// refquota and volsize both grow while in use
		{Type: &csi.PluginCapability_VolumeExpansion_{VolumeExpansion: &csi.PluginCapability_VolumeExpansion{
			Type: csi.PluginCapability_VolumeExpansion_ONLINE,
		}}},
	}}, nil
}

// Probe asks the endpoint's gRPC health service about the endpoint
// service: ready only when it is SERVING. The daemon turns it NOT_SERVING
// while its own probe of the parent fails (a suspended or exported pool)
// and while it drains, so a front end in front of an unusable pool is not
// reported ready. That is ready=false, not an error: the plugin itself is
// fine. So is an endpoint that cannot be reached (Unavailable: its socket
// is gone while it restarts) or does not answer in time (DeadlineExceeded):
// CSI's "still initializing, but otherwise perfectly healthy", which the
// sidecars' ProbeForever retries, where any error makes csi-provisioner
// and csi-snapshotter exit. Other Check errors pass through.
func (s *Identity) Probe(ctx context.Context, _ *csi.ProbeRequest) (*csi.ProbeResponse, error) {
	res, err := s.Health.Check(ctx, &healthpb.HealthCheckRequest{Service: pb.EndpointService_ServiceDesc.ServiceName})
	switch status.Code(err) {
	case codes.OK:
	case codes.Unavailable, codes.DeadlineExceeded:
		return &csi.ProbeResponse{Ready: wrapperspb.Bool(false)}, nil
	default:
		return nil, err
	}
	return &csi.ProbeResponse{Ready: wrapperspb.Bool(res.GetStatus() == healthpb.HealthCheckResponse_SERVING)}, nil
}
