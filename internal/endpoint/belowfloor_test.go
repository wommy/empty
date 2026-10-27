package endpoint

import (
	"slices"
	"testing"
)

// csi-nfs-design §2 as built, §10 D2: the floor bounds new writes, not
// ownership. A value of ours with a network below -nfs-min-prefix (an
// admin's edit, or written before the floor was raised) is reported by
// Reconcile, never rewritten: a rewrite would revoke clients.
func TestPlanReconcileNFSBelowFloor(t *testing.T) {
	t.Parallel()
	floor8 := nfsPl
	floor8.NFSMinPrefix = 8
	unset := nfsPl
	unset.NFSMinPrefix = 0 // a bare planner: the default floor, /16
	below := func(hosts string) []string { return []string{"a:" + SkipNFSBelowFloor + ": " + hosts} }
	cases := []struct {
		name  string
		pl    Planner
		share string
		local bool
		skips []string
	}{
		{"at the floor", nfsPl, "rw=10.20.0.0/16", true, nil},
		{"below it", nfsPl, "rw=10.0.0.0/8", true, below("10.0.0.0/8 rw")},
		{"below it, default floor", unset, "rw=10.0.0.0/8", true, below("10.0.0.0/8 rw")},
		{"several, each with its mode", nfsPl, "rw=10.0.0.0/8:192.168.1.0/24,ro=172.16.0.0/12", true,
			below("10.0.0.0/8 rw, 172.16.0.0/12 ro")},
		{"the floor lowered to /8", floor8, "rw=10.0.0.0/8", true, nil},
		{"off", nfsPl, "off", true, nil},
		// not ours: foreign and inherited values are other reports' business
		{"foreign", nfsPl, "rw=10.0.0.0/8,no_root_squash", true, nil},
		{"inherited", nfsPl, "rw=10.0.0.0/8", false, nil},
	}
	for _, tc := range cases {
		row := fsv("a", func(r *Row) { r.ShareNFS, r.ShareNFSLocal = tc.share, tc.local })
		plan, err := tc.pl.PlanReconcile(table(row), desired("a"))
		if err != nil {
			t.Fatal(err)
		}
		acts, skips := summary(plan)
		if !slices.Equal(skips, tc.skips) || len(acts) > 0 || len(plan.Plan()) > 0 {
			t.Errorf("%s: actions %q (plan %v), skipped %q; want no change and %q", tc.name, acts, plan.Plan(), skips, tc.skips)
		}
	}
}
