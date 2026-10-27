package endpoint

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/wommy/empty/internal/zfs"
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
	// cell is set on a zfs failure (zfsFail): its code is errTable's, and
	// CodeOf reads it there.
	cell *errCell
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
	if errors.As(err, &e) {
		if e.cell != nil {
			return e.cell.code()
		}
		if e.Code != OK {
			return e.Code
		}
	}
	return Internal
}

// The zfs error table is the one mapping from a failed zfs command to a
// Code. A cell is keyed by
//   - the op the command ran for: an apply OpKind, or opRead for a scan or
//     a get (opReadParent when it read the parent);
//   - the kind of failure: a zfs.Kind, or kindDeadline for a command the
//     caller's deadline killed (an error with no *zfs.Error in it is
//     zfs.KindUnknown);
//   - mutated: whether an earlier op of the same plan, or the earlier
//     attempt being resumed, already changed the pool.
//
// errRules is the table as written, first match wins; errTable is it
// expanded once, every cell set. Nothing else maps a zfs failure to a Code:
// zfsFail is the only constructor, and CodeOf reads the cell.
//
// Busy, an unavailable pool and an interrupted command clear by themselves,
// so they are retryable; states only the caller or an administrator change
// are final. Busy on a destroy is "in use", which CSI's DeleteVolume and
// DeleteSnapshot report as FAILED_PRECONDITION. An unclassified failure
// after a change is Aborted, not Internal: a retry re-scans and converges,
// while a final code would strand the half-applied plan
// (external-provisioner never deletes what it never got). That holds for
// a read too (a scan or get after the change, opRead mutated): the change
// is what a retry must finish, whatever command failed. Before any change
// it stays Internal.
//
// A destroy whose target no longer has the guid the plan read (OWN-15:
// renamed away, another dataset made under the name) is not a zfs failure
// and has no cell: apply refuses it as Aborted whatever ran before, the
// code an interrupted command gets, since a retry re-scans and plans
// against what is there now.
var errRules = []errRule{
	// zfs prints the same "dataset does not exist" for an exported pool as
	// for a missing dataset, and the parent is configuration, so it is
	// never "absent": Unavailable is retried, NotFound is final for the
	// CSI sidecars
	{ops: []OpKind{opReadParent}, kinds: kinds(zfs.KindNotFound), code: Unavailable},
	{ops: []OpKind{OpDestroy}, kinds: kinds(zfs.KindBusy), code: FailedPrecondition},
	{kinds: kinds(zfs.KindNotFound, zfs.KindParentNotFound), code: NotFound},
	{kinds: kinds(zfs.KindExists), code: AlreadyExists},
	// KindPermission: a missing zfs allow grant
	{kinds: kinds(zfs.KindHasClones, zfs.KindHasChildren, zfs.KindPrecondition, zfs.KindPermission), code: FailedPrecondition},
	{kinds: kinds(zfs.KindBusy, zfs.KindUnavailable), code: Unavailable},
	// killed by the caller's context: the op may or may not have run, so
	// never Internal; a retry re-scans and re-plans
	{kinds: []errKind{kindDeadline}, code: DeadlineExceeded},
	{kinds: kinds(zfs.KindInterrupted), code: Aborted},
	// no space after a change (a fresh clone's reservation, set after its
	// snapshot and clone ran): ResourceExhausted is final for
	// external-provisioner under Immediate binding, and would strand the
	// uncommitted clone (and its source's DeleteVolume, "has dependent
	// clone"); a retry converges once there is space. The better fix,
	// not built: roll a fresh create back (destroy it by the guid just
	// read) and return the classified code, Aborted only if the rollback
	// fails; that would cover the deterministic codes mid-plan too
	// (AlreadyExists or NotFound on a clone after its snapshot,
	// InvalidArgument from a size set).
	{ops: mutatingOps, kinds: kinds(zfs.KindNoSpace), after: true, code: Aborted},
	{kinds: kinds(zfs.KindNoSpace), code: ResourceExhausted},
	{kinds: kinds(zfs.KindInvalidProperty), code: InvalidArgument},
	{kinds: kinds(zfs.KindUnknown), after: true, code: Aborted},
	{kinds: kinds(zfs.KindUnknown), code: Internal},
}

// opRead is the op of a zfs command that changes nothing: a scan, a get.
// opReadParent is one that read the parent dataset, or found a volume
// absent without the parent there to confirm it (parentUnavailable).
const (
	opRead       OpKind = 0
	opReadParent        = OpSetRoot + 1
)

// mutatingOps are the apply ops: every OpKind.
var mutatingOps = []OpKind{OpCreateFS, OpCreateVol, OpSnapshot, OpClone, OpSet, OpDestroy, OpMount, OpSetRoot}

// errKind is a zfs.Kind, or kindDeadline.
type errKind uint8

// kindDeadline is zfs.KindInterrupted by the caller's deadline.
const kindDeadline = errKind(zfs.NumKinds)

const (
	nErrOps   = int(opReadParent) + 1
	nErrKinds = zfs.NumKinds + 1
)

func kinds(ks ...zfs.Kind) []errKind {
	out := make([]errKind, len(ks))
	for i, k := range ks {
		out[i] = errKind(k)
	}
	return out
}

// kindOf is the errKind of err.
func kindOf(err error) errKind {
	var ze *zfs.Error
	if !errors.As(err, &ze) || ze.Kind < 0 || int(ze.Kind) >= zfs.NumKinds {
		return errKind(zfs.KindUnknown)
	}
	if ze.Kind == zfs.KindInterrupted && errors.Is(err, context.DeadlineExceeded) {
		return kindDeadline
	}
	return errKind(ze.Kind)
}

// errRule is a row of errRules.
type errRule struct {
	ops   []OpKind // nil: every op, opRead included
	kinds []errKind
	after bool // only after a change (mutated)
	code  Code
}

func (r errRule) matches(c errCell) bool {
	return (r.ops == nil || slices.Contains(r.ops, c.op)) &&
		slices.Contains(r.kinds, c.kind) &&
		(!r.after || c.mutated)
}

// errCell is a key of errTable.
type errCell struct {
	op      OpKind
	kind    errKind
	mutated bool
}

func (c errCell) code() Code {
	m := 0
	if c.mutated {
		m = 1
	}
	return errTable[c.op][c.kind][m]
}

// errTable is errRules expanded. A cell no rule covers panics at init: the
// table is total.
var errTable = expandErrRules(errRules)

func expandErrRules(rules []errRule) (t [nErrOps][nErrKinds][2]Code) {
	for op := range nErrOps {
		for k := range nErrKinds {
			for m := range 2 {
				c := errCell{OpKind(op), errKind(k), m == 1}
				i := slices.IndexFunc(rules, func(r errRule) bool { return r.matches(c) })
				if i < 0 {
					panic(fmt.Sprintf("errRules: no rule for %+v", c))
				}
				t[op][k][m] = rules[i].code
			}
		}
	}
	return t
}

// zfsFail is the one place a zfs failure becomes an *Error: its code is
// errTable's cell for (op, the kind of err, mutated). nil stays nil.
func zfsFail(op OpKind, err error, mutated bool, msg string) error {
	if err == nil {
		return nil
	}
	c := errCell{op, kindOf(err), mutated}
	return &Error{Code: c.code(), Msg: msg, Err: err, cell: &c}
}

// zfsErr maps the failure of a zfs command that changes nothing: a scan, a
// get.
func zfsErr(err error, what string) error { return zfsFail(opRead, err, false, what) }

// remapMutated re-keys a zfs failure as one after a change (an earlier op
// of the plan, an earlier attempt being resumed). Where that changes the
// code, the message says why with because; any other error is unchanged.
func remapMutated(err error, because string) error {
	var e *Error
	if !errors.As(err, &e) || e.cell == nil || e.cell.mutated {
		return err
	}
	c := *e.cell
	c.mutated = true
	if c.code() == e.cell.code() {
		return err
	}
	if c.kind == errKind(zfs.KindNoSpace) {
		because = noSpaceAfterChange(because)
	}
	return &Error{Code: c.code(), Msg: e.Msg + because, Err: e.Err, cell: &c}
}

// noSpaceAfterChange is because for an out-of-space failure after a
// change: ABORTED, so the provisioner retries, but no retry converges
// while the pool stays full. "(earlier ops applied; a retry converges)"
// becomes "(earlier ops applied; out of space: free space on the pool
// first, then a retry converges)".
func noSpaceAfterChange(because string) string {
	return strings.Replace(because, "; a retry converges", "; out of space: free space on the pool first, then a retry converges", 1)
}
