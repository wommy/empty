//go:build !linux

package endpoint

import "context"

// LocalRoot is the RootSetter of a host the endpoint runs on as root. It
// needs openat2(RESOLVE_NO_SYMLINKS) and the mount table, so outside Linux
// it refuses.
type LocalRoot struct {
	// Mountinfo is the mount table read on Linux.
	Mountinfo string
}

func (LocalRoot) SetRoot(_ context.Context, dataset, _ string, _, _, _ int64) error {
	return errorf(FailedPrecondition, "volume %s: setting a volume root's owner and mode is supported on Linux only", dataset)
}
