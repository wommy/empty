package endpoint

import (
	"context"
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

// digested is r with the plan_digest of a dry run of it: a run that
// collects an undesired volume needs one (ADR 0001 v2 D3).
func digested(ctx context.Context, t testing.TB, ep *Endpoint, r ReconcileRequest) ReconcileRequest {
	t.Helper()
	dry := r
	dry.DryRun = true
	p, err := ep.Reconcile(ctx, dry)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	r.PlanDigest = p.Digest
	return r
}

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

// stamp makes a snapshot row an internal clone-source snapshot the
// endpoint cut: no CSI name, managed and clone_source_for set at creation.
func stamp(r *Row) {
	_, name, _ := strings.Cut(r.ID, "@")
	r.Managed, r.SnapshotName = true, ""
	r.CloneSourceFor = strings.TrimPrefix(name, cloneSourceSnapshotPrefix)
}

// bare is a snapshot carrying only the reserved name: someone else's, or a
// legacy one democratic-csi cut without properties.
func bare(r *Row) { r.Managed, r.SnapshotName = false, "" }

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
		// ADR 0004: export state is sharenfs alone; a leftover stored
		// client list is reported on the volumes that stay, never acted on
		{
			name:  "leftover nfs_clients is reported",
			rows:  []Row{vol("a", func(r *Row) { r.NFSClientsSet = true }), vol("b", nil)},
			want:  desired("a", "b"),
			skips: []string{"a:" + SkipLegacyNFSClients},
		},
		{
			name: "leftover nfs_clients on a collected volume goes with it",
			rows: []Row{vol("a", nil), vol("b", func(r *Row) { r.NFSClientsSet = true })},
			want: desired("a"),
			acts: []string{"b:" + ReasonUndesired},
		},
		{
			name: "leftover nfs_clients on an unowned dataset is not ours to report",
			rows: []Row{vol("x", func(r *Row) { r.Managed, r.NFSClientsSet = false, true }), vol("a", nil)},
			want: desired("a"),
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
				snap("b@volume-source-for-volume-a", stamp),
			},
			want:  desired("a"),
			skips: []string{"b:volume b has dependent clone a"},
		},
		{
			name: "fixpoint: clone and its source collected together, source last",
			rows: []Row{
				vol("a", nil),
				vol("b", nil),
				snap("b@volume-source-for-volume-c", stamp),
				vol("c", func(r *Row) { r.Origin = "tank/v/b@volume-source-for-volume-c" }),
			},
			want: desired("a"),
			acts: []string{
				"b@volume-source-for-volume-c:" + ReasonReleasedWithVolume,
				"c:" + ReasonUndesired,
				"b:" + ReasonUndesired,
			},
		},
		{
			name: "fixpoint blocked by a young clone",
			rows: []Row{
				vol("a", nil),
				vol("b", nil),
				snap("b@volume-source-for-volume-c", stamp),
				vol("c", func(r *Row) { r.Origin = "tank/v/b@volume-source-for-volume-c"; young(r) }),
			},
			want:  desired("a"),
			skips: []string{"c:" + SkipTooYoung, "b:volume b has dependent clone c"},
		},
		{
			name: "orphan clone-source snapshot on a desired volume",
			rows: []Row{
				vol("a", nil),
				snap("a@volume-source-for-volume-gone", stamp),
			},
			want: desired("a"),
			acts: []string{"a@volume-source-for-volume-gone:" + ReasonOrphanCloneSource},
		},
		{
			name: "clone-source snapshot in use is kept",
			rows: []Row{
				vol("a", nil),
				snap("a@volume-source-for-volume-b", stamp),
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
				snap("x@volume-source-for-volume-gone", stamp),
			},
			want:  desired("a"),
			skips: []string{"a:" + SkipDesiredMissing, "x@volume-source-for-volume-gone:" + SkipUnmanagedSource},
		},
		{
			name: "young clone-source snapshot may be mid-clone",
			rows: []Row{
				vol("a", nil),
				snap("a@volume-source-for-volume-b", func(r *Row) { stamp(r); young(r) }),
			},
			want:  desired("a"),
			skips: []string{"a@volume-source-for-volume-b:" + SkipTooYoung},
		},
		{
			name: "orphan snapshot of a collected volume goes with it",
			rows: []Row{
				vol("a", nil),
				vol("b", nil),
				snap("b@volume-source-for-volume-gone", stamp),
			},
			want: desired("a"),
			acts: []string{"b@volume-source-for-volume-gone:" + ReasonWithVolume, "b:" + ReasonUndesired},
		},
		{
			name: "deferred CSI snapshot does not block",
			rows: []Row{vol("a", nil), vol("b", nil), snap("b@s", func(r *Row) { r.Deferred = true })},
			want: desired("a"),
			acts: []string{"b@s:" + ReasonWithVolume, "b:" + ReasonUndesired},
		},
		{
			// adv4 F2: another cluster's set, or one naming only the gone
			name:  "a desired set naming no existing volume counts as empty",
			rows:  []Row{vol("a", nil), vol("x", func(r *Row) { r.Managed = false })},
			want:  desired("gone", "x"),
			skips: []string{"gone:" + SkipDesiredMissing, "a:" + SkipEmptyDesired},
		},
		{
			// adv4 F6: anyone can `zfs snapshot` the reserved name
			name:  "unstamped clone-source-named snapshot on a desired volume is not ours",
			rows:  []Row{vol("a", nil), snap("a@volume-source-for-volume-manual", bare)},
			want:  desired("a"),
			skips: []string{"a@volume-source-for-volume-manual:" + SkipUnstampedCloneSource},
		},
		{
			name: "unstamped clone-source snapshot makes its volume's deletion refuse",
			rows: []Row{vol("a", nil), vol("b", nil), snap("b@volume-source-for-volume-gone", bare)},
			want: desired("a"),
			skips: []string{
				"b:volume b has snapshots the endpoint did not make: b@volume-source-for-volume-gone (delete them, or run with -destroy-foreign-snapshots)",
				"b@volume-source-for-volume-gone:" + SkipUnstampedCloneSource,
			},
		},
		{
			name:  "stamp for another clone is not ours",
			rows:  []Row{vol("a", nil), snap("a@volume-source-for-volume-x", func(r *Row) { stamp(r); r.CloneSourceFor = "y" })},
			want:  desired("a"),
			skips: []string{"a@volume-source-for-volume-x:" + SkipUnstampedCloneSource},
		},
		{
			// was TestPlanReconcileLocksEveryTarget: an orphan clone
			// source and a released one, whose locks the loop checks
			name: "orphan and released clone sources lock their volumes and clones",
			rows: []Row{
				vol("a", nil),
				snap("a@volume-source-for-volume-gone", stamp),
				vol("b", nil),
				snap("b@volume-source-for-volume-c", stamp),
				vol("c", func(r *Row) { r.Origin = "tank/v/b@volume-source-for-volume-c" }),
			},
			want: desired("a"),
			acts: []string{
				"b@volume-source-for-volume-c:" + ReasonReleasedWithVolume,
				"c:" + ReasonUndesired,
				"b:" + ReasonUndesired,
				"a@volume-source-for-volume-gone:" + ReasonOrphanCloneSource,
			},
		},
		{
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
			name:  "unaddressable clone-source snapshot is reported, not planned",
			rows:  []Row{vol("a", nil), snap("a@volume-source-for-volume-x y", stamp)},
			want:  desired("a"),
			skips: []string{"a@volume-source-for-volume-x y:" + SkipUnaddressable},
		},
		{
			name:  "a desired id naming a snapshot names no volume",
			rows:  []Row{vol("a", nil), snap("a@s", nil)},
			want:  desired("a", "a@s"),
			skips: []string{"a@s:" + SkipDesiredMissing},
		},
		{
			// committed_at counts only for a committed volume
			name: "an uncommitted row ages from its creation",
			rows: []Row{vol("a", nil), vol("b", func(r *Row) { r.Committed, r.CommittedAt = false, recNow-60 })},
			want: desired("a"),
			acts: []string{"b:" + ReasonUncommitted},
		},
		{
			// "less than MinAge before Now is untouched": the boundary
			name: "exactly MinAge old is collected",
			rows: []Row{vol("a", nil), vol("b", func(r *Row) { r.Creation = recNow - 3600 })},
			want: desired("a"),
			acts: []string{"b:" + ReasonUndesired},
		},
		{
			name:  "one second under MinAge is not",
			rows:  []Row{vol("a", nil), vol("b", func(r *Row) { r.Creation = recNow - 3599 })},
			want:  desired("a"),
			skips: []string{"b:" + SkipTooYoung},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
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
				if op.Recursive || op.Defer && !strings.Contains(op.Target, "@") {
					t.Fatalf("op %+v: never -r; only snapshots are destroyed deferred", op)
				}
			}
			// every action locks its volume, and a released or orphan clone
			// source also locks the clone it serves, there or not (REC-10).
			// A clone-source snapshot destroyed with its collected volume
			// (ReasonWithVolume) relies on the volume's lock alone:
			// CreateVolume from that volume takes vol:<source> too.
			for _, a := range plan.Actions {
				vol, _, _ := strings.Cut(a.Target, "@")
				if !slices.Contains(a.Volumes, vol) {
					t.Errorf("%s: locks %v miss its volume", a.Target, a.Volumes)
				}
				_, clone, ok := strings.Cut(a.Target, "@"+cloneSourceSnapshotPrefix)
				serves := a.Reason == ReasonReleasedWithVolume || a.Reason == ReasonOrphanCloneSource
				if ok && serves && !slices.Contains(a.Volumes, clone) {
					t.Errorf("%s: locks %v miss the clone it serves", a.Target, a.Volumes)
				}
			}
		})
	}
}

// Under Reconcile's locks, an action outside them is skipped, and so is
// whatever depends on it: here b cannot go while its clone c stays.
func TestPlanReconcileHeldLimitsActions(t *testing.T) {
	tab := table(
		vol("a", nil),
		vol("b", nil),
		snap("b@volume-source-for-volume-c", stamp),
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
	// the locked re-plan keeps an action only for the reason the unlocked
	// plan had: an uncommitted row committed in between is judged afresh
	d = desired("a")
	d.planned = map[string]plannedAction{"b": {ReasonUncommitted, ""}}
	if plan, err = pl.PlanReconcile(table(vol("a", nil), vol("b", nil)), d); err != nil {
		t.Fatal(err)
	}
	if acts, skips = summary(plan); len(acts) != 0 || !slices.Equal(skips, []string{"b:" + SkipChangedSinceScan}) {
		t.Fatalf("reason changed: actions %q skipped %q", acts, skips)
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

// The fixpoint's simulation, case by case: what a round of destroys leaves
// is what the next round plans against, so reaping too much would let a
// volume whose snapshot another clone still pins be judged deletable.
func TestSimulateDestroyAllCases(t *testing.T) {
	t.Parallel()
	snapRow := func(name string, deferred bool) Row {
		return Row{Name: name, ID: strings.TrimPrefix(name, "tank/v/"), Type: zfs.TypeSnapshot, Deferred: deferred}
	}
	dsRow := func(name, origin string) Row {
		return Row{Name: name, ID: strings.TrimPrefix(name, "tank/v/"), Origin: origin}
	}
	for _, tc := range []struct {
		name     string
		rows     []Row
		plans    []Plan
		want     string // remaining ids
		deferred string // remaining ids marked deferred
	}{
		{
			name: "only the destroyed clone's deferred origin is reaped",
			rows: []Row{
				dsRow("tank/v/b", ""), snapRow("tank/v/b@s1", true), snapRow("tank/v/b@s2", true),
				dsRow("tank/v/c", "tank/v/b@s1"), dsRow("tank/v/e", "tank/v/b@s2"),
			},
			plans:    []Plan{{{Kind: OpDestroy, Target: "tank/v/c"}}},
			want:     "b,b@s2,e",
			deferred: "b@s2",
		},
		{
			name:  "an undeferred origin outlives its last clone",
			rows:  []Row{dsRow("tank/v/b", ""), snapRow("tank/v/b@s", false), dsRow("tank/v/c", "tank/v/b@s")},
			plans: []Plan{{{Kind: OpDestroy, Target: "tank/v/c"}}},
			want:  "b,b@s",
		},
		{
			name:  "a plain destroy takes the dataset alone",
			rows:  []Row{dsRow("tank/v/b", ""), snapRow("tank/v/b@s", false)},
			plans: []Plan{{{Kind: OpDestroy, Target: "tank/v/b"}}},
			want:  "b@s",
		},
		{
			name:  "-r takes the dataset, its snapshots and children, not a name sharing its prefix",
			rows:  []Row{dsRow("tank/v/b", ""), snapRow("tank/v/b@s", false), dsRow("tank/v/b/x", ""), dsRow("tank/v/bb", "")},
			plans: []Plan{{{Kind: OpDestroy, Target: "tank/v/b", Recursive: true}}},
			want:  "bb",
		},
		{
			name:     "a deferred destroy of a used first row marks it",
			rows:     []Row{snapRow("tank/v/a@s", false), dsRow("tank/v/c", "tank/v/a@s")},
			plans:    []Plan{{{Kind: OpDestroy, Target: "tank/v/a@s", Defer: true}}},
			want:     "a@s,c",
			deferred: "a@s",
		},
		{
			name:  "a deferred destroy of an unused snapshot removes it",
			rows:  []Row{dsRow("tank/v/a", ""), snapRow("tank/v/a@s", false)},
			plans: []Plan{{{Kind: OpDestroy, Target: "tank/v/a@s", Defer: true}}},
			want:  "a",
		},
		{
			name: "release then clone in one round reaps the origin",
			rows: []Row{dsRow("tank/v/b", ""), snapRow("tank/v/b@o", false), dsRow("tank/v/c", "tank/v/b@o")},
			plans: []Plan{{
				{Kind: OpDestroy, Target: "tank/v/b@o", Defer: true},
				{Kind: OpDestroy, Target: "tank/v/c"},
			}},
			want: "b",
		},
	} {
		rows := simulateDestroyAll(slices.Clone(tc.rows), tc.plans)
		var ids, deferred []string
		for _, r := range rows {
			ids = append(ids, r.ID)
			if r.Deferred {
				deferred = append(deferred, r.ID)
			}
		}
		if got := strings.Join(ids, ","); got != tc.want {
			t.Errorf("%s: rows %s, want %s", tc.name, got, tc.want)
		}
		if got := strings.Join(deferred, ","); got != tc.deferred {
			t.Errorf("%s: deferred %s, want %s", tc.name, got, tc.deferred)
		}
	}
}
