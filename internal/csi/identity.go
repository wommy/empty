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
	"google.golang.org/protobuf/types/known/wrapperspb"

	pb "github.com/wommy/empty/gen/zfsendpoint/v1"
	"github.com/wommy/empty/internal/version"
)

// DefaultDriverName is the CSI driver name reported by GetPluginInfo.
const DefaultDriverName = "zfs-endpoint.wommy.github.io"

// Identity implements csi.IdentityServer.
type Identity struct {
	csi.UnimplementedIdentityServer
	EP   pb.EndpointServiceClient
	Name string // defaults to DefaultDriverName
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

// Probe is ready when the endpoint answers; its error passes through.
func (s *Identity) Probe(ctx context.Context, _ *csi.ProbeRequest) (*csi.ProbeResponse, error) {
	if _, err := s.EP.GetCapabilities(ctx, &pb.GetCapabilitiesRequest{}); err != nil {
		return nil, err
	}
	return &csi.ProbeResponse{Ready: wrapperspb.Bool(true)}, nil
}
