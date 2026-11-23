package endpoint

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// LocalRoot on a temporary directory made to look like a dataset's mount
// through an injected mount table. Owner changes use the test's own uid and
// gid, so it runs unprivileged (the unit job) as well as as root.
func TestLocalRoot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base := t.TempDir()
	mp := filepath.Join(base, "vols", "a")
	if err := os.MkdirAll(mp, 0o755); err != nil {
		t.Fatal(err)
	}
	var st unix.Stat_t
	if err := unix.Stat(mp, &st); err != nil {
		t.Fatal(err)
	}
	dev := fmt.Sprintf("%d:%d", unix.Major(st.Dev), unix.Minor(st.Dev))
	table := func(t *testing.T, lines ...string) LocalRoot {
		f := filepath.Join(t.TempDir(), "mountinfo")
		var b []byte
		for _, l := range lines {
			b = append(b, l+"\n"...)
		}
		if err := os.WriteFile(f, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return LocalRoot{Mountinfo: f}
	}
	ours := func(point string) string { return "40 21 " + dev + " / " + point + " rw shared:9 - zfs tank/v/a rw" }
	uid, gid := int64(os.Getuid()), int64(os.Getgid())
	modeOf := func(t *testing.T, p string) uint32 {
		var s unix.Stat_t
		if err := unix.Lstat(p, &s); err != nil {
			t.Fatal(err)
		}
		return s.Mode & 0o7777
	}

	t.Run("sets owner and mode, and again", func(t *testing.T) {
		l := table(t, "21 1 0:1 / / rw - ext4 /dev/root rw", ours(mp))
		for range 2 { // idempotent
			if err := l.SetRoot(ctx, "tank/v/a", mp, uid, gid, 0o2770); err != nil {
				t.Fatal(err)
			}
			var s unix.Stat_t
			if err := unix.Lstat(mp, &s); err != nil {
				t.Fatal(err)
			}
			if int64(s.Uid) != uid || int64(s.Gid) != gid || s.Mode&0o7777 != 0o2770 {
				t.Fatalf("root %d:%d %o", s.Uid, s.Gid, s.Mode&0o7777)
			}
		}
		// -1 leaves the mode alone
		if err := l.SetRoot(ctx, "tank/v/a", mp, uid, -1, -1); err != nil || modeOf(t, mp) != 0o2770 {
			t.Fatalf("%v, mode %o", err, modeOf(t, mp))
		}
	})

	// every refusal leaves the directory as it was
	link := filepath.Join(base, "link")
	if err := os.Symlink(filepath.Join(base, "vols"), link); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(base, "other")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path string
		lines      []string
		code       Code
	}{
		// a symlink anywhere on the path: the table even agrees, so only
		// RESOLVE_NO_SYMLINKS refuses it
		{"symlink on the path", filepath.Join(link, "a"), []string{ours(filepath.Join(link, "a"))}, FailedPrecondition},
		{"symlink as the mountpoint", link, []string{ours(link)}, FailedPrecondition},
		{"not mounted", mp, []string{"21 1 0:1 / / rw - ext4 /dev/root rw"}, Aborted},
		{"another dataset", mp, []string{"40 21 " + dev + " / " + mp + " rw - zfs tank/v/b rw"}, FailedPrecondition},
		{"not zfs", mp, []string{"40 21 " + dev + " / " + mp + " rw - tmpfs tank/v/a rw"}, FailedPrecondition},
		{"a bind mount of a subdirectory", mp, []string{"40 21 " + dev + " /sub " + mp + " rw - zfs tank/v/a rw"}, FailedPrecondition},
		{"another device", mp, []string{"40 21 999:999 / " + mp + " rw - zfs tank/v/a rw"}, FailedPrecondition},
		{"covered by a later mount", mp, []string{ours(mp), "41 40 0:99 / " + mp + " rw - tmpfs none rw"}, FailedPrecondition},
		{"relative", "vols/a", []string{ours("vols/a")}, FailedPrecondition},
		{"unclean", mp + "/.", []string{ours(mp + "/.")}, FailedPrecondition},
		{"missing", filepath.Join(base, "gone"), []string{ours(filepath.Join(base, "gone"))}, Aborted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := modeOf(t, mp)
			err := table(t, tc.lines...).SetRoot(ctx, "tank/v/a", tc.path, uid, gid, 0o701)
			if CodeOf(err) != tc.code {
				t.Fatalf("%v, want code %d", err, tc.code)
			}
			if got := modeOf(t, mp); got != before || modeOf(t, other) != 0o700 {
				t.Fatalf("refused, but the mode changed: %o, was %o", got, before)
			}
		})
	}
}
