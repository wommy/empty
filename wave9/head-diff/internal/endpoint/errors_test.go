package endpoint

import (
	"context"
	"fmt"
	"testing"

	"github.com/wommy/empty/internal/zfs"
)

// zfsErr maps each classified zfs failure to the code the CSI sidecars
// need: retryable for states that clear by themselves (busy, a pool that
// went read-only or away, a killed command whose outcome is unknown),
// final for ones only an administrator or the caller can change.
func TestZfsErrCodes(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want Code
	}{
		{&zfs.Error{Kind: zfs.KindBusy}, Unavailable}, // destroys: FailedPrecondition, see opErr
		{&zfs.Error{Kind: zfs.KindHasClones}, FailedPrecondition},
		{&zfs.Error{Kind: zfs.KindHasChildren}, FailedPrecondition},
		{&zfs.Error{Kind: zfs.KindPrecondition}, FailedPrecondition},
		{&zfs.Error{Kind: zfs.KindPermission}, FailedPrecondition},
		{&zfs.Error{Kind: zfs.KindUnavailable}, Unavailable},
		{&zfs.Error{Kind: zfs.KindInvalidProperty}, InvalidArgument},
		{&zfs.Error{Kind: zfs.KindNoSpace}, ResourceExhausted},
		{&zfs.Error{Kind: zfs.KindInterrupted, Err: fmt.Errorf("run: %w", context.DeadlineExceeded)}, DeadlineExceeded},
		{&zfs.Error{Kind: zfs.KindInterrupted, Err: fmt.Errorf("run: %w", context.Canceled)}, Aborted},
		{&zfs.Error{Kind: zfs.KindUnknown}, Internal},
	} {
		if got := CodeOf(zfsErr(tc.err, "op")); got != tc.want {
			t.Errorf("%v: code %d, want %d", tc.err, got, tc.want)
		}
	}
}

func TestCodeOfNeverReportsAFailureAsOK(t *testing.T) {
	if c := CodeOf(&Error{Msg: "zero code"}); c != Internal {
		t.Fatalf("non-nil *Error with zero Code: %v, want Internal", c)
	}
}
