package endpoint

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

// scanMeter counts what a run's `zfs get -j` calls printed: datasets read
// with the full scan columns (rows) and those of every other listing.
type scanMeter struct {
	zfs.Runner
	mu         sync.Mutex
	rows, list int // datasets printed
	rowBytes   int
	listBytes  int
}

func (m *scanMeter) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := m.Runner.Run(ctx, name, args...)
	if len(args) > 0 && args[0] == "get" && slices.Contains(args, "-j") {
		n := strings.Count(string(out), `"createtxg":`)
		m.mu.Lock()
		if slices.ContainsFunc(args, func(a string) bool { return strings.Contains(a, "volblocksize") }) {
			m.rows, m.rowBytes = m.rows+n, m.rowBytes+len(out)
		} else {
			m.list, m.listBytes = m.list+n, m.listBytes+len(out)
		}
		m.mu.Unlock()
	}
	return out, err
}

// deleteScanCost deletes clone c of src, with n unrelated volumes (each
// with a CSI snapshot) beside them, and reports what the delete read.
func deleteScanCost(t testing.TB, n int) *scanMeter {
	ctx := context.Background()
	sim := zfstest.New("tank")
	m := &scanMeter{Runner: sim}
	z := zfs.New(m)
	if err := z.CreateFilesystem(ctx, "tank/v", nil, false); err != nil {
		t.Fatal(err)
	}
	ep, err := New(z, Config{Parent: "tank/v", Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	for i := range n {
		id := fmt.Sprintf("pvc-%04d", i)
		if _, err := ep.CreateVolume(ctx, CreateVolumeRequest{Name: id}); err != nil {
			t.Fatal(err)
		}
		if _, err := ep.CreateSnapshot(ctx, "s-"+id, id); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range []CreateVolumeRequest{{Name: "src"}, {Name: "c", SourceVolumeID: "src"}} {
		if _, err := ep.CreateVolume(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	m.mu.Lock()
	m.rows, m.list, m.rowBytes, m.listBytes = 0, 0, 0, 0
	m.mu.Unlock()
	if err := ep.DeleteVolume(ctx, "c"); err != nil {
		t.Fatal(err)
	}
	if sim.Has("tank/v/c") || !sim.Has("tank/v/src") {
		t.Fatal("DeleteVolume(c) did not delete c alone")
	}
	if sim.Has("tank/v/src@" + cloneSourceSnapshotPrefix + "c") {
		t.Fatal("DeleteVolume(c) kept the clone's origin")
	}
	return m
}

// DeleteVolume reads the volume's subtree and the volumes its plan touches
// (the clone origin's), not the whole parent: the rows it decodes do not
// grow with unrelated volumes. Only a names-only listing of the parent's
// snapshots does, to find clone sources cut for the volume.
func TestDeleteVolumeScanIsSubtreeScoped(t *testing.T) {
	t.Parallel()
	few, many := deleteScanCost(t, 1), deleteScanCost(t, 100)
	t.Logf("1 unrelated: %d rows (%d B), names %d (%d B); 100 unrelated: %d rows (%d B), names %d (%d B)",
		few.rows, few.rowBytes, few.list, few.listBytes, many.rows, many.rowBytes, many.list, many.listBytes)
	// c, src and src@volume-source-for-volume-c
	if few.rows != 3 || many.rows != few.rows {
		t.Errorf("DeleteVolume decoded %d rows beside 1 unrelated volume, %d beside 100: want 3 both", few.rows, many.rows)
	}
}

// A clone of the volume's snapshot outside the parent is a dependent too:
// the refusal comes from the plan, naming it, before any destroy runs.
func TestDeleteVolumeRefusesACloneOutsideTheParent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sim := zfstest.New("tank")
	z := zfs.New(sim)
	for _, d := range []string{"tank/v", "tank/elsewhere"} {
		if err := z.CreateFilesystem(ctx, d, nil, false); err != nil {
			t.Fatal(err)
		}
	}
	ep, err := New(z, Config{Parent: "tank/v", Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ep.CreateVolume(ctx, CreateVolumeRequest{Name: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ep.CreateSnapshot(ctx, "s", "a"); err != nil {
		t.Fatal(err)
	}
	snap := "tank/v/a@" + snapComponent("a", "s")
	if err := z.Clone(ctx, snap, "tank/elsewhere/x", nil); err != nil {
		t.Fatal(err)
	}
	if err := ep.DeleteSnapshot(ctx, "a@"+snapComponent("a", "s")); err != nil {
		t.Fatal(err)
	}
	sim.Calls = nil
	err = ep.DeleteVolume(ctx, "a")
	want := "volume a has dependent clone tank/elsewhere/x"
	if CodeOf(err) != FailedPrecondition || err == nil || err.Error() != want {
		t.Fatalf("DeleteVolume: %v (code %d), want FailedPrecondition %q", err, CodeOf(err), want)
	}
	for _, c := range sim.Calls {
		if len(c) > 1 && c[1] == "destroy" {
			t.Fatalf("DeleteVolume ran %v before refusing", c)
		}
	}
}
