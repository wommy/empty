package endpoint

// The recovery FSM explorer's row classifier: every row a world can hold
// maps to a named state, or to IMPOSSIBLE(...), which the explorer reports
// as a finding. Pure (a Row and its Table in, a state name out), so its
// table test runs on every PR; the explorer itself is nightly
// (fsm_explore_test.go, build tag nightly).

import (
	"cmp"
	"fmt"
	"strings"
	"testing"

	"github.com/wommy/empty/internal/zfs"
)

const fsmParent = "tank/k8s/vols"
const dcPrefix = "democratic-csi"

// state of one row, by the spec (ADR 0003 + CHECKPOINT A): ownership is a
// locally set marker; received or other-prefix datasets are Foreign.
func classify(r Row, t Table, prov map[string]string, prefix string) string {
	p := prov[r.Name]
	if r.Type != zfs.TypeSnapshot {
		switch p {
		case "recv":
			return "Foreign(recv)"
		case "dc":
			if prefix == dcPrefix {
				if r.Managed && r.Committed && r.VolumeName == r.ID {
					return "Adopted-dcsi"
				}
				return "IMPOSSIBLE(dcsi-adopt " + bits(r) + ")"
			}
			if r.Managed || r.Committed || r.VolumeName != "" {
				return "IMPOSSIBLE(foreign-dcsi with our markers " + bits(r) + ")"
			}
			return "Foreign(dcsi)"
		}
		markers := r.Committed || r.VolumeName != "" || r.SourceType != "" || r.SourceID != ""
		if !r.Managed {
			if markers {
				return "IMPOSSIBLE(unmanaged with markers " + bits(r) + ")"
			}
			return "Foreign"
		}
		if r.VolumeName == "" {
			return "IMPOSSIBLE(managed without name)"
		}
		if r.VolumeName != r.ID {
			return "Alias"
		}
		if (r.SourceType == "") != (r.SourceID == "") {
			return "IMPOSSIBLE(half source)"
		}
		clone := r.SourceType != ""
		if !clone && r.Origin != "" {
			return "IMPOSSIBLE(origin without source markers)"
		}
		if clone {
			want := ""
			switch r.SourceType {
			case "snapshot":
				want = fsmParent + "/" + r.SourceID
			case "volume":
				want = fsmParent + "/" + r.SourceID + "@" + cloneSourceSnapshotPrefix + r.ID
			}
			if r.Origin == "" {
				if r.Committed {
					return "Committed-detached"
				}
				return "IMPOSSIBLE(uncommitted clone without origin)"
			}
			if r.Origin != want {
				return "IMPOSSIBLE(origin " + r.Origin + " != source " + want + ")"
			}
		}
		switch {
		case !r.Committed && !clone:
			return "Created-uncommitted"
		case !r.Committed:
			return "Clone-uncommitted"
		case !clone:
			return "Committed"
		default:
			return "Committed-clone"
		}
	}
	vol, sn, _ := strings.Cut(r.ID, "@")
	if p == "recv" {
		return "Foreign-snap(recv)"
	}
	internal := strings.HasPrefix(sn, cloneSourceSnapshotPrefix)
	clones := hasAny(t.ClonesOf(r.Name))
	if !r.Managed {
		if r.SnapshotName != "" || r.SnapshotSource != "" {
			return "IMPOSSIBLE(unmanaged snapshot with markers)"
		}
		if r.Deferred && !clones && r.Holds == 0 {
			return "IMPOSSIBLE(deferred without clones or holds)"
		}
		if internal {
			src, ok := t.Get(vol)
			if !ok || isForeign(classify(src, t, prov, prefix)) {
				return "Foreign-snap"
			}
			switch {
			case r.Deferred:
				return "CloneSrc-deferred"
			case clones:
				return "CloneSrc-live"
			default:
				return "CloneSrc-orphan"
			}
		}
		return "Foreign-snap"
	}
	if internal {
		// the endpoint's own clone source: stamped managed + clone_source_for
		// at creation (Row.OwnedCloneSource), the normal state of a clone
		if _, ours := r.OwnedCloneSource(); ours {
			switch {
			case r.Deferred:
				return "OwnCloneSrc-deferred"
			case clones:
				return "OwnCloneSrc-live"
			default:
				return "OwnCloneSrc-orphan"
			}
		}
		return "IMPOSSIBLE(managed internal snapshot without our stamp)"
	}
	if r.SnapshotName == "" {
		return "IMPOSSIBLE(managed snapshot without CSI name)"
	}
	if r.SnapshotSource != "" && r.SnapshotSource != vol {
		return "Alias-snap"
	}
	if r.Deferred {
		// zfs keeps a snapshot marked for deferred destroy while a clone
		// or a user hold pins it, and reaps it with the last of them
		if !clones && r.Holds == 0 {
			return "IMPOSSIBLE(deferred without clones or holds)"
		}
		return "CSI-snap-deferred"
	}
	return "CSI-snap"
}

func bits(r Row) string {
	return fmt.Sprintf("M=%v C=%v VN=%q ST=%q", r.Managed, r.Committed, r.VolumeName, r.SourceType)
}

func isForeign(st string) bool {
	return strings.HasPrefix(st, "Foreign") || st == "Alias" || st == "Alias-snap" || strings.HasPrefix(st, "IMPOSSIBLE(foreign")
}

// TestClassify pins the classifier on single rows (AN-09 among them: the
// clone source the endpoint stamps at CreateVolume-from-volume is a legal
// state, which the explorer once reported as IMPOSSIBLE at depth 2).
func TestClassify(t *testing.T) {
	v := func(id string, mut func(*Row)) Row {
		r := Row{Name: fsmParent + "/" + id, ID: id, Type: zfs.TypeFilesystem, Managed: true, Committed: true, VolumeName: id}
		if mut != nil {
			mut(&r)
		}
		return r
	}
	sn := func(id string, mut func(*Row)) Row {
		_, name, _ := strings.Cut(id, "@")
		r := Row{Name: fsmParent + "/" + id, ID: id, Type: zfs.TypeSnapshot, Managed: true, SnapshotName: name}
		if mut != nil {
			mut(&r)
		}
		return r
	}
	src := "a@" + cloneSourceSnapshotPrefix + "b"
	stamped := func(r *Row) { r.SnapshotName, r.CloneSourceFor = "", "b" }
	cloneOfA := v("b", func(r *Row) { r.SourceType, r.SourceID, r.Origin = "volume", "a", fsmParent+"/"+src })
	cases := []struct {
		name   string
		row    Row
		others []Row
		prov   string
		prefix string
		want   string
	}{
		{name: "owned committed volume", row: v("a", nil), want: "Committed"},
		{name: "uncommitted volume", row: v("a", func(r *Row) { r.Committed = false }), want: "Created-uncommitted"},
		{name: "committed clone", row: cloneOfA, others: []Row{v("a", nil), sn(src, stamped)}, want: "Committed-clone"},
		{name: "markers naming another volume", row: v("a", func(r *Row) { r.VolumeName = "z" }), want: "Alias"},
		{name: "managed without a name", row: v("a", func(r *Row) { r.VolumeName = "" }), want: "IMPOSSIBLE(managed without name)"},
		{name: "unmanaged plain dataset", row: v("a", func(r *Row) { r.Managed, r.Committed, r.VolumeName = false, false, "" }), want: "Foreign"},
		{name: "received copy", row: v("b", func(r *Row) { r.Managed, r.Committed, r.VolumeName = false, false, "" }), prov: "recv", want: "Foreign(recv)"},
		{name: "democratic-csi volume, adoption mode", row: v("a", nil), prov: "dc", prefix: dcPrefix, want: "Adopted-dcsi"},
		{name: "democratic-csi volume, own prefix", row: v("a", func(r *Row) { r.Managed, r.Committed, r.VolumeName = false, false, "" }),
			prov: "dc", want: "Foreign(dcsi)"},

		// AN-09: the endpoint's own, stamped clone source
		{name: "stamped clone source with its clone", row: sn(src, stamped), others: []Row{v("a", nil), cloneOfA}, want: "OwnCloneSrc-live"},
		{name: "stamped clone source, clone gone", row: sn(src, stamped), others: []Row{v("a", nil)}, want: "OwnCloneSrc-orphan"},
		{name: "stamped clone source marked deferred", row: sn(src, func(r *Row) { stamped(r); r.Deferred = true }),
			others: []Row{v("a", nil), cloneOfA}, want: "OwnCloneSrc-deferred"},
		{name: "managed reserved name stamped for another clone", row: sn(src, func(r *Row) { stamped(r); r.CloneSourceFor = "c" }),
			others: []Row{v("a", nil)}, want: "IMPOSSIBLE(managed internal snapshot without our stamp)"},

		{name: "unstamped reserved name on an owned volume", row: sn(src, func(r *Row) { r.Managed, r.SnapshotName = false, "" }),
			others: []Row{v("a", nil)}, want: "CloneSrc-orphan"},
		{name: "unstamped reserved name on a foreign volume", row: sn(src, func(r *Row) { r.Managed, r.SnapshotName = false, "" }),
			others: []Row{v("a", func(r *Row) { r.Managed, r.Committed, r.VolumeName = false, false, "" })}, want: "Foreign-snap"},
		{name: "CSI snapshot", row: sn("a@s", nil), others: []Row{v("a", nil)}, want: "CSI-snap"},
		{name: "CSI snapshot of another volume", row: sn("a@s", func(r *Row) { r.SnapshotSource = "z" }), others: []Row{v("a", nil)}, want: "Alias-snap"},
		{name: "managed snapshot without a CSI name", row: sn("a@s", func(r *Row) { r.SnapshotName = "" }), others: []Row{v("a", nil)},
			want: "IMPOSSIBLE(managed snapshot without CSI name)"},
		{name: "deleted CSI snapshot kept by a hold", row: sn("a@s", func(r *Row) { r.Deferred, r.Holds = true, 1 }), others: []Row{v("a", nil)},
			want: "CSI-snap-deferred"},
		{name: "deferred CSI snapshot with nothing pinning it", row: sn("a@s", func(r *Row) { r.Deferred = true }), others: []Row{v("a", nil)},
			want: "IMPOSSIBLE(deferred without clones or holds)"},
		{name: "unmanaged deferred snapshot kept by a hold", row: sn("a@x", func(r *Row) { r.Managed, r.SnapshotName, r.Deferred, r.Holds = false, "", true, 2 }),
			others: []Row{v("a", nil)}, want: "Foreign-snap"},
	}
	for _, c := range cases {
		prefix := cmp.Or(c.prefix, DefaultPropertyPrefix)
		prov := map[string]string{}
		if c.prov != "" {
			prov[c.row.Name] = c.prov
		}
		if got := classify(c.row, NewTable(append([]Row{c.row}, c.others...)), prov, prefix); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}
