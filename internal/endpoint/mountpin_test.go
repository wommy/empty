package endpoint

import (
	"slices"
	"testing"
)

// csi-nfs-design §4: a new filesystem's mountpoint is pinned locally in the
// create itself, next to F12's mount options, so volume_context share does
// not follow a parent that moves. Pure planner tests.
func TestPlanCreateVolumePinsMountpoint(t *testing.T) {
	t.Parallel()
	src := fsv("src", func(r *Row) { r.Refquota = 1 << 30 })
	srcSnap := snap("src@s", func(r *Row) { r.Refquota = 0 })
	cases := []struct {
		name, parentMount string
		req               CreateVolumeRequest
		want              string // the create's or clone's mountpoint; "" none
	}{
		{"create", "/tank/v", CreateVolumeRequest{Name: "a"}, "/tank/v/a"},
		{"a parent at the root", "/", CreateVolumeRequest{Name: "a"}, "/a"},
		{"restore", "/tank/v", CreateVolumeRequest{Name: "a", SourceSnapshotID: "src@s"}, "/tank/v/a"},
		{"clone", "/tank/v", CreateVolumeRequest{Name: "a", SourceVolumeID: "src"}, "/tank/v/a"},
		{"NFS export", "/mnt/k8s", nfsReq("a", &NFSExport{Clients: []string{"10.20.0.0/24"}}), "/mnt/k8s/a"},
		// nothing to pin: zfs mounts the volume nowhere either way
		{"parent mountpoint none", "none", CreateVolumeRequest{Name: "a"}, ""},
		{"parent mountpoint legacy", "legacy", CreateVolumeRequest{Name: "a"}, ""},
		{"not read (a bare planner)", "", CreateVolumeRequest{Name: "a"}, ""},
		{"zvol", "/tank/v", CreateVolumeRequest{Name: "a", Kind: Block, CapacityBytes: 1 << 20}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := nfsPl
			p.parentMount = tc.parentMount
			plan, err := p.PlanCreateVolume(NewTable([]Row{src, srcSnap}), tc.req)
			if err != nil {
				t.Fatal(err)
			}
			var made Op
			for _, op := range plan {
				if op.Kind == OpCreateFS || op.Kind == OpCreateVol || op.Kind == OpClone {
					made = op
				}
			}
			if got := made.Props["mountpoint"]; got != tc.want {
				t.Fatalf("mountpoint %q, want %q: %v", got, tc.want, plan)
			}
			// F12 stays: the pin extends the same block
			if tc.req.Kind == Filesystem {
				for _, k := range []string{"setuid", "exec", "devices", "sharesmb"} {
					if made.Props[k] != "off" {
						t.Fatalf("%s=%q, want off (F12): %v", k, made.Props[k], plan)
					}
				}
			}
		})
	}
	// a volume that exists is never re-pinned: a resume or a repeat writes
	// no mountpoint (zfs would unmount and remount it)
	p := nfsPl
	p.parentMount = "/tank/v"
	for _, row := range []Row{fsv("a", nil), fsv("a", func(r *Row) { r.MountLocal, r.Committed = false, false })} {
		plan, err := p.PlanCreateVolume(NewTable([]Row{row}), CreateVolumeRequest{Name: "a"})
		if err != nil {
			t.Fatal(err)
		}
		for _, op := range plan {
			if _, ok := op.Props["mountpoint"]; ok {
				t.Fatalf("existing %+v: re-pinned: %v", row, plan)
			}
		}
	}
}

// An unpinned committed filesystem is reported, never rewritten.
func TestPlanReconcileMountpointNotPinned(t *testing.T) {
	t.Parallel()
	unpinned := func(mut func(*Row)) Row {
		return fsv("a", func(r *Row) {
			r.MountLocal = false
			if mut != nil {
				mut(r)
			}
		})
	}
	cases := []struct {
		name  string
		row   Row
		skips []string
	}{
		{"pinned", fsv("a", nil), nil},
		{"inherited", unpinned(nil), []string{"a:" + SkipMountpointNotPinned}},
		{"inherited, unmounted", unpinned(func(r *Row) { r.Mounted = false }), []string{"a:" + SkipMountpointNotPinned}},
		{"inherited none", unpinned(func(r *Row) { r.Mount = "none" }), nil},
		{"inherited legacy", unpinned(func(r *Row) { r.Mount = "legacy" }), nil},
		{"uncommitted (in flight)", unpinned(func(r *Row) { r.Committed = false }), nil},
		{"zvol", vol("a", func(r *Row) { r.Type = "volume"; r.Volsize, r.BlockSz = 1<<20, 16384 }), nil},
	}
	for _, tc := range cases {
		plan, err := nfsPl.PlanReconcile(table(tc.row), desired("a"))
		if err != nil {
			t.Fatal(err)
		}
		acts, skips := summary(plan)
		if !slices.Equal(skips, tc.skips) || len(acts) > 0 {
			t.Errorf("%s: actions %q, skipped %q, want none and %q", tc.name, acts, skips, tc.skips)
		}
	}
}
