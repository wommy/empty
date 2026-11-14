package endpoint

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// LocalRoot is the RootSetter of a host the endpoint runs on as root. It
// opens the mountpoint with openat2(RESOLVE_NO_SYMLINKS), so no component
// of the path may be a symlink (a tenant cannot steer the chown through
// one: the mountpoint's ancestors are the parent's directories, and an
// export's client can plant links inside a volume, never above it). It
// then checks, on the open directory, that it is the root of dataset's
// mount: the mount on top at mountpoint is zfs, from dataset, its root
// "/", and the directory is on that mount's device. Only then does it
// fchown and fchmod that descriptor: nothing is ever looked up by path
// twice.
type LocalRoot struct {
	// Mountinfo is the mount table read. Default /proc/self/mountinfo.
	Mountinfo string
}

func (l LocalRoot) SetRoot(ctx context.Context, dataset, mountpoint string, uid, gid, mode int64) error {
	if err := ctx.Err(); err != nil {
		return &Error{Code: Aborted, Msg: "set root of " + dataset, Err: err}
	}
	if !filepath.IsAbs(mountpoint) || filepath.Clean(mountpoint) != mountpoint {
		return errorf(FailedPrecondition, "volume %s: mountpoint %q is not a clean absolute path; its root's owner and mode are not set", dataset, mountpoint)
	}
	fd, err := unix.Openat2(unix.AT_FDCWD, mountpoint, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	switch {
	case errors.Is(err, unix.ELOOP), errors.Is(err, unix.ENOTDIR):
		// O_NOFOLLOW with O_DIRECTORY on a final symlink is ENOTDIR
		return errorf(FailedPrecondition, "volume %s: a symlink is on the path to its mountpoint %s; its root's owner and mode are set only through real directories", dataset, mountpoint)
	case errors.Is(err, unix.ENOENT):
		return &Error{Code: Aborted, Msg: fmt.Sprintf("volume %s: mountpoint %s is missing (not mounted?); a retry mounts it", dataset, mountpoint), Err: err}
	case errors.Is(err, unix.ENOSYS):
		return &Error{Code: FailedPrecondition, Msg: "openat2 is not available (Linux 5.6 or later): the volume root's owner and mode cannot be set safely", Err: err}
	case err != nil:
		return &Error{Code: Internal, Msg: "open " + mountpoint, Err: err}
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return &Error{Code: Internal, Msg: "stat " + mountpoint, Err: err}
	}
	path := l.Mountinfo
	if path == "" {
		path = "/proc/self/mountinfo"
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return &Error{Code: Internal, Msg: "read the mount table", Err: err}
	}
	m, ok := mountedAt(parseMountinfo(b), mountpoint)
	if !ok {
		return &Error{Code: Aborted, Msg: fmt.Sprintf("volume %s is not mounted at %s; a retry mounts it", dataset, mountpoint)}
	}
	if m.fstype != "zfs" || m.source != dataset || m.root != "/" ||
		unix.Major(st.Dev) != m.major || unix.Minor(st.Dev) != m.minor {
		return errorf(FailedPrecondition, "volume %s: %s is not its mount (mounted there: %s %s, root %s, device %d:%d; the directory is on %d:%d); its root's owner and mode are not set",
			dataset, mountpoint, m.fstype, m.source, m.root, m.major, m.minor, unix.Major(st.Dev), unix.Minor(st.Dev))
	}
	if uid >= 0 || gid >= 0 {
		// -1 leaves one unchanged (chown(2))
		if err := unix.Fchown(fd, int(uid), int(gid)); err != nil {
			return rootSysErr("chown", dataset, mountpoint, err)
		}
	}
	if mode >= 0 {
		if err := unix.Fchmod(fd, uint32(mode)); err != nil {
			return rootSysErr("chmod", dataset, mountpoint, err)
		}
	}
	return nil
}

// rootSysErr: a refusal the host keeps making (not root, a read-only
// mount) is final; anything else is the caller's retry.
func rootSysErr(op, dataset, mountpoint string, err error) error {
	code := Internal
	if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EROFS) {
		code = FailedPrecondition
	}
	return &Error{Code: code, Msg: fmt.Sprintf("volume %s: %s %s", dataset, op, mountpoint), Err: err}
}
