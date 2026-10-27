package endpoint_test

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/zfs"
)

// Reconcile contract tests: simulator or, with ZFS_TEST_POOL, a real pool.
// The endpoint's clock is moved forward so everything made here is old.

func later(t *testing.T, ep *endpoint.Endpoint, h *harness) *endpoint.Endpoint {
	t.Helper()
	ep2, err := endpoint.New(h.Z, endpoint.Config{Parent: h.Parent, LockWait: time.Second,
		Now: func() time.Time { return time.Now().Add(48 * time.Hour) }})
	if err != nil {
		t.Fatal(err)
	}
	return ep2
}

func targets(p endpoint.ReconcilePlan) (acts, skips []string) {
	for _, a := range p.Actions {
		acts = append(acts, a.Target)
	}
	for _, s := range p.Skipped {
		skips = append(skips, s.Target)
	}
	return
}

func TestReconcileCollectsAbandonedCreate(t *testing.T) {
	ctx := context.Background()
	ep, h := setup(t)
	// a CreateVolume that crashed before its commit marker, never retried
	if err := h.Z.CreateFilesystem(ctx, h.Parent+"/abandoned", map[string]string{
		"democratic-csi:managed_resource": "true",
		"democratic-csi:csi_volume_name":  "abandoned",
	}, false); err != nil {
		t.Fatal(err)
	}
	// just made: it may be in flight
	p, err := ep.Reconcile(ctx, endpoint.ReconcileRequest{})
	if err != nil || len(p.Actions) != 0 || len(p.Skipped) != 1 || p.Skipped[0].Reason != endpoint.SkipTooYoung {
		t.Fatalf("young: %+v %v", p, err)
	}

	old := later(t, ep, h)
	dry, err := old.Reconcile(ctx, endpoint.ReconcileRequest{DryRun: true})
	if acts, _ := targets(dry); err != nil || !slices.Equal(acts, []string{"abandoned"}) || dry.Actions[0].Reason != endpoint.ReasonUncommitted {
		t.Fatalf("dry run: %+v %v", dry, err)
	}
	if !h.Has(h.Parent + "/abandoned") {
		t.Fatal("dry run applied")
	}
	if _, err := old.Reconcile(ctx, endpoint.ReconcileRequest{}); err != nil {
		t.Fatal(err)
	}
	if h.Has(h.Parent + "/abandoned") {
		t.Fatal("abandoned create not collected")
	}
	if again, err := old.Reconcile(ctx, endpoint.ReconcileRequest{}); err != nil || len(again.Actions) != 0 {
		t.Fatalf("second reconcile: %+v %v", again, err)
	}
}

func TestReconcileCollectsLeakedCloneSource(t *testing.T) {
	ctx := context.Background()
	ep, h := setup(t)
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "src", CapacityBytes: 1 << 30}); err != nil {
		t.Fatal(err)
	}
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "clone", CapacityBytes: 1 << 30, SourceVolumeID: "src"}); err != nil {
		t.Fatal(err)
	}
	// review H3: a clone that failed after its source snapshot was cut
	leaked := h.Parent + "/src@volume-source-for-volume-failed"
	if err := h.Z.Snapshot(ctx, leaked, nil); err != nil {
		t.Fatal(err)
	}
	old := later(t, ep, h)
	p, err := old.Reconcile(ctx, endpoint.ReconcileRequest{DesiredVolumeIDs: []string{"src", "clone"}})
	if acts, _ := targets(p); err != nil || !slices.Equal(acts, []string{"src@volume-source-for-volume-failed"}) {
		t.Fatalf("plan %+v %v", p, err)
	}
	if h.Has(leaked) {
		t.Fatal("leaked clone source not collected")
	}
	if !h.Has(h.Parent+"/src@volume-source-for-volume-clone") || !h.Has(h.Parent+"/clone") {
		t.Fatal("clone source in use was collected")
	}
}

func TestReconcileAgainstDesiredSet(t *testing.T) {
	ctx := context.Background()
	ep, h := setup(t)
	for _, name := range []string{"keep", "drop", "snapped", "src"} {
		if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: name, CapacityBytes: 1 << 30}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "clone", CapacityBytes: 1 << 30, SourceVolumeID: "src"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ep.CreateSnapshot(ctx, "snap-1", "snapped"); err != nil {
		t.Fatal(err)
	}
	if err := h.Z.CreateFilesystem(ctx, h.Parent+"/foreign", nil, false); err != nil {
		t.Fatal(err)
	}
	old := later(t, ep, h)

	// an empty set is not a statement that nothing is wanted
	p, err := old.Reconcile(ctx, endpoint.ReconcileRequest{})
	if err != nil || len(p.Actions) != 0 {
		t.Fatalf("empty desired set: %+v %v", p, err)
	}

	want := []string{"keep", "gone-pvc"}
	p, err = old.Reconcile(ctx, endpoint.ReconcileRequest{DesiredVolumeIDs: want})
	if err != nil {
		t.Fatal(err)
	}
	acts, skips := targets(p)
	// the clone first, its source snapshot with it, then the freed source
	if !slices.Equal(acts, []string{"clone", "src@volume-source-for-volume-clone", "drop", "src"}) {
		t.Fatalf("actions %q", acts)
	}
	if !slices.Equal(skips, []string{"gone-pvc", "snapped"}) {
		t.Fatalf("skipped %+v", p.Skipped)
	}
	for _, gone := range []string{"clone", "drop", "src"} {
		if h.Has(h.Parent + "/" + gone) {
			t.Errorf("%s not collected", gone)
		}
	}
	for _, kept := range []string{"keep", "snapped", "snapped@snap-1", "foreign"} {
		if !h.Has(h.Parent + "/" + kept) {
			t.Errorf("%s destroyed", kept)
		}
	}
	if again, err := old.Reconcile(ctx, endpoint.ReconcileRequest{DesiredVolumeIDs: want}); err != nil || len(again.Actions) != 0 {
		t.Fatalf("second reconcile: %+v %v", again, err)
	}

	_, err = old.Reconcile(ctx, endpoint.ReconcileRequest{DesiredVolumeIDs: []string{"bad/id"}})
	wantCode(t, err, endpoint.InvalidArgument)
}

// parked holds the first zfs call made after arm, outside the sim's own
// lock, until release is closed.
type parked struct {
	zfs.Runner
	mu      sync.Mutex
	armed   bool
	entered chan struct{}
	release chan struct{}
}

func (g *parked) arm() {
	g.mu.Lock()
	g.armed = true
	g.mu.Unlock()
}

func (g *parked) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	g.mu.Lock()
	hold := g.armed
	g.armed = false
	g.mu.Unlock()
	if hold {
		close(g.entered)
		<-g.release
	}
	return g.Runner.Run(ctx, name, args...)
}

// Reconcile takes the lock of everything it touches: an operation holding
// one makes it Aborted, with nothing applied. A dry run takes no locks.
func TestReconcileLocksTargets(t *testing.T) {
	ctx := context.Background()
	_, h := setup(t)
	g := &parked{Runner: h.Sim(t), entered: make(chan struct{}), release: make(chan struct{})}
	ep, err := endpoint.New(zfs.New(g), endpoint.Config{Parent: h.Parent, LockWait: 200 * time.Millisecond,
		Now: func() time.Time { return time.Now().Add(48 * time.Hour) }})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"keep", "busy"} {
		if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: name, CapacityBytes: 1 << 30}); err != nil {
			t.Fatal(err)
		}
	}
	// an ExpandVolume holding vol:busy, parked in its scan
	g.arm()
	done := make(chan error)
	go func() { _, err := ep.ExpandVolume(ctx, "busy", 2<<30, 0); done <- err }()
	<-g.entered

	keep := endpoint.ReconcileRequest{DesiredVolumeIDs: []string{"keep"}}
	_, err = ep.Reconcile(ctx, keep)
	wantCode(t, err, endpoint.Aborted)
	if !h.Has(h.Parent + "/busy") {
		t.Fatal("destroyed a volume another operation held")
	}
	dry := keep
	dry.DryRun = true
	if p, err := ep.Reconcile(ctx, dry); err != nil || len(p.Actions) != 1 {
		t.Fatalf("dry run: %+v %v", p, err)
	}
	close(g.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := ep.Reconcile(ctx, keep); err != nil || h.Has(h.Parent+"/busy") {
		t.Fatalf("after release: %v", err)
	}
}
