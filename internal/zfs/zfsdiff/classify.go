package zfsdiff

import (
	"context"
	"errors"
	"strings"

	"github.com/wommy/empty/internal/zfs"
)

// Class names a failure by the OpenZFS message that produced it, finer
// than zfs.Kind. Each pattern quotes a string from the 2.4.4 source;
// the comment is where it is printed (cmd/zfs/, lib/libzfs/).
type Class string

var classes = []struct {
	c    Class
	text string
}{
	{"clones", "has dependent clones"},                      // zfs_main.c:1439
	{"cloned", "snapshot is cloned"},                        // libzfs_dataset.c:3982
	{"children", "has children"},                            // zfs_main.c:1418
	{"held", "it's being held"},                             // libzfs_dataset.c:3993
	{"nosnaps", "could not find any snapshots"},             // zfs_main.c:1764
	{"nopool", "no such pool"},                              // libzfs_dataset.c:3492
	{"noparent", "parent does not exist"},                   // libzfs_dataset.c:3509
	{"parent-not-fs", "parent is not a filesystem"},         // libzfs_dataset.c:3529
	{"exists", "dataset already exists"},                    // libzfs_util.c (EEXIST)
	{"noent", "dataset does not exist"},                     // libzfs_util.c (ENOENT)
	{"busy", "dataset is busy"},                             // libzfs_util.c (EBUSY)
	{"tag-exists", "tag already exists"},                    // libzfs_util.c EZFS_REFTAG_HOLD
	{"no-tag", "no such tag"},                               // libzfs_util.c EZFS_REFTAG_RELE
	{"nospace-quota", "size is less than current used"},     // libzfs_util.c:546
	{"nospace-resv", "size is greater than available"},      // libzfs_util.c:554
	{"nospace", "out of space"},                             // libzfs_util.c (ENOSPC)
	{"readonly", "is readonly"},                             // libzfs_dataset.c:1206
	{"readonly", "property is read-only"},                   // zfs_main.c:2523
	{"noinherit", "cannot be inherited"},                    // zfs_main.c:2529
	{"noinherit", "cannot be reverted"},                     // zfs_main.c:2545
	{"proptype", "does not apply to datasets of this type"}, // libzfs_dataset.c:1197
	{"proptype", "property doesn't apply"},                  // libzfs_util.c EZFS_PROPTYPE
	{"snapprop", "can not be modified for snapshots"},       // libzfs_dataset.c:1091
	{"badprop", "use 'none' to disable"},                    // libzfs_util.c:1832
	{"badprop", "only allowed on volumes"},                  // libzfs_util.c:1873
	{"badprop", "is invalid value for"},                     // libzfs_util.c:1892
	{"badprop", "must be one of"},                           // libzfs_util.c:1911
	{"badprop", "must be power of 2"},                       // libzfs_dataset.c:1255
	{"badprop", "multiple of volume block size"},            // libzfs_dataset.c:1521, :3790
	{"badprop", "cannot be zero"},                           // libzfs_dataset.c:1531, :3783
	{"badprop", "must be an absolute path"},                 // libzfs_dataset.c:1352
	{"badprop", "invalid property"},                         // libzfs_dataset.c:1189
	{"badprop", "bad numeric value"},                        // libzfs_util.c:1685
	{"badprop", "missing '=' for"},                          // zfs_main.c parseprop
	{"notclone", "not a cloned filesystem"},                 // libzfs_dataset.c:4120
	{"notsnap", "is not a snapshot"},                        // zfs_main.c:6797
	{"pool-op", "operation does not apply to pools"},        // zfs_main.c:1866
	{"conflict", "conflicting snapshot"},                    // libzfs_dataset.c:4142
	{"crosssnap", "must be part of same dataset"},           // libzfs_dataset.c:4519
	{"descendant", "cannot be a descendant"},                // libzfs_dataset.c:4555
	{"badname", "invalid character"},                        // zfs_namecheck messages
	{"badname", "missing '@' delimiter"},
	{"badname", "multiple '@' delimiters"},
	{"badname", "empty component"},
	{"badname", "trailing slash"},
	{"badname", "leading slash"},
	{"badname", "name is too long"},
}

// ClassOf classifies stderr of a failed command; "" for success.
func ClassOf(code int, stderr string) Class {
	if code == 0 {
		return ""
	}
	if code == 2 { // usage(B_FALSE), zfs_main.c:644: the message varies
		return "usage"
	}
	s := strings.ToLower(stderr)
	for _, p := range classes {
		if strings.Contains(s, strings.ToLower(p.text)) {
			return p.c
		}
	}
	return "unknown"
}

// KindOf is the zfs.Kind the production client gives stderr, obtained
// through the client itself so the mapping is not copied.
func KindOf(stderr string) zfs.Kind {
	r := stubRunner{stderr: stderr}
	_, err := zfs.New(r).Get(context.Background(), "p/x", "type")
	var e *zfs.Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return zfs.KindUnknown
}

type stubRunner struct{ stderr string }

func (r stubRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return nil, &zfs.ExitError{Code: 1, Stderr: r.stderr}
}
