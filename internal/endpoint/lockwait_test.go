package endpoint

import (
	"context"
	"testing"
)

// H-01: with less than lockMargin left before the caller's deadline there
// is no time to wait, but a free lock is still taken: only a held one is
// Aborted. Before, WithTimeout(0) refused even an uncontended lock.
func TestShortDeadlineStillTakesAFreeLock(t *testing.T) {
	t.Parallel()
	ep, _ := simEndpoint(t, Config{})
	ctx, cancel := context.WithTimeout(context.Background(), lockMargin/2)
	defer cancel()
	release, err := ep.lock(ctx, "vol:a")
	if err != nil {
		t.Fatalf("free lock with %v left: %v", lockMargin/2, err)
	}
	defer release()
	if _, err := ep.lock(ctx, "vol:a"); CodeOf(err) != Aborted {
		t.Fatalf("held lock: %v", err)
	}
	// end to end: an uncontended CreateVolume inside the margin
	if _, err := ep.CreateVolume(ctx, CreateVolumeRequest{Name: "quick", CapacityBytes: 1 << 20}); err != nil {
		t.Fatalf("CreateVolume with %v left: %v", lockMargin/2, err)
	}
}
