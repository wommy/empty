package zfs

import (
	"errors"
	"fmt"
	"strings"
)

// Kind classifies a failed zfs command. Callers branch on Kind, never on
// stderr text.
type Kind int

const (
	KindUnknown Kind = iota
	KindExists
	KindNotFound
	KindParentNotFound
	KindHasChildren
	KindHasClones
	KindBusy
	KindNoSpace
	KindInvalidProperty
	KindPermission
	KindUnavailable
)

func (k Kind) String() string {
	switch k {
	case KindExists:
		return "exists"
	case KindNotFound:
		return "not found"
	case KindParentNotFound:
		return "parent not found"
	case KindHasChildren:
		return "has children"
	case KindHasClones:
		return "has dependent clones"
	case KindBusy:
		return "busy"
	case KindNoSpace:
		return "no space"
	case KindInvalidProperty:
		return "invalid property"
	case KindPermission:
		return "permission denied"
	case KindUnavailable:
		return "unavailable"
	}
	return "unknown"
}

// Sentinels for errors.Is.
var (
	ErrExists          = &Error{Kind: KindExists}
	ErrNotFound        = &Error{Kind: KindNotFound}
	ErrParentNotFound  = &Error{Kind: KindParentNotFound}
	ErrHasChildren     = &Error{Kind: KindHasChildren}
	ErrHasClones       = &Error{Kind: KindHasClones}
	ErrBusy            = &Error{Kind: KindBusy}
	ErrNoSpace         = &Error{Kind: KindNoSpace}
	ErrInvalidProperty = &Error{Kind: KindInvalidProperty}
)

// Error is a failed zfs command.
type Error struct {
	Kind    Kind
	Op      string // create, destroy, get, ...
	Dataset string
	Stderr  string
	Err     error // underlying exec error, if any
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" && e.Err != nil {
		msg = e.Err.Error()
	}
	return fmt.Sprintf("zfs %s %s: %s: %s", e.Op, e.Dataset, e.Kind, msg)
}

func (e *Error) Unwrap() error { return e.Err }

// Is matches on Kind so errors.Is(err, zfs.ErrNotFound) works.
func (e *Error) Is(target error) bool {
	var t *Error
	if errors.As(target, &t) {
		return t.Kind == e.Kind && t.Op == "" && t.Dataset == ""
	}
	return false
}

// IsNotFound reports whether err is a classified not-found. Anything else,
// including unclassified failures, is NOT absence: callers must not fall
// through to create (or report a delete as done) on a transient error.
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

// Patterns are the messages the zfs CLI prints, taken from OpenZFS:
//
//	lib/libzfs/libzfs_util.c   zfs_standard_error_fmt (errno -> aux text),
//	                           libzfs_error_description (EZFS_* text)
//	lib/libzfs/libzfs_dataset.c "parent does not exist", "snapshot is cloned"
//	cmd/zfs/zfs_main.c         "has children", "has dependent clones"
//
// Order matters: the more specific message wins.
var patterns = []struct {
	kind Kind
	text string
}{
	{KindHasClones, "has dependent clones"},
	{KindHasClones, "snapshot is cloned"},
	{KindHasChildren, "has children"},
	{KindParentNotFound, "parent does not exist"},
	{KindParentNotFound, "no such parent"},
	{KindExists, "dataset already exists"},
	{KindExists, "pool or dataset exists"},
	{KindNotFound, "dataset does not exist"},
	{KindNotFound, "no such pool or dataset"},
	{KindNotFound, "could not find any snapshots"},
	{KindBusy, "dataset is busy"},
	{KindBusy, "pool or dataset is busy"},
	{KindNoSpace, "out of space"},
	{KindInvalidProperty, "invalid property value"},
	{KindInvalidProperty, "bad property"},
	{KindInvalidProperty, "invalid property"},
	{KindInvalidProperty, "property is read-only"},
	{KindPermission, "permission denied"},
	// a missing pool is not a missing dataset: never let it read as absent
	{KindUnavailable, "no such pool"},
	{KindUnavailable, "pool i/o is currently suspended"},
	{KindUnavailable, "pool is unavailable"},
}

func classify(stderr string) Kind {
	s := strings.ToLower(stderr)
	for _, p := range patterns {
		if strings.Contains(s, p.text) {
			return p.kind
		}
	}
	return KindUnknown
}
