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
	t.Parallel()
	for _, tc := range []struct {
		err  error
		want Code
	}{
		{nil, OK}, // nil in, nil out: never a failure made up
		{&zfs.Error{Kind: zfs.KindNotFound, Op: "get", Dataset: "tank/a"}, NotFound},
		{&zfs.Error{Kind: zfs.KindParentNotFound}, NotFound},
		{&zfs.Error{Kind: zfs.KindExists}, AlreadyExists},
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
	if err := zfsErr(nil, "op"); err != nil {
		t.Errorf("zfsErr(nil) = %v", err)
	}
}

// The message carries its cause: operators read it in the CSI events.
func TestErrorText(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		err  *Error
		want string
	}{
		{&Error{Code: NotFound, Msg: "m", Err: fmt.Errorf("cause")}, "m: cause"},
		{&Error{Code: NotFound, Msg: "m"}, "m"},
	} {
		if got := tc.err.Error(); got != tc.want {
			t.Errorf("Error() = %q, want %q", got, tc.want)
		}
	}
}

func TestCodeOfNeverReportsAFailureAsOK(t *testing.T) {
	if c := CodeOf(&Error{Msg: "zero code"}); c != Internal {
		t.Fatalf("non-nil *Error with zero Code: %v, want Internal", c)
	}
}
