package server

import (
	"context"
	"os"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	pb "github.com/wommy/empty/gen/zfsendpoint/v1"
)

// F2: the admin role is root's (or the daemon's own user's, which owns the
// 0600 admin socket): a peer whose SO_PEERCRED shows another uid is
// refused, even if the socket's mode was loosened by hand. A connection
// with no credentials (not a unix socket; the daemon serves none) is left
// to the listener's role.
func TestAdminPeerIsRootOrTheDaemonsUser(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		p    PeerInfo
		euid int
		ok   bool
	}{
		{PeerInfo{HasCred: true, UID: 0}, 0, true},
		{PeerInfo{HasCred: true, UID: 1000}, 0, false},
		{PeerInfo{HasCred: true, UID: 65534}, 0, false},
		{PeerInfo{HasCred: true, UID: 1000}, 1000, true}, // -allow-non-root
		{PeerInfo{HasCred: true, UID: 0}, 1000, true},
		{PeerInfo{HasCred: true, UID: 1001}, 1000, false},
		{PeerInfo{}, 0, true},
	} {
		if got := adminPeer(tc.p, tc.euid); got != tc.ok {
			t.Errorf("peer %+v, daemon euid %d: %v, want %v", tc.p, tc.euid, got, tc.ok)
		}
	}
}

// authorize itself refuses an admin-socket peer of another uid: the
// adminPeer table above is not enough if authorize stops calling it.
func TestAuthorizeRefusesAdminPeerOfAnotherUID(t *testing.T) {
	t.Parallel()
	euid := os.Geteuid()
	reconcile := pb.EndpointService_Reconcile_FullMethodName
	at := func(pi PeerInfo) context.Context {
		return peer.NewContext(context.Background(), &peer.Peer{AuthInfo: pi})
	}
	other := euid + 1
	err := authorize(at(PeerInfo{Role: RoleAdmin, HasCred: true, UID: other}), reconcile)
	if status.Code(err) != codes.PermissionDenied || !strings.Contains(err.Error(), "admin socket serves root") {
		t.Fatalf("admin socket, uid %d (daemon %d): %v", other, euid, err)
	}
	for _, uid := range []int{0, euid} {
		if err := authorize(at(PeerInfo{Role: RoleAdmin, HasCred: true, UID: uid}), reconcile); err != nil {
			t.Fatalf("admin socket, uid %d (daemon %d): %v", uid, euid, err)
		}
	}
	// the CSI socket never serves Reconcile, whoever calls
	if err := authorize(at(PeerInfo{Role: RoleCSI, HasCred: true, UID: 0}), reconcile); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("csi socket, root: %v", err)
	}
	// no peer at all: unauthenticated, whatever the method
	for _, m := range []string{reconcile, pb.EndpointService_GetCapabilities_FullMethodName} {
		if err := authorize(context.Background(), m); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("no peer, %s: %v", m, err)
		}
	}
	// health needs no peer
	if err := authorize(context.Background(), "/grpc.health.v1.Health/Check"); err != nil {
		t.Fatalf("health: %v", err)
	}
}

// The stream interceptor denies every stream but health's, before its
// handler runs.
func TestStreamInterceptorServesHealthOnly(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		method string
		ok     bool
	}{
		{"/zfsendpoint.v1.EndpointService/Watch", false},
		{pb.EndpointService_Reconcile_FullMethodName, false},
		{"/grpc.health.v1.Health/Watch", true},
	} {
		called := false
		err := stream(nil, nil, &grpc.StreamServerInfo{FullMethod: tc.method, IsServerStream: true},
			func(any, grpc.ServerStream) error { called = true; return nil })
		if tc.ok && (err != nil || !called) {
			t.Errorf("%s: called %v, %v", tc.method, called, err)
		}
		if !tc.ok && (status.Code(err) != codes.PermissionDenied || called) {
			t.Errorf("%s: called %v, %v, want PermissionDenied and no call", tc.method, called, err)
		}
	}
}
