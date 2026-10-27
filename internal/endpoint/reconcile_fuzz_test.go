package endpoint

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

// FuzzReconcileSafe builds an arbitrary parent on the simulator (owned and
// unowned volumes, committed or not, old or young, CSI snapshots, clones of
// clones, leaked clone-source snapshots) from the fuzz bytes, then checks
// reconcile's safety invariant on the plan and convergence on the result:
//
//   - no op targets a desired, unowned or CSI-snapshotted volume, a CSI
//     snapshot, or anything younger than the minimum age
//   - after applying, every desired, unowned or snapshotted volume and
//     every CSI snapshot still exists
//   - reconciling again plans nothing
func FuzzReconcileSafe(f *testing.F) {
	f.Add([]byte{0x07, 0x03, 0x23, 0x00, 0x83})
	f.Add([]byte{0x01, 0x00, 0x21, 0x00, 0x21, 0x01})
	f.Add([]byte{0x13, 0x00, 0x2b, 0x00, 0x09})
	f.Add([]byte{0x00, 0x00, 0x05, 0x00})
	f.Add([]byte{0xff, 0xff, 0xff, 0x00, 0x23, 0x01, 0x23, 0x02})
	f.Fuzz(func(t *testing.T, spec []byte) {
		const (
			managed   = 1 << iota // owned: managed_resource set locally
			committed             // provision_success set
			desiredB              // in the desired set
			youngB                // created inside the minimum age
			csiSnap               // has a CSI snapshot
			cloned                // cloned from an earlier volume (next byte picks it)
			block                 // zvol
			leak                  // carries a clone-source snapshot whose clone is gone
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
		if err := z.CreateFilesystem(ctx, parent, nil, false); err != nil {
			t.Fatal(err)
		}
		names := props{"democratic-csi"}
		var ids, want []string
		for i := 0; i < len(spec) && len(ids) < 8; i++ {
			b := spec[i]
			id := fmt.Sprintf("v%d", len(ids))
			clock = old
			if b&youngB != 0 {
				clock = now - 60
			}
			own := map[string]string{}
			if b&managed != 0 {
				own[names.managed()], own[names.volumeName()] = "true", id
			}
			var err error
			switch {
			case b&cloned != 0 && len(ids) > 0 && i+1 < len(spec):
				i++
				src := ids[int(spec[i])%len(ids)]
				snap := parent + "/" + src + "@" + cloneSourceSnapshotPrefix + id
				if err = z.Snapshot(ctx, snap, nil); err == nil {
					err = z.Clone(ctx, snap, parent+"/"+id, own)
				}
			case b&block != 0:
				err = z.CreateVolume(ctx, parent+"/"+id, 1<<20, true, own)
			default:
				err = z.CreateFilesystem(ctx, parent+"/"+id, own, false)
			}
			if err != nil {
				t.Fatal(err)
			}
			if b&managed != 0 && b&committed != 0 {
				if err := z.Set(ctx, parent+"/"+id, map[string]string{names.success(): "true"}); err != nil {
					t.Fatal(err)
				}
			}
			if b&csiSnap != 0 {
				if err := z.Snapshot(ctx, parent+"/"+id+"@csi", map[string]string{
					names.managed(): "true", names.snapshotName(): "csi-" + id, names.snapshotSource(): id,
				}); err != nil {
					t.Fatal(err)
				}
			}
			if b&leak != 0 {
				if err := z.Snapshot(ctx, parent+"/"+id+"@"+cloneSourceSnapshotPrefix+"gone"+id, nil); err != nil {
					t.Fatal(err)
				}
			}
			ids = append(ids, id)
			if b&desiredB != 0 {
				want = append(want, id)
			}
		}

		ep, err := New(z, Config{Parent: parent, Now: func() time.Time { return time.Unix(now, 0) }})
		if err != nil {
			t.Fatal(err)
		}
		before, err := ep.scan(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		isWanted := map[string]bool{}
		for _, id := range want {
			isWanted[id] = true
		}
		req := ReconcileRequest{DesiredVolumeIDs: want, MinAge: time.Hour}

		dry, err := ep.Reconcile(ctx, ReconcileRequest{DesiredVolumeIDs: want, MinAge: time.Hour, DryRun: true})
		if err != nil {
			t.Fatal(err)
		}
		done, err := ep.Reconcile(ctx, req)
		if err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		if fmt.Sprint(dry) != fmt.Sprint(done) {
			t.Fatalf("dry run %+v differs from the run %+v", dry, done)
		}
		for _, a := range done.Actions {
			row, ok := before.Get(a.Target)
			if !ok {
				t.Fatalf("%s: targets a row that did not exist", a.Target)
			}
			if a.Op.Kind != OpDestroy || !a.Op.OkMissing {
				t.Fatalf("%s: op %+v", a.Target, a.Op)
			}
			if row.Type == zfs.TypeSnapshot {
				if row.Managed || row.SnapshotName != "" {
					t.Fatalf("%s: a CSI snapshot is targeted", a.Target)
				}
				if !strings.Contains(a.Target, "@"+cloneSourceSnapshotPrefix) {
					t.Fatalf("%s: not a clone-source snapshot", a.Target)
				}
				continue
			}
			switch {
			case !row.Managed:
				t.Fatalf("%s: unowned volume targeted", a.Target)
			case isWanted[a.Target]:
				t.Fatalf("%s: desired volume targeted", a.Target)
			case time.Unix(now, 0).Sub(time.Unix(row.Creation, 0)) < time.Hour:
				t.Fatalf("%s: young volume targeted", a.Target)
			case len(want) == 0 && row.Committed:
				t.Fatalf("%s: committed volume collected on an empty desired set", a.Target)
			}
			for s := range before.SnapshotsOf(a.Target) {
				if s.SnapshotName != "" {
					t.Fatalf("%s: volume with CSI snapshot %s targeted", a.Target, s.ID)
				}
			}
		}

		after, err := ep.scan(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		for r := range before.All() {
			vol, _, _ := strings.Cut(r.ID, "@")
			keep := r.SnapshotName != "" || isWanted[vol] && r.Type != zfs.TypeSnapshot
			if r.Type != zfs.TypeSnapshot {
				keep = keep || !r.Managed || csiSnapped(before, r.ID)
			}
			if _, ok := after.Get(r.ID); keep && !ok {
				t.Fatalf("%s was destroyed (plan %+v)", r.ID, done)
			}
		}

		again, err := ep.Reconcile(ctx, req)
		if err != nil || len(again.Actions) != 0 {
			t.Fatalf("second reconcile: %+v %v", again.Actions, err)
		}
	})
}

func csiSnapped(t Table, id string) bool {
	for s := range t.SnapshotsOf(id) {
		if s.SnapshotName != "" {
			return true
		}
	}
	return false
}
