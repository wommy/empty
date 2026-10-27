package zfs_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

func TestClassify(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		stderr string
		want   error
	}{
		{"cannot create 'tank/a': dataset already exists\n", zfs.ErrExists},
		{"cannot open 'tank/a': dataset does not exist\n", zfs.ErrNotFound},
		{"cannot create 'tank/a/b': parent does not exist\n", zfs.ErrParentNotFound},
		{"cannot destroy 'tank/a': filesystem has children\nuse '-r' to destroy the following datasets:\ntank/a/b\n", zfs.ErrHasChildren},
		{"cannot destroy 'tank/a@s': snapshot has dependent clones\nuse '-R' ...\n", zfs.ErrHasClones},
		{"cannot destroy 'tank/a': dataset is busy\n", zfs.ErrBusy},
		{"cannot create 'tank/a': out of space\n", zfs.ErrNoSpace},
		// first seen on real ZFS in CI: thick zvol volsize/refreservation
		{"cannot set property for 'tank/a': size is greater than available space\n", zfs.ErrNoSpace},
		{"cannot set property for 'tank/a': size is less than current used or reserved space\n", zfs.ErrInvalidProperty},
	} {
		c := zfs.New(failRunner(tc.stderr))
		err := c.Destroy(ctx, "tank/a", zfs.DestroyOptions{})
		if !errors.Is(err, tc.want) {
			t.Errorf("%q: got %v, want %v", tc.stderr, err, tc.want)
		}
	}
}

// An unclassified failure must never read as "absent" (zfs-localpv and others
// treat any list error as not-found, which turns transient failures into
// duplicate creates or false delete successes).
func TestExistsDoesNotSwallowUnknownErrors(t *testing.T) {
	c := zfs.New(failRunner("internal error: Input/output error\n"))
	ok, err := c.Exists(context.Background(), "tank/a")
	if err == nil || ok {
		t.Fatalf("Exists = %v, %v; want error", ok, err)
	}
	c = zfs.New(failRunner("cannot open 'tank/a': dataset does not exist\n"))
	ok, err = c.Exists(context.Background(), "tank/a")
	if err != nil || ok {
		t.Fatalf("Exists = %v, %v; want false, nil", ok, err)
	}
}

func TestRejectsFlagLikeAndShellNames(t *testing.T) {
	c := zfs.New(zfstest.New("tank"))
	for _, name := range []string{"-rf", "tank/a b", "tank/$(id)", "tank/a;rm", "tank//a", ""} {
		if err := c.CreateFilesystem(context.Background(), name, nil, false); err == nil {
			t.Errorf("CreateFilesystem(%q) accepted", name)
		}
	}
	if err := c.Set(context.Background(), "tank", map[string]string{"a:b": "x\ny"}); err == nil {
		t.Error("newline in property value accepted")
	}
}

func TestLifecycleAgainstSim(t *testing.T) {
	ctx := context.Background()
	sim := zfstest.New("tank")
	c := zfs.New(sim)

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(c.CreateFilesystem(ctx, "tank/k8s/vols", nil, true))
	must(c.CreateVolume(ctx, "tank/k8s/vols/v1", 1<<30, true, map[string]string{"dc:name": "v1"}))
	if err := c.CreateVolume(ctx, "tank/k8s/vols/v1", 1<<30, true, nil); !errors.Is(err, zfs.ErrExists) {
		t.Fatalf("second create: %v", err)
	}

	d, err := c.Get(ctx, "tank/k8s/vols/v1", "type", "volsize", "refreservation", "dc:name")
	must(err)
	if d.Type() != zfs.TypeVolume {
		t.Fatalf("type %q", d.Type())
	}
	if n, _ := d.Props["volsize"].Uint(); n != 1<<30 {
		t.Fatalf("volsize %d", n)
	}
	if d.Props["refreservation"].Value != "0" {
		t.Fatalf("sparse zvol has refreservation %q", d.Props["refreservation"].Value)
	}
	if p := d.Props["dc:name"]; p.Value != "v1" || !p.Local() {
		t.Fatalf("user prop %+v", p)
	}

	must(c.Snapshot(ctx, "tank/k8s/vols/v1@s1", nil))
	must(c.Clone(ctx, "tank/k8s/vols/v1@s1", "tank/k8s/vols/v2", nil))
	if err := c.Destroy(ctx, "tank/k8s/vols/v1@s1", zfs.DestroyOptions{}); !errors.Is(err, zfs.ErrHasClones) {
		t.Fatalf("destroy cloned snapshot: %v", err)
	}
	// deferred destroy succeeds now and reaps when the clone goes
	must(c.Destroy(ctx, "tank/k8s/vols/v1@s1", zfs.DestroyOptions{Defer: true}))
	if !sim.Has("tank/k8s/vols/v1@s1") {
		t.Fatal("deferred snapshot destroyed early")
	}
	must(c.Destroy(ctx, "tank/k8s/vols/v2", zfs.DestroyOptions{}))
	if sim.Has("tank/k8s/vols/v1@s1") {
		t.Fatal("deferred snapshot not reaped after last clone")
	}

	list, err := c.List(ctx, "tank/k8s/vols", zfs.ListOptions{})
	must(err)
	var names []string
	for _, d := range list {
		names = append(names, d.Name)
	}
	if !reflect.DeepEqual(names, []string{"tank/k8s/vols/v1"}) {
		t.Fatalf("list %v", names)
	}
}

type failRunner string

func (f failRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return nil, &zfs.ExitError{Args: args, Code: 1, Stderr: string(f)}
}

// ADR 0003: ownership is a user property set locally. A received value
// (zfs send -R | zfs recv) was set by whoever owned the source, not here.
func TestPropLocalIsOnlyLocal(t *testing.T) {
	for src, want := range map[string]bool{
		"local": true, "received": false, "default": false, "-": false,
		"inherited from tank": false, "temporary": false,
	} {
		if got := (zfs.Prop{Value: "x", Source: src}).Local(); got != want {
			t.Errorf("source %q: Local() = %v, want %v", src, got, want)
		}
	}
}
