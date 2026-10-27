// Package code is the core's error vocabulary: CSI/gRPC codes and the
// Refusal a decision returns instead of a plan.
package code

import "fmt"

// Code mirrors the gRPC/CSI codes (and endpoint.Code) 1:1.
type Code uint8

const (
	OK                 Code = 0
	InvalidArgument    Code = 3
	DeadlineExceeded   Code = 4
	NotFound           Code = 5
	AlreadyExists      Code = 6
	ResourceExhausted  Code = 8
	FailedPrecondition Code = 9
	Aborted            Code = 10
	OutOfRange         Code = 11
	Unimplemented      Code = 12
	Internal           Code = 13
	Unavailable        Code = 14
)

var names = map[Code]string{OK: "OK", InvalidArgument: "InvalidArgument", DeadlineExceeded: "DeadlineExceeded",
	NotFound: "NotFound", AlreadyExists: "AlreadyExists", ResourceExhausted: "ResourceExhausted",
	FailedPrecondition: "FailedPrecondition", Aborted: "Aborted", OutOfRange: "OutOfRange",
	Unimplemented: "Unimplemented", Internal: "Internal", Unavailable: "Unavailable"}

func (c Code) String() string {
	if n, ok := names[c]; ok {
		return n
	}
	return fmt.Sprintf("Code(%d)", uint8(c))
}

// Refusal is a decision not to act. Code is never OK: the constructor
// maps OK to Internal, so a refusal can never read as success.
type Refusal struct {
	Code Code
	Why  string
}

// Refuse builds a Refusal.
func Refuse(c Code, f string, a ...any) *Refusal {
	if c == OK {
		c = Internal
	}
	return &Refusal{c, fmt.Sprintf(f, a...)}
}

func (r *Refusal) Error() string { return r.Code.String() + ": " + r.Why }
