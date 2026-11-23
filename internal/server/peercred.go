package server

import (
	"context"
	"errors"
	"net"

	"google.golang.org/grpc/credentials"
)

// PeerInfo is a connection's credentials.AuthInfo: the caller's
// SO_PEERCRED, read once at the handshake. socket(7): they are the
// credentials in effect at connect(2), translated into the daemon's user
// namespace (every uid-0 container reads as uid 0).
type PeerInfo struct {
	credentials.CommonAuthInfo
	// HasCred: UID, GID and PID were read. False for a connection that is
	// not a unix socket (bufconn in tests; the daemon serves nothing else).
	HasCred       bool
	UID, GID, PID int
	// Role is the role of the listener that accepted the connection
	// (Daemon.Serve); 0 for none.
	Role Role
}

// AuthType implements credentials.AuthInfo.
func (PeerInfo) AuthType() string { return "peercred" }

// peerCreds are the server's transport credentials: no encryption (a unix
// socket needs none), only the peer's identity, for the audit line (ADR
// 0001 v2 §4, F7).
type peerCreds struct{}

func (peerCreds) ServerHandshake(c net.Conn) (net.Conn, credentials.AuthInfo, error) {
	info := PeerInfo{CommonAuthInfo: credentials.CommonAuthInfo{SecurityLevel: credentials.NoSecurity}}
	raw := c
	if rc, ok := c.(roleConn); ok {
		info.Role, raw = rc.role, rc.Conn
	}
	if uc, ok := raw.(*net.UnixConn); ok {
		if uid, gid, pid, err := peerCred(uc); err == nil {
			info.HasCred, info.UID, info.GID, info.PID = true, uid, gid, pid
		}
	}
	return c, info, nil
}

func (peerCreds) ClientHandshake(context.Context, string, net.Conn) (net.Conn, credentials.AuthInfo, error) {
	return nil, nil, errors.New("peer credentials are server-side only")
}

func (peerCreds) Info() credentials.ProtocolInfo {
	return credentials.ProtocolInfo{SecurityProtocol: "peercred"}
}

func (c peerCreds) Clone() credentials.TransportCredentials { return c }

func (peerCreds) OverrideServerName(string) error { return nil }
