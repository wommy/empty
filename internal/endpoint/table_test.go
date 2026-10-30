package endpoint

import (
	"fmt"
	"testing"

	"github.com/wommy/empty/internal/zfs"
)

// Row derivations every planner decides on, one row per broken condition:
// a compound ownership rule is only pinned when each conjunct has a row
// that fails it alone.

func TestRowOwnedVolume(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		r    Row
		want bool
	}{
		{"managed, marker names it", Row{ID: "a", Type: zfs.TypeFilesystem, Managed: true, VolumeName: "a"}, true},
		{"zvol", Row{ID: "a", Type: zfs.TypeVolume, Managed: true, VolumeName: "a"}, true},
		{"uncommitted is still owned", Row{ID: "a", Managed: true, VolumeName: "a", Committed: false}, true},
		{"marker names another volume (rename, send|recv copy)", Row{ID: "a", Managed: true, VolumeName: "b"}, false},
		{"managed without a marker", Row{ID: "a", Managed: true}, false},
		{"unmanaged", Row{ID: "a", VolumeName: "a"}, false},
		{"snapshot carrying volume markers", Row{ID: "a@s", Type: zfs.TypeSnapshot, Managed: true, VolumeName: "a@s"}, false},
	} {
		if got := tc.r.OwnedVolume(); got != tc.want {
			t.Errorf("%s: OwnedVolume = %v, want %v", tc.name, got, tc.want)
		}
		if served := tc.r.ServedVolume(); served != (tc.want && tc.r.Committed) {
			t.Errorf("%s: ServedVolume = %v", tc.name, served)
		}
	}
}

func TestRowOwnedSnapshot(t *testing.T) {
	t.Parallel()
	ok := Row{ID: "a@s", Type: zfs.TypeSnapshot, Managed: true, SnapshotName: "s", SnapshotSource: "a"}
	for _, tc := range []struct {
		name string
		mut  func(*Row)
		want bool
	}{
		{"CSI snapshot of its marked source", func(*Row) {}, true},
		{"no source marker", func(r *Row) { r.SnapshotSource = "" }, true},
		{"filesystem with snapshot markers", func(r *Row) { r.Type = zfs.TypeFilesystem }, false},
		{"zvol with snapshot markers", func(r *Row) { r.Type = zfs.TypeVolume }, false},
		{"unmanaged", func(r *Row) { r.Managed = false }, false},
		{"no CSI name (administrator's, clone source)", func(r *Row) { r.SnapshotName = "" }, false},
		{"source marker names another volume", func(r *Row) { r.SnapshotSource = "b" }, false},
	} {
		r := ok
		tc.mut(&r)
		if got := r.OwnedSnapshot(); got != tc.want {
			t.Errorf("%s: OwnedSnapshot = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRowOwnedCloneSource(t *testing.T) {
	t.Parallel()
	ok := Row{ID: "a@" + cloneSourceSnapshotPrefix + "c", Type: zfs.TypeSnapshot, Managed: true, CloneSourceFor: "c"}
	for _, tc := range []struct {
		name string
		mut  func(*Row)
		want bool
	}{
		{"stamped for its clone", func(*Row) {}, true},
		{"unmanaged", func(r *Row) { r.Managed = false }, false},
		{"stamp names another clone", func(r *Row) { r.CloneSourceFor = "d" }, false},
		{"no stamp (bare reserved name)", func(r *Row) { r.CloneSourceFor = "" }, false},
		{"carries a CSI name", func(r *Row) { r.SnapshotName = "x" }, false},
		{"not a snapshot", func(r *Row) { r.Type = zfs.TypeFilesystem }, false},
		{"another name", func(r *Row) { r.ID = "a@other" }, false},
	} {
		r := ok
		tc.mut(&r)
		if clone, got := r.OwnedCloneSource(); got != tc.want || got && clone != "c" {
			t.Errorf("%s: OwnedCloneSource = %q %v, want %v", tc.name, clone, got, tc.want)
		}
	}
}

func TestRowKindAndCapacity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		r    Row
		kind VolumeKind
		cap  uint64
	}{
		{Row{Type: zfs.TypeVolume, Volsize: 5, Refquota: 9}, Block, 5},
		{Row{Type: zfs.TypeSnapshot, Volsize: 5}, Block, 5}, // a zvol's snapshot
		{Row{Type: zfs.TypeSnapshot, Refquota: 7}, Filesystem, 7},
		{Row{Type: zfs.TypeFilesystem, Refquota: 7}, Filesystem, 7},
		{Row{Type: zfs.TypeFilesystem}, Filesystem, 0}, // unlimited
	} {
		if tc.r.Kind() != tc.kind || tc.r.Capacity() != tc.cap {
			t.Errorf("%+v: kind %v cap %d, want %v %d", tc.r, tc.r.Kind(), tc.r.Capacity(), tc.kind, tc.cap)
		}
	}
}

func TestTableVolumesSkipsSnapshots(t *testing.T) {
	t.Parallel()
	tab := NewTable([]Row{snap("b@s", nil), vol("b", nil), vol("a", nil), snap("a@s", nil)})
	var got []string
	for r := range tab.Volumes() {
		got = append(got, r.ID)
	}
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("Volumes = %v, want [a b]", got)
	}
}

// SnapshotsOf is a range of the ID-sorted rows: ids that sort between a
// volume and its snapshots ("a-b" < "a/c" < "a@s") never leak into it.
func TestTableSnapshotsOf(t *testing.T) {
	t.Parallel()
	tab := NewTable([]Row{
		snap("a@s2", nil), vol("a-b", nil), snap("a-b@s", nil), vol("a", nil), snap("a@s1", nil),
		vol("a/c", nil), snap("a/c@s", nil), vol("ab", nil), snap("ab@s", nil), vol("x", nil),
	})
	for vol, want := range map[string][]string{
		"a": {"a@s1", "a@s2"}, "a-b": {"a-b@s"}, "a/c": {"a/c@s"}, "ab": {"ab@s"}, "x": nil, "none": nil, "": nil,
	} {
		var got []string
		for r := range tab.SnapshotsOf(vol) {
			got = append(got, r.ID)
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("SnapshotsOf(%q) = %v, want %v", vol, got, want)
		}
	}
}
