package endpoint

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/wommy/empty/internal/zfs"
)

// Reconcile planner tests: pure, Table in, ReconcilePlan out.

const (
	oldT   = 1_000_000
	recNow = oldT + 10*3600 // ten hours after oldT
)

func desired(ids ...string) Desired {
	return Desired{VolumeIDs: ids, MinAge: time.Hour, Now: time.Unix(recNow, 0)}
}

func aged(r Row) Row {
	if r.Creation == 0 {
		r.Creation = oldT
	}
	return r
}

func table(rows ...Row) Table {
	for i := range rows {
		rows[i] = aged(rows[i])
	}
	return NewTable(rows)
}

// summary renders a plan as "target:reason" lines for exact comparison.
func summary(p ReconcilePlan) (acts, skips []string) {
	for _, a := range p.Actions {
		acts = append(acts, a.Target+":"+a.Reason)
	}
	for _, s := range p.Skipped {
		skips = append(skips, s.Target+":"+s.Reason)
	}
	return
}

func TestPlanReconcile(t *testing.T) {
	young := func(r *Row) { r.Creation = recNow - 60 }
	uncommitted := func(r *Row) { r.Committed = false }
	cases := []struct {
		name  string
		rows  []Row
		want  Desired
		acts  []string
		skips []string
	}{
		{
			name: "desired committed volume is kept",
			rows: []Row{vol("a", nil)},
			want: desired("a"),
		},
		{
			name: "undesired committed volume is collected",
			rows: []Row{vol("a", nil), vol("b", nil)},
			want: desired("a"),
			acts: []string{"b:" + ReasonUndesired},
		},
		{
			name:  "empty desired set collects no committed volume",
			rows:  []Row{vol("a", nil)},
			want:  desired(),
			skips: []string{"a:" + SkipEmptyDesired},
		},
		{
			name: "old uncommitted row is collected even with an empty desired set",
			rows: []Row{vol("a", uncommitted)},
			want: desired(),
			acts: []string{"a:" + ReasonUncommitted},
		},
		{
			name:  "young uncommitted row may be in flight",
			rows:  []Row{vol("a", func(r *Row) { uncommitted(r); young(r) })},
			want:  desired(),
			skips: []string{"a:" + SkipTooYoung},
		},
		{
			name:  "young undesired volume may postdate the desired set",
			rows:  []Row{vol("a", nil), vol("b", young)},
			want:  desired("a"),
			skips: []string{"b:" + SkipTooYoung},
		},
		{
			name: "desired uncommitted row is left for the caller's retry",
			rows: []Row{vol("a", uncommitted)},
			want: desired("a"),
		},
		{
			name:  "unknown creation is never old",
			rows:  []Row{vol("a", func(r *Row) { r.Committed, r.Creation = false, -1 })},
			want:  desired(),
			skips: []string{"a:" + SkipNoCreation},
		},
		{
			name: "unowned datasets are invisible",
			rows: []Row{vol("x", func(r *Row) { r.Managed, r.Committed = false, false }), vol("a", nil)},
			want: desired("a"),
		},
		{
			name:  "snapshotted volume is skipped with the reason",
			rows:  []Row{vol("a", nil), vol("b", nil), snap("b@s", nil)},
			want:  desired("a"),
			skips: []string{"b:volume b has snapshot b@s"},
		},
		{
			name: "volume cloned by a desired volume is skipped",
			rows: []Row{
				vol("a", func(r *Row) { r.Origin = "tank/v/b@volume-source-for-volume-a" }),
				vol("b", nil),
				snap("b@volume-source-for-volume-a", func(r *Row) { r.Managed, r.SnapshotName = false, "" }),
			},
			want:  desired("a"),
			skips: []string{"b:volume b has dependent clone a"},
		},
		{
			name: "fixpoint: clone and its source collected together, clone first",
			rows: []Row{
				vol("a", nil),
				vol("b", nil),
				snap("b@volume-source-for-volume-c", func(r *Row) { r.Managed, r.SnapshotName = false, "" }),
				vol("c", func(r *Row) { r.Origin = "tank/v/b@volume-source-for-volume-c" }),
			},
			want: desired("a"),
			acts: []string{
				"c:" + ReasonUndesired,
				"b@volume-source-for-volume-c:" + ReasonReleasedWithVolume,
				"b:" + ReasonUndesired,
			},
		},
		{
			name: "fixpoint blocked by a young clone",
			rows: []Row{
				vol("a", nil),
				vol("b", nil),
				snap("b@volume-source-for-volume-c", func(r *Row) { r.Managed, r.SnapshotName = false, "" }),
				vol("c", func(r *Row) { r.Origin = "tank/v/b@volume-source-for-volume-c"; young(r) }),
			},
			want:  desired("a"),
			skips: []string{"c:" + SkipTooYoung, "b:volume b has dependent clone c"},
		},
		{
			name: "orphan clone-source snapshot on a desired volume",
			rows: []Row{
				vol("a", nil),
				snap("a@volume-source-for-volume-gone", func(r *Row) { r.Managed, r.SnapshotName = false, "" }),
			},
			want: desired("a"),
			acts: []string{"a@volume-source-for-volume-gone:" + ReasonOrphanCloneSource},
		},
		{
			name: "clone-source snapshot in use is kept",
			rows: []Row{
				vol("a", nil),
				snap("a@volume-source-for-volume-b", func(r *Row) { r.Managed, r.SnapshotName = false, "" }),
				vol("b", func(r *Row) { r.Origin = "tank/v/a@volume-source-for-volume-b" }),
			},
			want: desired("a", "b"),
		},
		{
			name: "clone-source-named CSI snapshot is never collected",
			rows: []Row{
				vol("a", nil),
				snap("a@volume-source-for-volume-x", func(r *Row) { r.SnapshotName = "legacy" }),
			},
			want: desired("a"),
		},
		{
			name: "clone-source snapshot on an unowned volume is reported only",
			rows: []Row{
				vol("x", func(r *Row) { r.Managed = false }),
				snap("x@volume-source-for-volume-gone", func(r *Row) { r.Managed, r.SnapshotName = false, "" }),
			},
			want:  desired("a"),
			skips: []string{"a:" + SkipDesiredMissing, "x@volume-source-for-volume-gone:" + SkipUnmanagedSource},
		},
		{
			name: "young clone-source snapshot may be mid-clone",
			rows: []Row{
				vol("a", nil),
				snap("a@volume-source-for-volume-b", func(r *Row) { r.Managed, r.SnapshotName = false, ""; young(r) }),
			},
			want:  desired("a"),
			skips: []string{"a@volume-source-for-volume-b:" + SkipTooYoung},
		},
		{
			name: "orphan snapshot of a collected volume goes with it",
			rows: []Row{
				vol("a", nil),
				vol("b", nil),
				snap("b@volume-source-for-volume-gone", func(r *Row) { r.Managed, r.SnapshotName = false, "" }),
			},
			want: desired("a"),
			acts: []string{"b:" + ReasonUndesired},
		},
		{
			name: "deferred CSI snapshot does not block",
			rows: []Row{vol("a", nil), vol("b", nil), snap("b@s", func(r *Row) { r.Deferred = true })},
			want: desired("a"),
			acts: []string{"b:" + ReasonUndesired},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tab := table(c.rows...)
			plan, err := pl.PlanReconcile(tab, c.want)
			if err != nil {
				t.Fatal(err)
			}
			acts, skips := summary(plan)
			if !slices.Equal(acts, c.acts) || !slices.Equal(skips, c.skips) {
				t.Fatalf("actions %q\nskipped %q\nwant    %q\n        %q", acts, skips, c.acts, c.skips)
			}
			for _, op := range plan.Plan() {
				if op.Kind != OpDestroy || !op.OkMissing {
					t.Fatalf("op %+v: reconcile only destroys, replay-tolerantly", op)
				}
				if strings.Contains(op.Target, "@") != op.Defer {
					t.Fatalf("op %+v: snapshots are destroyed deferred, volumes not", op)
				}
			}
		})
	}
}

func TestPlanReconcileLocksEveryTarget(t *testing.T) {
	tab := table(
		vol("a", nil),
		snap("a@volume-source-for-volume-gone", func(r *Row) { r.Managed, r.SnapshotName = false, "" }),
		vol("b", nil),
		snap("b@volume-source-for-volume-c", func(r *Row) { r.Managed, r.SnapshotName = false, "" }),
		vol("c", func(r *Row) { r.Origin = "tank/v/b@volume-source-for-volume-c" }),
	)
	plan, err := pl.PlanReconcile(tab, desired("a"))
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range plan.Actions {
		vol, _, _ := strings.Cut(a.Target, "@")
		if !slices.Contains(a.Volumes, vol) {
			t.Errorf("%s: locks %v miss its volume", a.Target, a.Volumes)
		}
		if _, clone, ok := strings.Cut(a.Target, "@"+cloneSourceSnapshotPrefix); ok && !slices.Contains(a.Volumes, clone) {
			t.Errorf("%s: locks %v miss the clone it serves", a.Target, a.Volumes)
		}
	}
}

// Under Reconcile's locks, an action outside them is skipped, and so is
// whatever depends on it: here b cannot go while its clone c stays.
func TestPlanReconcileHeldLimitsActions(t *testing.T) {
	tab := table(
		vol("a", nil),
		vol("b", nil),
		snap("b@volume-source-for-volume-c", func(r *Row) { r.Managed, r.SnapshotName = false, "" }),
		vol("c", func(r *Row) { r.Origin = "tank/v/b@volume-source-for-volume-c" }),
	)
	d := desired("a")
	d.held = map[string]bool{"b": true}
	plan, err := pl.PlanReconcile(tab, d)
	if err != nil {
		t.Fatal(err)
	}
	acts, skips := summary(plan)
	if len(acts) != 0 || !slices.Equal(skips, []string{"c:" + SkipChangedSinceScan, "b:volume b has dependent clone c"}) {
		t.Fatalf("actions %q skipped %q", acts, skips)
	}
}

func TestSimulateDestroyReapsDeferredOrigins(t *testing.T) {
	rows := []Row{
		{Name: "tank/v/b", ID: "b"},
		{Name: "tank/v/b@s", ID: "b@s", Type: zfs.TypeSnapshot},
		{Name: "tank/v/c", ID: "c", Origin: "tank/v/b@s"},
		{Name: "tank/v/d", ID: "d", Origin: "tank/v/b@s"},
	}
	rows = simulateDestroyAll(rows, []Plan{{{Kind: OpDestroy, Target: "tank/v/b@s", Defer: true}}})
	if len(rows) != 4 || !rows[1].Deferred {
		t.Fatalf("deferred with clones: %+v", rows)
	}
	rows = simulateDestroyAll(rows, []Plan{{{Kind: OpDestroy, Target: "tank/v/c"}}})
	rows = simulateDestroyAll(rows, []Plan{{{Kind: OpDestroy, Target: "tank/v/d"}}})
	if len(rows) != 1 || rows[0].ID != "b" {
		t.Fatalf("last clone gone: %+v", rows)
	}
}
