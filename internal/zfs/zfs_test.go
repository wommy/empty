package zfs_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

func TestClassify(t *testing.T) {
	t.Parallel()
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
		// texts below are OpenZFS's; see errors.go for each source
		{"cannot create 'tank/a': pool is read-only\n", zfs.ErrUnavailable},
		{"cannot destroy snapshot tank/a@s: it's being held. Run 'zfs holds -r tank/a@s' to see holders.\n", zfs.ErrBusy},
		{"cannot create 'tank/enc/a': encryption root's key is not loaded or provided\n", zfs.ErrPrecondition},
		{"cannot create 'tank/a': 'snapdir' must be one of 'hidden | visible'\n", zfs.ErrInvalidProperty},
		{"cannot create 'tank/a': 'recordsize' must be power of 2 from 512B to 128K\n", zfs.ErrInvalidProperty},
		{"cannot create 'tank/a': pool must be upgraded to set this property or value\n", zfs.ErrPrecondition},
		{"cannot create 'tank/a': the loaded zfs module does not support this operation. A reboot may be required to enable this operation.\n", zfs.ErrUnavailable},
		// every other phrase of errors.go's table, written out here and not
		// ranged over from it, so dropping or re-kinding an alias fails
		{"cannot destroy 'tank/a@s': snapshot is cloned\n", zfs.ErrHasClones},
		{"cannot create 'tank/a/b': no such parent 'tank/a'\n", zfs.ErrParentNotFound},
		{"cannot create 'tank/a': pool or dataset exists\n", zfs.ErrExists},
		{"cannot open 'tank/a': no such pool or dataset\n", zfs.ErrNotFound},
		{"could not find any snapshots to destroy; check snapshot names.\n", zfs.ErrNotFound},
		{"cannot destroy 'tank/a': pool or dataset is busy\n", zfs.ErrBusy},
		{"cannot set property for 'tank/a': invalid property value\n", zfs.ErrInvalidProperty},
		{"cannot create 'tank/a': bad property list\n", zfs.ErrInvalidProperty},
		{"cannot set property for 'tank/a': invalid property 'x'\n", zfs.ErrInvalidProperty},
		{"cannot set property for 'tank/a': property is read-only\n", zfs.ErrInvalidProperty},
		{"cannot create 'tank/a': permission denied\n", zfs.ErrPermission},
		// a missing or stuck pool is never a missing dataset
		{"cannot open 'gone': no such pool\n", zfs.ErrUnavailable},
		{"cannot create 'tank/a': pool I/O is currently suspended\n", zfs.ErrUnavailable},
		{"cannot open 'tank/a': pool is unavailable\n", zfs.ErrUnavailable},
		{"CANNOT OPEN 'TANK/A': DATASET DOES NOT EXIST\n", zfs.ErrNotFound}, // case-insensitive
	} {
		c := zfs.New(failRunner(tc.stderr))
		err := c.Destroy(ctx, "tank/a", zfs.DestroyOptions{})
		if !errors.Is(err, tc.want) {
			t.Errorf("%q: got %v, want %v", tc.stderr, err, tc.want)
		}
	}
	c := zfs.New(failRunner("something else entirely\n"))
	var ze *zfs.Error
	if err := c.Destroy(ctx, "tank/a", zfs.DestroyOptions{}); !errors.As(err, &ze) || ze.Kind != zfs.KindUnknown {
		t.Errorf("unmatched text: %v, want KindUnknown", err)
	}
}

// errors.Is matches a kind only against a bare sentinel: a target naming an
// op or dataset is a specific failure, not a kind.
func TestErrorIsMatchesBareSentinelsOnly(t *testing.T) {
	t.Parallel()
	e := &zfs.Error{Kind: zfs.KindNotFound, Op: "destroy", Dataset: "tank/a", Stderr: "dataset does not exist"}
	for _, tc := range []struct {
		target error
		want   bool
	}{
		{zfs.ErrNotFound, true},
		{zfs.ErrExists, false},
		{&zfs.Error{Kind: zfs.KindNotFound, Op: "destroy"}, false},
		{&zfs.Error{Kind: zfs.KindNotFound, Dataset: "tank/a"}, false},
		{errors.New("dataset does not exist"), false},
	} {
		if got := errors.Is(e, tc.target); got != tc.want {
			t.Errorf("errors.Is(e, %#v) = %v, want %v", tc.target, got, tc.want)
		}
	}
}

// The message is zfs's own stderr, else the exec error.
func TestErrorTextPrefersStderrThenCause(t *testing.T) {
	t.Parallel()
	cause := errors.New("exit status 1")
	for _, tc := range []struct {
		e        *zfs.Error
		has, not string
	}{
		{&zfs.Error{Kind: zfs.KindNotFound, Op: "get", Dataset: "d", Stderr: "boom\n", Err: cause}, "boom", "exit status"},
		{&zfs.Error{Kind: zfs.KindNotFound, Op: "get", Dataset: "d", Err: cause}, "exit status 1", "\x00"},
		{&zfs.Error{Kind: zfs.KindNotFound, Op: "get", Dataset: "d"}, "zfs get d", "\x00"}, // neither: no panic
	} {
		if s := tc.e.Error(); !strings.Contains(s, tc.has) || strings.Contains(s, tc.not) {
			t.Errorf("Error() = %q, want %q and not %q", s, tc.has, tc.not)
		}
	}
}

// TestClassify244Messages: OpenZFS 2.4.4 messages the zfsdiff replay
// recorded as KindUnknown (wave9 zfs-diff report), with the texts the
// reference model quotes from source (internal/zfs/zfsdiff).
func TestClassify244Messages(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		stderr string
		want   error
	}{
		// libzfs_dataset.c zfs_valid_proplist: set-once, on set and clone -o
		{"cannot set property for 'tank/v': 'volblocksize' is readonly\n", zfs.ErrInvalidProperty},
		{"cannot create 'tank/c': 'volblocksize' is readonly\n", zfs.ErrInvalidProperty},
		{"cannot set property for 'tank/a': 'volsize' does not apply to datasets of this type\n", zfs.ErrInvalidProperty},
		{"cannot set property for 'tank/v': 'volsize' cannot be zero\n", zfs.ErrInvalidProperty},
		{"cannot create 'tank/v': volume size cannot be zero\n", zfs.ErrInvalidProperty},
		{"cannot set property for 'tank/v': 'volsize' must be a multiple of volume block size (16K)\n", zfs.ErrInvalidProperty},
		{"cannot set property for 'tank/a': use 'none' to disable quota/refquota\n", zfs.ErrInvalidProperty},
		{"cannot set property for 'tank/a@s': this property can not be modified for snapshots\n", zfs.ErrInvalidProperty},
		// zfs_main.c parseprop, exit 255
		{"property 'atime' specified multiple times\n", zfs.ErrInvalidProperty},
		// zfs_namecheck: a malformed argument, final like a bad property
		{"cannot open 'tank/a': missing '@' delimiter in snapshot name\n", zfs.ErrInvalidProperty},
		// a zvol as parent: configuration only an administrator changes
		{"cannot create 'tank/v/a': parent is not a filesystem\n", zfs.ErrPrecondition},
		{"cannot promote 'tank/a': not a cloned filesystem\n", zfs.ErrPrecondition},
		// EZFS_EXISTS without the "already exists" text
		{"cannot promote 'tank/x': conflicting snapshot 's1' from parent 'tank/a'\n", zfs.ErrExists},
		// holds: EZFS_REFTAG_HOLD, EZFS_REFTAG_RELE
		{"cannot hold snapshot 'tank/a@s': tag already exists on this dataset\n", zfs.ErrExists},
		{"cannot release hold from snapshot 'tank/a@s': no such tag on this dataset\n", zfs.ErrNotFound},
	} {
		c := zfs.New(failRunner(tc.stderr))
		err := c.Destroy(ctx, "tank/a", zfs.DestroyOptions{})
		if !errors.Is(err, tc.want) {
			t.Errorf("%q: got %v, want %v", tc.stderr, err, tc.want)
		}
	}
}

// `zfs mount` on the simulator: what create -u left unmounted mounts
// (noauto too); mounted again is Exists; off, none, legacy and zvols
// refuse; the mount shares nothing.
func TestMountAgainstSim(t *testing.T) {
	ctx := context.Background()
	sim := zfstest.New("tank")
	c := zfs.New(sim)
	mounted := func(name string) string {
		t.Helper()
		d, err := c.Get(ctx, name, "mounted")
		if err != nil {
			t.Fatal(err)
		}
		return d.Props["mounted"].Value
	}
	for name, props := range map[string]map[string]string{
		"tank/a": {"sharenfs": "on"},
		"tank/n": {"canmount": "noauto"},
		"tank/o": {"canmount": "off"},
		"tank/m": {"mountpoint": "none"},
		"tank/l": {"mountpoint": "legacy"},
	} {
		if _, err := sim.Run(ctx, "zfs", append(append([]string{"create", "-u"}, propOpts(props)...), name)...); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.CreateVolume(ctx, "tank/v", 1<<20, true, nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"tank/a", "tank/n"} {
		if err := c.Mount(ctx, name); err != nil || mounted(name) != "yes" {
			t.Fatalf("mount %s: %v, mounted=%s", name, err, mounted(name))
		}
		if err := c.Mount(ctx, name); !errors.Is(err, zfs.ErrExists) {
			t.Fatalf("mount %s again: %v, want Exists", name, err)
		}
	}
	if etab, _ := sim.ReadEtab(ctx); len(etab) != 0 {
		t.Fatalf("zfs mount shared: %q", etab)
	}
	for _, name := range []string{"tank/o", "tank/m", "tank/l", "tank/v"} {
		if err := c.Mount(ctx, name); err == nil || errors.Is(err, zfs.ErrExists) {
			t.Fatalf("mount %s: %v, want a refusal", name, err)
		}
	}
	if err := c.Mount(ctx, "tank/missing"); !zfs.IsNotFound(err) {
		t.Fatalf("mount missing: %v", err)
	}
}

func propOpts(props map[string]string) []string {
	var out []string
	for k, v := range props {
		out = append(out, "-o", k+"="+v)
	}
	return out
}

// An unclassified failure must never read as "absent" (zfs-localpv and others
// treat any list error as not-found, which turns transient failures into
// duplicate creates or false delete successes).
func TestExistsDoesNotSwallowUnknownErrors(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// A command killed by its context may or may not have done its work: it
// is reported as interrupted (outcome unknown), carrying the context's
// error, never as an unclassified failure.
func TestKilledCommandIsInterrupted(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	// sh dies on SIGTERM, but its child keeps stdout open: only WaitDelay
	// stops Wait from blocking until the child exits
	bin := filepath.Join(t.TempDir(), "zfs")
	writeScript(t, bin, "#!/bin/sh\nsleep 30\n")
	c := &zfs.Client{Runner: zfs.LocalRunner{WaitDelay: 50 * time.Millisecond}, Binary: bin}
	start := time.Now()
	_, err := c.Get(ctx, "10", "x")
	if time.Since(start) > 5*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
	if !errors.Is(err, zfs.ErrInterrupted) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want interrupted by deadline", err)
	}
}

// The simulator stores index properties by value and prints the first
// table name, like zfs_prop.c/zprop_index_to_string (OpenZFS 2.4).
func TestSimPrintsIndexPropertiesLikeOpenZFS(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []struct {
		prop, set string
		want      string // "" = refused as an invalid property value
	}{
		{"compression", "gzip-6", "gzip"},
		{"compression", "zstd-fast-1", "zstd-fast"},
		{"acltype", "posixacl", "posix"},
		{"xattr", "on", "sa"},
		{"xattr", "dir", "dir"},
		{"snapdir", "disabled", "disabled"},
		{"snapdir", "shown", ""},
		{"compression", "bogus", ""},
	} {
		sim := zfstest.New("tank")
		c := zfs.New(sim)
		err := c.CreateFilesystem(ctx, "tank/a", map[string]string{tc.prop: tc.set}, false)
		if tc.want == "" {
			if !errors.Is(err, zfs.ErrInvalidProperty) {
				t.Errorf("%s=%s: %v, want an invalid property", tc.prop, tc.set, err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		d, err := c.Get(ctx, "tank/a", tc.prop)
		if got := d.Props[tc.prop]; err != nil || got.Value != tc.want || got.Source != "local" {
			t.Errorf("%s=%s reads back %+v %v, want %q", tc.prop, tc.set, got, err, tc.want)
		}
	}
}

// ADR 0003: ownership is a user property set locally. A received value
// (zfs send -R | zfs recv) was set by whoever owned the source, not here.
func TestPropLocalIsOnlyLocal(t *testing.T) {
	t.Parallel()
	for src, want := range map[string]bool{
		"local": true, "received": false, "default": false, "-": false,
		"inherited from tank": false, "temporary": false,
	} {
		if got := (zfs.Prop{Value: "x", Source: src}).Local(); got != want {
			t.Errorf("source %q: Local() = %v, want %v", src, got, want)
		}
	}
}

// `zfs set -u` changes sharenfs without mounting: an unmounted canmount=on
// filesystem stays unmounted, where a plain set mounts and shares it.
func TestSetNoMountLeavesUnmounted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sim := zfstest.New("tank")
	c := zfs.New(sim)
	if err := c.CreateFilesystem(ctx, "tank/fs", nil, false); err != nil {
		t.Fatal(err)
	}
	sim.Unmount("tank/fs")
	mounted := func() string {
		d, err := c.Get(ctx, "tank/fs", "mounted")
		if err != nil {
			t.Fatal(err)
		}
		return d.Props["mounted"].Value
	}
	if err := c.SetNoMount(ctx, "tank/fs", map[string]string{"sharenfs": "rw=10.0.0.0/24"}); err != nil {
		t.Fatal(err)
	}
	if got := sim.Calls[len(sim.Calls)-1]; !reflect.DeepEqual(got[1:], []string{"set", "-u", "sharenfs=rw=10.0.0.0/24", "tank/fs"}) {
		t.Fatalf("args %q", got)
	}
	if m := mounted(); m != "no" {
		t.Fatalf("set -u mounted it: %s", m)
	}
	// changelist_postfix mounts an unmounted canmount=on filesystem only
	// when a share property ends up on (sharenfs=off would leave it)
	if err := c.Set(ctx, "tank/fs", map[string]string{"sharenfs": "rw=10.0.0.0/16"}); err != nil {
		t.Fatal(err)
	}
	if m := mounted(); m != "yes" {
		t.Fatalf("plain set did not mount: %s", m)
	}
}

// usedbyrefreservation is what a dataset's refreservation charges its own
// dir beyond its unique data (dsl_dir.c dsl_dir_stats, DD_USED_REFRSRV;
// dsl_dataset.c dsl_dataset_set_refreservation_sync_impl): the whole
// refreservation in the Sim, whose datasets are empty. used is the dir's
// used, which includes it and each child's larger of used and
// reservation. A snapshot has no dir stats and prints "-". available less
// usedbyrefreservation is what a new child gets while no refquota caps it.
func TestSimUsedByRefreservation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sim := zfstest.New("tank")
	sim.PoolSize = 10 << 30
	c := zfs.New(sim)
	run := func(args ...string) {
		t.Helper()
		if _, err := sim.Run(ctx, "zfs", args...); err != nil {
			t.Fatal(args, err)
		}
	}
	run("create", "-o", "refreservation=1073741824", "tank/p")
	run("create", "-o", "reservation=2147483648", "tank/p/c")
	run("create", "-V", "1048576", "tank/p/vol")
	run("snapshot", "tank/p@s")
	vol, err := c.Get(ctx, "tank/p/vol", "refreservation")
	if err != nil {
		t.Fatal(err)
	}
	volResv, _ := vol.Props["refreservation"].Uint()
	for _, w := range []struct{ name, used, urefres string }{
		{"tank/p", strconv.FormatUint(1<<30+2<<30+volResv, 10), "1073741824"},
		{"tank/p/c", "0", "0"},
		{"tank/p/vol", strconv.FormatUint(volResv, 10), strconv.FormatUint(volResv, 10)},
		{"tank/p@s", "0", "-"},
		{"tank", strconv.FormatUint(1<<30+2<<30+volResv, 10), "0"},
	} {
		d, err := c.Get(ctx, w.name, "used", "usedbyrefreservation")
		if err != nil {
			t.Fatal(err)
		}
		if got := d.Props["used"]; got.Value != w.used || got.Source != "-" {
			t.Errorf("%s used %+v, want %s", w.name, got, w.used)
		}
		if got := d.Props["usedbyrefreservation"]; got.Value != w.urefres || got.Source != "-" {
			t.Errorf("%s usedbyrefreservation %+v, want %s", w.name, got, w.urefres)
		}
	}
	p, err := c.Get(ctx, "tank/p", "available", "usedbyrefreservation")
	if err != nil {
		t.Fatal(err)
	}
	run("create", "tank/p/new")
	n, err := c.Get(ctx, "tank/p/new", "available")
	if err != nil {
		t.Fatal(err)
	}
	pa, _ := p.Props["available"].Uint()
	pu, _ := p.Props["usedbyrefreservation"].Uint()
	if na, _ := n.Props["available"].Uint(); pa-pu != na {
		t.Errorf("parent available %d less usedbyrefreservation %d = %d, a new child's available %d", pa, pu, pa-pu, na)
	}
	if _, err := sim.Run(ctx, "zfs", "set", "usedbyrefreservation=0", "tank/p"); err == nil || !strings.Contains(err.Error(), "is readonly") {
		t.Errorf("set usedbyrefreservation: %v, want readonly", err)
	}
}

// changelist_gather records cn_shared from zfs_is_shared, which is false
// for an unmounted filesystem (it looks the mountpoint up in mnttab). A
// zfs.exports line left behind while NFS was down outlives an unmount,
// and a later sharenfs=off does not remove it: postfix unshares only
// cn_shared nodes.
func TestSimUnmountedIsNotShared(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sim := zfstest.New("tank")
	if _, err := sim.Run(ctx, "zfs", "create", "-o", "sharenfs=rw=10.0.0.0/24", "tank/fs"); err != nil {
		t.Fatal(err)
	}
	stale := sim.Exports()
	if !strings.Contains(stale, "10.0.0.0/24") {
		t.Fatalf("not shared at create: %q", stale)
	}
	sim.SetNFSDown(true)
	sim.Unmount("tank/fs") // the unshare cannot reach zfs.exports
	sim.SetNFSDown(false)
	if got := sim.Exports(); got != stale {
		t.Fatalf("unmount with NFS down rewrote zfs.exports: %q", got)
	}
	if _, err := sim.Run(ctx, "zfs", "set", "sharenfs=off", "tank/fs"); err != nil {
		t.Fatal(err)
	}
	if got := sim.Exports(); got != stale {
		t.Errorf("sharenfs=off on an unmounted filesystem unshared it: zfs.exports %q, want %q", got, stale)
	}
}

// A received space property is in force like a local one (the kernel's
// checks read the effective value, dsl_prop_get_int_ds): a thick volume
// received under a quota'd parent charges its refreservation to the
// parent's dd_used, so the quota cannot be set below it and a second
// thick volume of the same size does not fit.
func TestSimReceivedRefreservationIsCharged(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sim := zfstest.New("tank")
	sim.PoolSize = 10 << 30
	c := zfs.New(sim)
	run := func(args ...string) error {
		_, err := sim.Run(ctx, "zfs", args...)
		return err
	}
	for _, args := range [][]string{
		{"create", "-o", "quota=104857600", "tank/g"},
		{"create", "-V", "67108864", "tank/v"},
		{"snapshot", "tank/v@s"},
	} {
		if err := run(args...); err != nil {
			t.Fatal(args, err)
		}
	}
	if err := sim.SendRecv("tank/v@s", "tank/g/r"); err != nil {
		t.Fatal(err)
	}
	d, err := c.Get(ctx, "tank/g/r", "refreservation")
	if err != nil {
		t.Fatal(err)
	}
	if p := d.Props["refreservation"]; p.Source != "received" || p.Value == "0" {
		t.Fatalf("received refreservation %+v", p)
	}
	if err := run("create", "-V", "67108864", "tank/g/w"); err == nil || !strings.Contains(err.Error(), "out of space") {
		t.Errorf("second thick volume under the quota: %v", err)
	}
	if err := run("set", "quota=10485760", "tank/g"); err == nil || !strings.Contains(err.Error(), "less than current used") {
		t.Errorf("quota below the received refreservation: %v", err)
	}
}
