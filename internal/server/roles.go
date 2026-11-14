package server

import (
	"context"
	"net"
	"os"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	pb "github.com/wommy/empty/gen/zfsendpoint/v1"
)

// Role is what the callers of one listener may call (ADR 0001 v2 §3, F2).
// It belongs to the listener, not to the caller: whoever can open a socket
// gets its role, and filesystem permissions decide who can open it.
type Role uint8

const (
	// RoleCSI is the default listener's: exactly the RPCs zfs-csi calls.
	// A compromised CSI controller can neither collect every volume in
	// one Reconcile nor export a volume to itself.
	RoleCSI Role = iota + 1
	// RoleAdmin is everything, Reconcile and the export RPCs included. Its
	// socket is 0600, in a 0700 directory of its own that is never mounted
	// into a pod, and a peer must be root or the daemon's own user.
	RoleAdmin
)

func (r Role) String() string {
	switch r {
	case RoleCSI:
		return "csi"
	case RoleAdmin:
		return "admin"
	}
	return "none"
}

// methodRoles holds a row for every EndpointService method; a method
// without one is allowed to no role (TestEveryMethodHasARole).
var methodRoles = map[string][]Role{
	pb.EndpointService_GetCapabilities_FullMethodName:     {RoleCSI, RoleAdmin},
	pb.EndpointService_CreateVolume_FullMethodName:        {RoleCSI, RoleAdmin},
	pb.EndpointService_DeleteVolume_FullMethodName:        {RoleCSI, RoleAdmin},
	pb.EndpointService_ExpandVolume_FullMethodName:        {RoleCSI, RoleAdmin},
	pb.EndpointService_GetVolume_FullMethodName:           {RoleCSI, RoleAdmin},
	pb.EndpointService_ListVolumes_FullMethodName:         {RoleCSI, RoleAdmin},
	pb.EndpointService_CreateSnapshot_FullMethodName:      {RoleCSI, RoleAdmin},
	pb.EndpointService_DeleteSnapshot_FullMethodName:      {RoleCSI, RoleAdmin},
	pb.EndpointService_ListSnapshots_FullMethodName:       {RoleCSI, RoleAdmin},
	pb.EndpointService_GetCapacity_FullMethodName:         {RoleCSI, RoleAdmin},
	pb.EndpointService_Reconcile_FullMethodName:           {RoleAdmin},
	pb.EndpointService_PublishFileExport_FullMethodName:   {RoleAdmin},
	pb.EndpointService_UnpublishFileExport_FullMethodName: {RoleAdmin},
}

// Allowed reports whether role r may call fullMethod.
func Allowed(r Role, fullMethod string) bool {
	for _, x := range methodRoles[fullMethod] {
		if x == r {
			return true
		}
	}
	return false
}

// Serve serves the daemon on lis with role r: every connection lis accepts
// carries r into its PeerInfo. A listener passed to GRPC.Serve directly
// has no role and is served health only.
func (d *Daemon) Serve(lis net.Listener, r Role) error {
	return d.GRPC.Serve(roleListener{lis, r})
}

type roleListener struct {
	net.Listener
	role Role
}

func (l roleListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return roleConn{c, l.role}, nil
}

// roleConn is an accepted connection and its listener's role; the
// handshake (peerCreds) moves the role into PeerInfo.
type roleConn struct {
	net.Conn
	role Role
}

// health is served to every listener: it says nothing a caller could use.
func isHealth(fullMethod string) bool { return strings.HasPrefix(fullMethod, "/grpc.health.") }

// authorize allows a call by the role of the listener it arrived on.
func authorize(ctx context.Context, fullMethod string) error {
	if isHealth(fullMethod) {
		return nil
	}
	p, ok := peer.FromContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "no peer")
	}
	pi, ok := p.AuthInfo.(PeerInfo)
	if !ok || pi.Role == 0 {
		return status.Error(codes.Unauthenticated, "the connection came through no listener with a role")
	}
	if !Allowed(pi.Role, fullMethod) {
		return status.Errorf(codes.PermissionDenied, "%s is not served on the %s socket", fullMethod, pi.Role)
	}
	if pi.Role == RoleAdmin && !adminPeer(pi, os.Geteuid()) {
		return status.Errorf(codes.PermissionDenied, "the admin socket serves root and uid %d only, not uid %d", os.Geteuid(), pi.UID)
	}
	return nil
}

// adminPeer: an admin caller is root or the daemon's own user (who owns
// the 0600 admin socket), whatever the socket's mode. A connection without
// credentials is not a unix socket (bufconn in tests); its listener's role
// stands.
func adminPeer(p PeerInfo, euid int) bool {
	return !p.HasCred || p.UID == 0 || p.UID == euid
}

// stream refuses every stream but health's: EndpointService has none, so
// one added later is denied until it has a role.
func stream(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	if !isHealth(info.FullMethod) {
		return status.Errorf(codes.PermissionDenied, "%s: streams have no role", info.FullMethod)
	}
	return handler(srv, ss)
}
