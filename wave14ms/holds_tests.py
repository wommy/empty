p = '/home/user/empty/.claude/worktrees/agent-a5e3d463acd0910f8/internal/endpoint/reconcile_test.go'
s = open(p).read()
a = '''		{
			// wave-5 R9b: a name zfs accepts but the client refuses
			name:  "unaddressable clone-source snapshot is reported, not planned",'''
assert s.count(a) == 1
b = '''		{
			// HOLD-REPLAN: zfs refuses a plain destroy of a held snapshot
			// (EBUSY), so a plan through it fails on every run. The volume
			// is reported; the orphan clone source on it is still released
			// with -d, which zfs accepts on a held snapshot (it marks it).
			name: "held snapshot blocks its volume's collection",
			rows: []Row{
				vol("a", nil),
				vol("b", nil),
				snap("b@volume-source-for-volume-gone", func(r *Row) { stamp(r); r.Holds = 1 }),
			},
			want:  desired("a"),
			acts:  []string{"b@volume-source-for-volume-gone:" + ReasonOrphanCloneSource},
			skips: []string{"b:volume b has held snapshot b@volume-source-for-volume-gone (1 user hold): zfs release it first"},
		},
		{
			name:  "held deferred CSI snapshot blocks its volume's collection",
			rows:  []Row{vol("a", nil), vol("b", nil), snap("b@s", func(r *Row) { r.Deferred, r.Holds = true, 2 })},
			want:  desired("a"),
			skips: []string{"b:volume b has held snapshot b@s (2 user holds): zfs release it first"},
		},
		{
			// a held origin marked for deferred destroy outlives its last
			// clone: the fixpoint must not plan its volume as if reaped
			name: "fixpoint: a held released origin is not reaped",
			rows: []Row{
				vol("a", nil),
				vol("b", nil),
				snap("b@volume-source-for-volume-c", func(r *Row) { stamp(r); r.Holds = 1 }),
				vol("c", func(r *Row) { r.Origin = "tank/v/b@volume-source-for-volume-c" }),
			},
			want: desired("a"),
			acts: []string{
				"b@volume-source-for-volume-c:" + ReasonReleasedWithVolume,
				"c:" + ReasonUndesired,
			},
			skips: []string{"b:volume b has held snapshot b@volume-source-for-volume-c (1 user hold): zfs release it first"},
		},
		{
			// OWN-12: a dataset below a volume is never the endpoint's
			// (it creates none), and zfs refuses the plain destroy ("has
			// children") on every run
			name: "child dataset blocks its volume's collection",
			rows: []Row{
				vol("a", nil),
				vol("b", func(r *Row) { r.Children = []string{"b/x", "b/y"} }),
				vol("u", func(r *Row) { r.Committed = false; r.Children = []string{"u/x"} }),
			},
			want: desired("a"),
			skips: []string{
				"b:volume b has datasets below it the endpoint did not make: b/x, b/y",
				"u:volume u has datasets below it the endpoint did not make: u/x",
			},
		},
		{
			// wave-5 R9b: a name zfs accepts but the client refuses
			name:  "unaddressable clone-source snapshot is reported, not planned",'''
s = s.replace(a, b)
open(p, 'w').write(s)

p = '/home/user/empty/.claude/worktrees/agent-a5e3d463acd0910f8/internal/endpoint/plan_test.go'
s = open(p).read()
s += '''
// DeleteVolume refuses at plan time what zfs refuses at apply: a held
// snapshot (HOLD-REPLAN) and a dataset below the volume (OWN-12). Refused
// up front, nothing of the volume is destroyed first, even with the
// foreign-snapshot opt-in.
func TestPlanDeleteVolumeRefusesHoldsAndChildren(t *testing.T) {
	cases := []struct {
		name string
		rows []Row
		want string
	}{
		{"held clone source", []Row{vol("b", nil), snap("b@volume-source-for-volume-gone", func(r *Row) {
			r.SnapshotName, r.CloneSourceFor, r.Holds = "", "gone", 1
		})}, "volume b has held snapshot b@volume-source-for-volume-gone (1 user hold): zfs release it first"},
		{"held foreign snapshot", []Row{vol("b", nil), snap("b@admin", func(r *Row) { r.Managed, r.SnapshotName, r.Holds = false, "", 3 })},
			"volume b has held snapshot b@admin (3 user holds): zfs release it first"},
		{"child dataset", []Row{vol("b", func(r *Row) { r.Children = []string{"b/x"} })},
			"volume b has datasets below it the endpoint did not make: b/x"},
	}
	for _, c := range cases {
		for _, foreign := range []bool{false, true} {
			plan, err := pl.PlanDeleteVolume(NewTable(c.rows), "b", foreign)
			if code(err) != FailedPrecondition || err.Error() != c.want || len(plan) != 0 {
				t.Errorf("%s (foreign=%v): %v %+v, want FailedPrecondition %q", c.name, foreign, err, plan, c.want)
			}
		}
	}
}
'''
open(p, 'w').write(s)
