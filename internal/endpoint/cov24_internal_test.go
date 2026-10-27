package endpoint

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Item 7 (wave22): rootErr maps a failed OpSetRoot. A classified *Error
// (LocalRoot's) keeps its code wherever it ran; the caller's deadline is
// DeadlineExceeded and its cancellation Aborted; anything else is
// Internal on the plan's first op and Aborted once an earlier op applied,
// as opErr maps an unclassified zfs failure.
func TestRootErrMapping(t *testing.T) {
	t.Parallel()
	plan := Plan{{Kind: OpCreateFS, Target: "tank/v/a"}, {Kind: OpSetRoot, Target: "tank/v/a"}}
	root := Plan{{Kind: OpSetRoot, Target: "tank/v/a"}}
	plain := errors.New("x")
	for _, tc := range []struct {
		name string
		err  error
		i    int
		plan Plan
		code Code
		msg  string
	}{
		{"classified, first op", &Error{Code: FailedPrecondition, Msg: "read-only"}, 0, root, FailedPrecondition, "op 1/1 set-root tank/v/a: read-only"},
		{"classified, after a change", &Error{Code: FailedPrecondition, Msg: "read-only"}, 1, plan, FailedPrecondition, "op 2/2"},
		{"deadline", context.DeadlineExceeded, 0, root, DeadlineExceeded, "op 1/1"},
		{"deadline, after a change", context.DeadlineExceeded, 1, plan, DeadlineExceeded, "op 2/2"},
		{"canceled", context.Canceled, 0, root, Aborted, "op 1/1"},
		{"unclassified, first op", plain, 0, root, Internal, "op 1/1 set-root tank/v/a"},
		{"unclassified, after a change", plain, 1, plan, Aborted, "earlier ops applied"},
	} {
		got := rootErr(tc.err, tc.i, tc.plan)
		if CodeOf(got) != tc.code || !strings.Contains(got.Error(), tc.msg) {
			t.Errorf("%s: %v (code %d), want code %d with %q", tc.name, got, CodeOf(got), tc.code, tc.msg)
		}
		var e *Error
		if !errors.As(tc.err, &e) && !errors.Is(got, tc.err) {
			t.Errorf("%s: %v does not wrap %v", tc.name, got, tc.err)
		}
	}
}
