package endpoint

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/wommy/empty/internal/lock"
)

// A lock refusal says how long it waited, and who holds the key (its
// request id) and for how long, so the holder's lines can be found.
func TestLockRefusalNamesTheHolder(t *testing.T) {
	t.Parallel()
	ep, _ := simEndpoint(t, Config{LockWait: 20 * time.Millisecond})
	release, err := ep.lock(lock.WithOwner(context.Background(), "req-holder"), "vol:a")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	_, err = ep.lock(context.Background(), "vol:a", "vol:b")
	want := regexp.MustCompile(`^operation pending on \[vol:a vol:b\] \(waited \d+ms; vol:a held for \d+ms by request req-holder\)$`)
	if CodeOf(err) != Aborted || !want.MatchString(err.Error()) {
		t.Fatalf("got %v, want Aborted matching %s", err, want)
	}
	// no wait at all, and a holder that named no owner
	free, _ := simEndpoint(t, Config{LockWait: -1})
	r2, ok := free.locks.TryAcquire("vol:c")
	if !ok {
		t.Fatal("vol:c busy")
	}
	defer r2()
	_, err = free.lock(context.Background(), "vol:c")
	if want := regexp.MustCompile(`^operation pending on \[vol:c\] \(waited 0s; vol:c held for \d+m?s\)$`); !want.MatchString(err.Error()) {
		t.Fatalf("got %v, want %s", err, want)
	}
}

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
