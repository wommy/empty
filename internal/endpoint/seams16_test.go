package endpoint

// Wave-16 seam regressions. Each failed before its fix, but for the pin
// of a finding judged wrong (TestThickZvolReservationIgnoresCopies).

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

// replanSwapper renames a away and makes an impostor with a's markers in
// its place right before the second whole-parent scan of the run: the
// one Reconcile makes under the locks, after the digest matched.
type replanSwapper struct {
	zfs.Runner
	h        *a4
	scans    int
	impostor string // its guid, "" until made
}

func (s *replanSwapper) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "get" && args[len(args)-1] == a4Parent && strings.Contains(strings.Join(args, " "), "volblocksize") {
		if s.scans++; s.scans == 2 {
			_, err := s.Runner.Run(ctx, "zfs", "rename", a4Parent+"/a", a4Parent+"/a-moved")
			s.h.must(err)
			s.h.vol("a", true)
			d, err := s.h.z.Get(ctx, a4Parent+"/a", "guid")
			s.h.must(err)
			s.impostor = d.Props["guid"].Value
		}
	}
	return s.Runner.Run(ctx, name, args...)
}

// The digest covers the guids of the unlocked plan; the locked re-plan
// must too. A dataset replaced between the two scans is not the one the
// caller's digest named: skipped, never destroyed.
func TestReconcileLockedReplanKeepsTheDigestedGuid(t *testing.T) {
	h := newA4(t)
	ctx := context.Background()
	h.vol("keep", true)
	h.vol("a", true)
	sw := &replanSwapper{Runner: h.sim, h: h}
	ep, err := New(zfs.New(sw), h.ep.cfg)
	h.must(err)
	r := ReconcileRequest{DesiredVolumeIDs: []string{"keep"}}
	dry := r
	dry.DryRun = true
	p, err := h.ep.Reconcile(ctx, dry)
	h.must(err)
	r.PlanDigest = p.Digest
	if p, err = ep.Reconcile(ctx, r); err != nil {
		t.Fatal(err)
	}
	if sw.impostor == "" {
		t.Fatal("no swap")
	}
	d, err := h.z.Get(ctx, a4Parent+"/a", "guid")
	if err != nil || d.Props["guid"].Value != sw.impostor {
		t.Fatalf("the impostor made in place of a was destroyed (%v): %+v", err, p)
	}
	if len(p.Actions) != 0 || !slices.Contains(p.Skipped, ReconcileSkip{Target: "a", Reason: SkipChangedSinceScan}) {
		t.Fatalf("want a skipped as changed since the scan: %+v", p)
	}
}

// Not a defect (wave16 S2, judged against libzfs): refreservation=auto
// does not scale by copies, since zvol_volsize_to_reservation looks copies
// up as a string and zfs create's validated props hold it as a uint64.
// A thick copies=3 zvol takes about volsize from available, which is what
// the headroom guard counts; counting 3× would refuse creates that fit.
func TestThickZvolReservationIgnoresCopies(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	z := zfs.New(zfstest.New("tank"))
	for _, c := range []string{"1", "3"} {
		if err := z.CreateVolume(ctx, "tank/c"+c, 1<<30, false, map[string]string{"copies": c}); err != nil {
			t.Fatal(err)
		}
	}
	one, err := z.Get(ctx, "tank/c1", "refreservation")
	if err != nil {
		t.Fatal(err)
	}
	three, err := z.Get(ctx, "tank/c3", "refreservation")
	if err != nil {
		t.Fatal(err)
	}
	if one.Props["refreservation"].Value != three.Props["refreservation"].Value {
		t.Errorf("copies=3 reserves %s, copies=1 %s: libzfs never scales by copies",
			three.Props["refreservation"].Value, one.Props["refreservation"].Value)
	}
	need, _ := pl.planReserves(NewTable(nil), Plan{{Kind: OpCreateVol, Target: "tank/v/new", Size: 1 << 40, Props: map[string]string{"copies": "3"}}})
	if need != 1<<40 {
		t.Errorf("a 1T copies=3 create counts %d, want its volsize", need)
	}
}
