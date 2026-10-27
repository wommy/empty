package endpoint

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
)

// RootSetter sets a mounted filesystem volume's root directory owner and
// mode: the directory dataset is mounted on at mountpoint, never anything
// a symlink leads to. uid, gid and mode of -1 are left as they are. It
// must be idempotent: CreateVolume's resume applies it again after a
// crash, and an attribute already at its value is no change.
type RootSetter interface {
	SetRoot(ctx context.Context, dataset, mountpoint string, uid, gid, mode int64) error
}

// rootAttrs is what a create sets on the volume's root: -1 leaves one as
// it is.
type rootAttrs struct{ uid, gid, mode int64 }

var noRoot = rootAttrs{-1, -1, -1}

func (a rootAttrs) set() bool { return a != noRoot }

// props renders a as an OpSetRoot's Props (plan text and golden plans):
// uid and gid in decimal, mode in octal; an unset one is absent.
func (a rootAttrs) props() map[string]string {
	m := map[string]string{}
	if a.uid >= 0 {
		m["uid"] = strconv.FormatInt(a.uid, 10)
	}
	if a.gid >= 0 {
		m["gid"] = strconv.FormatInt(a.gid, 10)
	}
	if a.mode >= 0 {
		m["mode"] = fmt.Sprintf("%04o", a.mode)
	}
	return m
}

// rootOf reads an OpSetRoot's Props back.
func rootOf(props map[string]string) (rootAttrs, error) {
	a := noRoot
	for k, v := range props {
		var n int64
		var err error
		switch k {
		case "uid", "gid":
			n, err = strconv.ParseInt(v, 10, 64)
			if err == nil && (n < 0 || n > MaxRootID) {
				err = fmt.Errorf("out of range")
			}
		case "mode":
			n, err = strconv.ParseInt(v, 8, 64)
			if err == nil && (n < 0 || n > 0o7777) {
				err = fmt.Errorf("out of range")
			}
		default:
			err = fmt.Errorf("unknown attribute")
		}
		if err != nil {
			return noRoot, fmt.Errorf("root attribute %s=%q: %v", k, v, err)
		}
		switch k {
		case "uid":
			a.uid = n
		case "gid":
			a.gid = n
		case "mode":
			a.mode = n
		}
	}
	return a, nil
}

// validRoot checks an NFS export's root owner and mode on their own.
func validRoot(n *NFSExport) (rootAttrs, error) {
	a := noRoot
	for _, f := range []struct {
		name string
		v    *uint32
		max  uint32
		dst  *int64
	}{{"uid", n.UID, MaxRootID, &a.uid}, {"gid", n.GID, MaxRootID, &a.gid}, {"mode", n.Mode, 0o7777, &a.mode}} {
		if f.v == nil {
			continue
		}
		if *f.v > f.max {
			if f.name == "mode" {
				return noRoot, errorf(InvalidArgument, "NFS root mode %o: want permission bits, at most 7777 (octal)", *f.v)
			}
			return noRoot, errorf(InvalidArgument, "NFS root %s %d: want 0-%d", f.name, *f.v, f.max)
		}
		*f.dst = int64(*f.v)
	}
	return a, nil
}

// mountEntry is one line of /proc/self/mountinfo (proc(5)).
type mountEntry struct {
	major, minor uint32
	root         string // the directory of the filesystem mounted here
	point        string // mount point, in the reader's namespace
	fstype       string
	source       string // for zfs: the dataset
}

// parseMountinfo reads /proc/self/mountinfo, unescaping the octal escapes
// the kernel writes for space, tab, newline and backslash. A line it
// cannot read is skipped: nothing is ever decided from one.
func parseMountinfo(b []byte) []mountEntry {
	var out []mountEntry
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		sep := -1
		for i := 6; i < len(f); i++ {
			if f[i] == "-" {
				sep = i
				break
			}
		}
		if len(f) < 6 || sep < 0 || sep+2 >= len(f) {
			continue
		}
		maj, min, ok := strings.Cut(f[2], ":")
		if !ok {
			continue
		}
		mj, err1 := strconv.ParseUint(maj, 10, 32)
		mn, err2 := strconv.ParseUint(min, 10, 32)
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, mountEntry{
			major: uint32(mj), minor: uint32(mn),
			root: unescapeMount(f[3]), point: unescapeMount(f[4]),
			fstype: f[sep+1], source: unescapeMount(f[sep+2]),
		})
	}
	return out
}

// unescapeMount undoes the kernel's mangle(): "\ooo" for ' ', '\t', '\n'
// and '\\'.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// mountedAt is the mount on top at point: the last entry for it, since a
// later mount over the same point hides the earlier ones.
func mountedAt(entries []mountEntry, point string) (mountEntry, bool) {
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].point == point {
			return entries[i], true
		}
	}
	return mountEntry{}, false
}
