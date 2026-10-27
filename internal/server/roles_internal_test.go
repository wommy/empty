package server

import "testing"

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
