package endpoint

import (
	"errors"
	"fmt"
)

// Code is the endpoint's error category. Values deliberately mirror the
// gRPC/CSI codes the front ends return, so the mapping is 1:1.
type Code int

const (
	OK                 Code = 0
	Aborted            Code = 10 // an operation on the same resource is in flight
	AlreadyExists      Code = 6  // same name, incompatible request
	FailedPrecondition Code = 9  // e.g. volume has snapshots or dependent clones
	Internal           Code = 13
	InvalidArgument    Code = 3
	NotFound           Code = 5
	OutOfRange         Code = 11 // capacity outside limits
	ResourceExhausted  Code = 8  // no space
	Unavailable        Code = 14 // host or pool temporarily unusable
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
	return &Error{Code: code, Msg: fmt.Sprintf(format, a...)}
}

// CodeOf returns the Code of err (Internal for foreign errors, OK for nil).
func CodeOf(err error) Code {
	if err == nil {
		return OK
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return Internal
}
