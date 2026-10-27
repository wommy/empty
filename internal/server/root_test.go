package server_test

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"

	pb "github.com/wommy/empty/gen/zfsendpoint/v1"
	"github.com/wommy/empty/internal/endpoint"
)

// rootLog records the root changes an endpoint asks for.
type rootLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *rootLog) SetRoot(_ context.Context, dataset, _ string, uid, gid, mode int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, fmt.Sprintf("%s %d %d %o", dataset, uid, gid, mode))
	return nil
}

// N11: the root's owner and mode cross the wire with their presence: an
// explicit 0 is root, unset is "leave it" (-1), never 0.
func TestNFSRootOwnerPresence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	log := &rootLog{}
	c := clientConfig(t, endpoint.Config{FileExport: true, NFSServers: []string{"192.0.2.10"}, Root: log})
	zero, mode := uint32(0), uint32(0o770)
	for i, n := range []*pb.NFSExport{
		{Clients: []string{"10.20.0.0/24"}, Uid: &zero},
		{Clients: []string{"10.20.0.0/24"}, Mode: &mode},
		{Clients: []string{"10.20.0.0/24"}},
	} {
		if _, err := c.CreateVolume(ctx, &pb.CreateVolumeRequest{Name: fmt.Sprint("v", i), NfsExport: n}); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"tank/vols/v0 0 -1 -1", "tank/vols/v1 -1 -1 770"}
	if !slices.Equal(log.calls, want) {
		t.Fatalf("root changes %q, want %q", log.calls, want)
	}
}
