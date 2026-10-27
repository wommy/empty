package endpoint

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

// FuzzReconcileSafe builds an arbitrary parent on the simulator from the
// fuzz bytes, two per volume (a third picks a clone's source):
//
//	b: owned or not, committed, desired, young, a CSI snapshot, cloned from
//	   an earlier volume, zvol, an orphan clone-source snapshot (stamped)
//	x: a foreign snapshot (sanoid), a child dataset someone made below it,
//	   a clone of its snapshot outside the parent, an origin outside the
//	   parent, a deferred CSI snapshot kept by a clone outside, an unstamped
//	   (legacy or hand-cut) clone source, a busy destroy on the first run,
//	   a just-committed retry (committed_at now)
//
// Unowned committed volumes are received copies (markers from zfs recv).
// Then it reconciles three times (the first with injected failures) and
// checks:
//
//   - no run errors; what runs is a subset of the dry run
//   - no op targets a desired, unowned, young or CSI-snapshotted volume, a
//     CSI snapshot, or a snapshot the endpoint did not cut, except a legacy
//     clone source released with the clone its origin proves it serves
//   - afterwards every row the endpoint does not own still exists, and so
//     does everything outside the scan (children, datasets elsewhere)
//   - once failures clear, the next run converges and the one after it
//     does nothing: it applies nothing and no group in it fails, except a
//     volume zfs refuses for a reason the scan cannot see (an outside clone
//     of a deferred snapshot); with no such volume a dry
//     run then plans nothing. A group failing on every run shows only in
//     Skipped, so counting applied actions alone would miss it.
func FuzzReconcileSafe(f *testing.F) {
	f.Add([]byte{0x07, 0x00, 0x03, 0x00, 0x23, 0x00, 0x00, 0x83, 0x00})
	f.Add([]byte{0x87, 0x00}) // a desired volume with a stamped orphan clone source: the only seed that plans ReasonOrphanCloneSource
	f.Add([]byte{0x01, 0x00, 0x00, 0x21, 0x00, 0x00, 0x21, 0x00, 0x01})
	f.Add([]byte{0x13, 0x00, 0x00, 0x2b, 0x20, 0x00, 0x09, 0x00})
	f.Add([]byte{0x00, 0x00, 0x00, 0x05, 0x00})
	f.Add([]byte{0xff, 0xff, 0xff, 0x00, 0x23, 0x01, 0x23, 0x02})
	f.Add([]byte{0x03, 0x01, 0x03, 0x02, 0x03, 0x04, 0x03, 0x08, 0x03, 0x10, 0x03, 0x40, 0x07, 0x00})
	f.Add([]byte{0x03, 0x00, 0x23, 0x20, 0x00, 0x03, 0x40, 0x07, 0x80, 0x03, 0x00})
	f.Fuzz(func(t *testing.T, spec []byte) {
		const (
			managed   = 1 << iota // owned: markers set locally
			committed             // provision_success set
			desiredB              // in the desired set
			youngB                // created inside the minimum age
			csiSnap               // has a CSI snapshot
			cloned                // cloned from an earlier volume (next byte picks it)
			block                 // zvol
			leak                  // a stamped clone-source snapshot whose clone is gone
		)
		const (
			foreignSnap   = 1 << iota // a snapshot someone else cut
			child                     // a dataset below a filesystem volume
			outsideClone              // a clone of its foreign snapshot outside the parent
			originOutside             // the volume is a clone of a snapshot outside the parent
			deferredSnap              // a deleted CSI snapshot kept by a clone outside
			unstamped                 // cloned: its source snapshot carries no stamp
			failDestroy               // its destroy is busy on the first run
			recommitted               // committed just now by a retry (committed_at)
		)
		const (
			parent = "tank/v"
			old    = int64(1_000_000)
			now    = old + 7200
		)
		ctx := context.Background()
		sim := zfstest.New("tank")
		clock := old
		sim.Clock = func() int64 { return clock }
		z := zfs.New(sim)
		for _, d := range []string{parent, "tank/other"} {
			if err := z.CreateFilesystem(ctx, d, nil, false); err != nil {
				t.Fatal(err)
			}
		}
		names := props{DefaultPropertyPrefix}
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		var ids, want, outside []string
		busy := map[string]bool{}
		hidden := map[string]bool{} // volumes zfs refuses to destroy for a reason the scan cannot see
		zvol := map[string]bool{}   // a clone is a zvol when its source is
		for i := 0; i < len(spec) && len(ids) < 8; i++ {
			b := spec[i]
			var x byte
			if i+1 < len(spec) {
				i++
				x = spec[i]
			}
			id := fmt.Sprintf("v%d", len(ids))
			ds := parent + "/" + id
			clock = old
			if b&youngB != 0 {
				clock = now - 60
			}
			own := map[string]string{}
			if b&managed != 0 {
				own[names.managed()], own[names.volumeName()] = "true", id
			}
			switch {
			case b&cloned != 0 && len(ids) > 0 && i+1 < len(spec):
				i++
				src := ids[int(spec[i])%len(ids)]
				snap := parent + "/" + src + "@" + cloneSourceSnapshotPrefix + id
				stamp := map[string]string{names.managed(): "true", names.cloneSourceFor(): id}
				if x&unstamped != 0 {
					stamp = nil
				}
				must(z.Snapshot(ctx, snap, stamp))
				must(z.Clone(ctx, snap, ds, own))
				zvol[id] = zvol[src]
			case x&originOutside != 0:
				golden := "tank/other/g" + id
				must(z.CreateFilesystem(ctx, golden, nil, false))
				snap := golden + "@" + cloneSourceSnapshotPrefix + id
				must(z.Snapshot(ctx, snap, nil))
				must(z.Clone(ctx, snap, ds, own))
				outside = append(outside, golden, snap)
			case b&block != 0:
				must(z.CreateVolume(ctx, ds, 1<<20, true, own))
				zvol[id] = true
			default:
				must(z.CreateFilesystem(ctx, ds, own, false))
			}
			if b&managed != 0 && b&committed != 0 {
				set := map[string]string{names.success(): "true"}
				if x&recommitted != 0 {
					set[names.committedAt()] = fmt.Sprint(now - 60)
				}
				must(z.Set(ctx, ds, set))
			}
			if b&managed == 0 && b&committed != 0 {
				// a received copy (zfs recv): every marker, none of them local
				for k, v := range map[string]string{names.managed(): "true", names.success(): "true", names.volumeName(): id} {
					sim.Receive(ds, k, v)
				}
			}
			if b&csiSnap != 0 {
				must(z.Snapshot(ctx, ds+"@csi", map[string]string{
					names.managed(): "true", names.snapshotName(): "csi-" + id, names.snapshotSource(): id,
				}))
			}
			if x&deferredSnap != 0 {
				must(z.Snapshot(ctx, ds+"@gone", map[string]string{
					names.managed(): "true", names.snapshotName(): "gone-" + id, names.snapshotSource(): id,
				}))
				must(z.Clone(ctx, ds+"@gone", "tank/other/d"+id, nil))
				must(z.Destroy(ctx, ds+"@gone", zfs.DestroyOptions{Defer: true}))
				outside = append(outside, "tank/other/d"+id)
				hidden[id] = true
			}
			if b&leak != 0 {
				must(z.Snapshot(ctx, ds+"@"+cloneSourceSnapshotPrefix+"gone"+id, map[string]string{
					names.managed(): "true", names.cloneSourceFor(): "gone" + id,
				}))
			}
			if x&foreignSnap != 0 {
				must(z.Snapshot(ctx, ds+"@autosnap", nil))
				if x&outsideClone != 0 {
					must(z.Clone(ctx, ds+"@autosnap", "tank/other/c"+id, nil))
					outside = append(outside, "tank/other/c"+id)
				}
			}
			if x&child != 0 && !zvol[id] { // zfs creates nothing below a zvol
				must(z.CreateFilesystem(ctx, ds+"/kid", nil, false))
				must(z.Snapshot(ctx, ds+"/kid@s", nil))
				// not hidden: the scan sees the child and refuses the volume
				// at plan time (OWN-12), so no group fails on it
				outside = append(outside, ds+"/kid", ds+"/kid@s")
			}
			if x&failDestroy != 0 {
				busy[ds] = true
			}
			ids = append(ids, id)
			if b&desiredB != 0 {
				want = append(want, id)
			}
		}

		ep, err := New(z, Config{Parent: parent, Now: func() time.Time { return time.Unix(now, 0) }})
		must(err)
		before, err := ep.scan(ctx, "")
		must(err)
		isWanted := map[string]bool{}
		stated := false
		for _, id := range want {
			isWanted[id] = true
			if r, ok := before.Get(id); ok && r.OwnedVolume() {
				stated = true
			}
		}
		req := ReconcileRequest{DesiredVolumeIDs: want, MinAge: time.Hour, UnlimitedDeletions: true}
		dryReq := req
		dryReq.DryRun = true

		dry, err := ep.Reconcile(ctx, dryReq)
		must(err)
		sim.Fail = func(args []string) error {
			if args[0] == "destroy" && busy[args[len(args)-1]] {
				return &zfs.ExitError{Code: 1, Stderr: fmt.Sprintf("cannot destroy '%s': dataset is busy\n", args[len(args)-1])}
			}
			return nil
		}
		req.PlanDigest = dry.Digest
		first, err := ep.Reconcile(ctx, req)
		sim.Fail = nil
		if err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		planned := map[string]string{}
		for _, a := range dry.Actions {
			planned[a.Target] = a.Reason
		}
		for _, a := range first.Actions {
			if planned[a.Target] != a.Reason {
				t.Fatalf("%s (%s) ran but was not in the dry run %+v", a.Target, a.Reason, dry.Actions)
			}
		}
		second, err := ep.Reconcile(ctx, digested(ctx, t, ep, req))
		must(err)
		third, err := ep.Reconcile(ctx, digested(ctx, t, ep, req))
		if err != nil || len(third.Actions) != 0 {
			t.Fatalf("third reconcile: %+v %v", third.Actions, err)
		}
		for _, s := range third.Skipped {
			failed := strings.HasPrefix(s.Reason, SkipFailed)
			if failed && !hidden[s.Target] || s.Reason == SkipDependentFailed && len(hidden) == 0 {
				t.Fatalf("third reconcile still failing %s, which no outside clone explains: %s", s.Target, s.Reason)
			}
		}
		// with nothing hidden from the scan, a converged parent plans nothing; a
		// hidden outside clone keeps its group (and the groups
		// depending on it) failing by design, so only the failures are bounded
		again, err := ep.Reconcile(ctx, dryReq)
		must(err)
		if len(hidden) == 0 && len(again.Actions) != 0 {
			t.Fatalf("dry run after convergence still plans %+v", again.Actions)
		}

		// origins an undesired clone of ours proves were cut for it: under
		// the parent, from a volume of ours, named for that clone
		released := map[string]bool{}
		for r := range before.Volumes() {
			src, name, ok := strings.Cut(strings.TrimPrefix(r.Origin, parent+"/"), "@")
			if s, sok := before.Get(src); ok && sok && s.OwnedVolume() && r.OwnedVolume() && !isWanted[r.ID] &&
				strings.HasPrefix(r.Origin, parent+"/") && name == cloneSourceSnapshotPrefix+r.ID {
				released[r.Origin] = true
			}
		}
		for _, run := range []ReconcilePlan{first, second} {
			for _, a := range run.Actions {
				row, ok := before.Get(a.Target)
				if !ok {
					t.Fatalf("%s: targets a row that did not exist", a.Target)
				}
				if a.Op.Kind != OpDestroy || !a.Op.OkMissing || a.Op.Recursive {
					t.Fatalf("%s: op %+v", a.Target, a.Op)
				}
				if row.Type == zfs.TypeSnapshot {
					_, stampedOurs := row.OwnedCloneSource()
					switch {
					case row.OwnedSnapshot() && !row.Deferred:
						t.Fatalf("%s: a CSI snapshot is targeted", a.Target)
					case stampedOurs, row.Deferred:
					case a.Reason == ReasonReleasedWithVolume && released[row.Name]:
						// legacy: the collected clone's origin proves it
					default:
						t.Fatalf("%s (%s): a snapshot the endpoint did not cut is targeted", a.Target, a.Reason)
					}
					continue
				}
				born := row.Creation
				if row.Committed && row.CommittedAt > born {
					born = row.CommittedAt
				}
				switch {
				case !row.OwnedVolume():
					t.Fatalf("%s: unowned volume targeted", a.Target)
				case isWanted[a.Target]:
					t.Fatalf("%s: desired volume targeted", a.Target)
				case time.Unix(now, 0).Sub(time.Unix(born, 0)) < time.Hour:
					t.Fatalf("%s: young volume targeted", a.Target)
				case !stated && row.Committed:
					t.Fatalf("%s: committed volume collected without a desired set naming ours", a.Target)
				case csiSnapped(before, a.Target):
					t.Fatalf("%s: volume with a CSI snapshot targeted", a.Target)
				}
			}
		}

		after, err := ep.scan(ctx, "")
		must(err)
		gone := func(id string) bool { _, ok := after.Get(id); return !ok }
		for r := range before.All() {
			if !gone(r.ID) {
				continue
			}
			vol, _, _ := strings.Cut(r.ID, "@")
			if r.Type != zfs.TypeSnapshot {
				if !r.OwnedVolume() || isWanted[vol] || csiSnapped(before, r.ID) {
					t.Fatalf("volume %s was destroyed (runs %+v / %+v)", r.ID, first, second)
				}
				continue
			}
			if _, ours := r.OwnedCloneSource(); ours || r.Deferred {
				continue
			}
			if r.OwnedSnapshot() {
				t.Fatalf("CSI snapshot %s was destroyed", r.ID)
			}
			// a legacy clone source goes only with every clone it served
			clones := slices.Collect(before.ClonesOf(r.Name))
			if len(clones) == 0 || slices.ContainsFunc(clones, func(c Row) bool { return !gone(c.ID) }) {
				t.Fatalf("snapshot %s the endpoint did not cut was destroyed (runs %+v / %+v)", r.ID, first, second)
			}
		}
		for _, name := range outside {
			if !sim.Has(name) {
				t.Fatalf("%s, outside the scan, was destroyed (runs %+v / %+v)", name, first, second)
			}
		}
	})
}

func csiSnapped(t Table, id string) bool {
	for s := range t.SnapshotsOf(id) {
		if s.OwnedSnapshot() && !s.Deferred {
			return true
		}
	}
	return false
}
