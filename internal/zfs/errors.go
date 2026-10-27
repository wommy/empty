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
	// KindPrecondition: a pool or dataset state only an administrator can
	// change (an unloaded encryption key, a pool feature not enabled).
	KindPrecondition
	// KindInterrupted: the command was killed by its context. It may or may
	// not have done its work; callers re-scan rather than guess.
	KindInterrupted
	kindLast = KindInterrupted
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
	case KindPrecondition:
		return "failed precondition"
	case KindInterrupted:
		return "interrupted, outcome unknown"
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
	ErrUnavailable     = &Error{Kind: KindUnavailable}
	ErrPrecondition    = &Error{Kind: KindPrecondition}
	ErrPermission      = &Error{Kind: KindPermission}
	ErrInterrupted     = &Error{Kind: KindInterrupted}
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
//	                           zfs_setprop_error (property ENOSPC),
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
	// libzfs_dataset.c zfs_destroy_snaps_nvl, EBUSY from a user hold:
	// "cannot destroy snapshot %s: it's being held. Run 'zfs holds -r %s'
	// to see holders." Busy: it clears once the holder releases.
	{KindBusy, "it's being held"},
	{KindNoSpace, "out of space"},
	// zfs_setprop_error ENOSPC: reservation/refreservation (incl. volsize
	// on a thick zvol) larger than the pool can give
	{KindNoSpace, "size is greater than available space"},
	{KindInvalidProperty, "invalid property value"},
	{KindInvalidProperty, "bad property"},
	{KindInvalidProperty, "invalid property"},
	{KindInvalidProperty, "property is read-only"},
	// zfs_setprop_error ENOSPC: quota/refquota below what is already used
	{KindInvalidProperty, "size is less than current used or reserved space"},
	// libzfs_util.c zprop_parse_value: an index value this userland's
	// table lacks, e.g. "'snapdir' must be one of 'hidden | visible | disabled'"
	{KindInvalidProperty, "must be one of"},
	// libzfs_dataset.c zfs_valid_proplist: a block size above the pool's
	// maximum (no large_blocks feature): "'recordsize' must be power of 2
	// from 512B to 128K"
	{KindInvalidProperty, "must be power of 2"},
	// libzfs_dataset.c zfs_create and zfs_clone, EACCES: "encryption root's
	// key is not loaded or provided" (needs zfs load-key)
	{KindPrecondition, "key is not loaded"},
	// libzfs_dataset.c zfs_create and libzfs_util.c zfs_setprop_error,
	// ENOTSUP: "pool must be upgraded to set this property or value" (a
	// feature such as zstd_compress is not enabled on the pool)
	{KindPrecondition, "pool must be upgraded"},
	// OpenZFS 2.4.4 texts the zfsdiff replay found classified as unknown
	// (internal/zfs/zfsdiff/classify.go cites each source line):
	// libzfs_dataset.c zfs_valid_proplist, a set-once property (set,
	// clone -o): "'volblocksize' is readonly"
	{KindInvalidProperty, "is readonly"},
	// zfs_valid_proplist: "'volsize' does not apply to datasets of this type"
	{KindInvalidProperty, "does not apply to datasets of this type"},
	// zfs_valid_proplist and zfs_create: "'volsize' cannot be zero",
	// "volume size cannot be zero"; "'volsize' must be a multiple of
	// volume block size (16K)"
	{KindInvalidProperty, "cannot be zero"},
	{KindInvalidProperty, "must be a multiple of"},
	// libzfs_util.c zprop_parse_value: "use 'none' to disable quota/refquota"
	{KindInvalidProperty, "use 'none' to disable"},
	// zfs_valid_proplist on a snapshot: "this property can not be
	// modified for snapshots"
	{KindInvalidProperty, "can not be modified for snapshots"},
	// zfs_main.c parseprop (exit 255): "property 'atime' specified
	// multiple times"
	{KindInvalidProperty, "specified multiple times"},
	// zfs_namecheck (EZFS_INVALIDNAME): a malformed name is a bad
	// argument, final like a bad property
	{KindInvalidProperty, "missing '@' delimiter"},
	// libzfs_dataset.c zfs_create: a zvol as the parent, configuration
	// only an administrator changes
	{KindPrecondition, "parent is not a filesystem"},
	// zfs_promote: "not a cloned filesystem"
	{KindPrecondition, "not a cloned filesystem"},
	// zfs_promote, EZFS_EXISTS: "conflicting snapshot 's1' from parent 'p'"
	{KindExists, "conflicting snapshot"},
	// libzfs_util.c EZFS_REFTAG_HOLD / EZFS_REFTAG_RELE: the hold is there
	// already / is not there
	{KindExists, "tag already exists"},
	{KindNotFound, "no such tag"},
	// zfs_main.c share_mount_one, `zfs mount` of a mounted filesystem:
	// "cannot mount 'tank/a': filesystem already mounted"
	{KindExists, "filesystem already mounted"},
	{KindPermission, "permission denied"},
	// a missing pool is not a missing dataset: never let it read as absent
	{KindUnavailable, "no such pool"},
	{KindUnavailable, "pool i/o is currently suspended"},
	{KindUnavailable, "pool is unavailable"},
	// libzfs_util.c libzfs_error_description, EZFS_POOLREADONLY (EROFS in
	// zfs_standard_error_fmt): a pool imported or dropped read-only; reads
	// still work, writes wait for the administrator
	{KindUnavailable, "pool is read-only"},
	// libzfs_util.c zfs_standard_error_fmt, ZFS_ERR_IOC_CMD_UNAVAIL: "the
	// loaded zfs module does not support this operation. A reboot may be
	// required to enable this operation." (userland newer than the module)
	{KindUnavailable, "does not support this operation"},
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
