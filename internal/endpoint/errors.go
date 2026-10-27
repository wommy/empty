package endpoint

import (
	"errors"
	"fmt"
	"unicode/utf8"
)

// Code is the endpoint's error category. Values deliberately mirror the
// gRPC/CSI codes the front ends return, so the mapping is 1:1.
type Code int

const (
	OK                 Code = 0
	Aborted            Code = 10 // an operation on the same resource is in flight
	AlreadyExists      Code = 6  // same name, incompatible request
	DeadlineExceeded   Code = 4  // the caller's deadline killed a zfs command; outcome unknown
	FailedPrecondition Code = 9  // e.g. volume has snapshots or dependent clones
	Internal           Code = 13
	InvalidArgument    Code = 3
	NotFound           Code = 5
	OutOfRange         Code = 11 // capacity outside limits
	ResourceExhausted  Code = 8  // no space
	Unavailable        Code = 14 // host or pool temporarily unusable
	Unimplemented      Code = 12 // capability not offered by this endpoint
)

// Error is an endpoint failure with a stable Code.
type Error struct {
	Code Code
	Msg  string
	Err  error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Msg, e.Err)
	}
	return e.Msg
}

func (e *Error) Unwrap() error { return e.Err }

func errorf(code Code, format string, a ...any) *Error {
	return &Error{Code: code, Msg: clamp(fmt.Sprintf(format, a...), maxMsg)}
}

// maxMsg bounds an errorf message. Many echo a caller's input with %q, and
// the message is returned to the caller and logged: one client sending a
// 1 MiB name must not turn into 1 MiB errors and log lines.
const maxMsg = 512

// clamp cuts s to at most n bytes (on a rune boundary, so the result stays
// valid UTF-8 for gRPC and JSON logs) and marks the cut.
func clamp(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := n
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return fmt.Sprintf("%s...[%d bytes cut]", s[:i], len(s)-i)
}

// CodeOf returns the Code of err (Internal for foreign errors, OK for nil).
// A non-nil error never reads as OK: an *Error left with the zero Code is
// a bug, and must not turn a failure into a reported success.
func CodeOf(err error) Code {
	if err == nil {
		return OK
	}
	var e *Error
	if errors.As(err, &e) && e.Code != OK {
		return e.Code
	}
	return Internal
}
