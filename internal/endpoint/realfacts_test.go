package endpoint_test

// Raw OpenZFS facts the model and the simulator both believe. They run
// plain zfs commands, never the endpoint, one fact per spec row
// (the wave8 spec catalogue), so a belief shared by the model, the Sim
// and the planner is checked against something none of them wrote.
//
//	ZFS_TEST_POOL=tank ZFS_REQUIRE_REAL=1 go test -run TestRealFacts   # the oracle: must all pass
//	go test -run TestRealFacts                                          # Sim conformance
//
// On the Sim a fact may be listed as a known divergence (simDiverges): the
// Sim must then FAIL it, so a Sim fix shows up as a test to update, and a
// fact that is not modelled at all skips with its reason (ci/expected-skips).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

type factEnv struct {
	z      *zfs.Client
	p      string // a fresh parent per fact
	sim    *zfstest.Sim
	runner zfs.Runner
}

func (f *factEnv) zfs(args ...string) error {
	_, err := f.runner.Run(context.Background(), "zfs", args...)
	return err
}

func (f *factEnv) get(name, prop string) (string, error) {
	d, err := f.z.Get(context.Background(), name, prop)
	if err != nil {
		return "", err
	}
	return d.Props[prop].Value, nil
}

func (f *factEnv) source(name, prop string) string {
	d, err := f.z.Get(context.Background(), name, prop)
	if err != nil {
		return "error: " + err.Error()
	}
	return d.Props[prop].Source
}

func (f *factEnv) exists(name string) bool {
	ok, err := f.z.Exists(context.Background(), name)
	return err == nil && ok
}

// sendRecv is `zfs send -p snap | zfs recv dst`: on the Sim, its SendRecv.
func (f *factEnv) sendRecv(snap, dst string) error {
	if f.sim != nil {
		return f.sim.SendRecv(snap, dst)
	}
	_, err := f.runner.Run(context.Background(), "sh", "-c", `zfs send -p "$1" | zfs recv "$2"`, "sh", snap, dst)
	return err
}

func kindOf(err error) zfs.Kind {
	var e *zfs.Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return zfs.KindUnknown
}

type fact struct {
	row, what   string
	simDiverges string // the Sim is known to get this wrong (it must fail)
	simSkip     string // the fact needs something the Sim cannot do
	check       func(f *factEnv) error
}

var facts = []fact{
	{row: "OWN-09", what: "`zfs create -p` exits 0 on an existing dataset", check: func(f *factEnv) error {
		if err := f.z.CreateFilesystem(context.Background(), f.p+"/a", nil, false); err != nil {
			return err
		}
		return f.zfs("create", "-p", f.p+"/a")
	}},
	{row: "OWN-08", what: "clone onto an existing name fails as Exists", check: func(f *factEnv) error {
		f.zfs("create", f.p+"/a")
		f.zfs("snapshot", f.p+"/a@s")
		f.zfs("create", f.p+"/b")
		err := f.z.Clone(context.Background(), f.p+"/a@s", f.p+"/b", nil)
		if kindOf(err) != zfs.KindExists {
			return fmt.Errorf("got %v (kind %v), want exists", err, kindOf(err))
		}
		return nil
	}},
	{row: "SNP-06", what: "`destroy -d` of a cloned snapshot exits 0 and marks defer_destroy=on", check: func(f *factEnv) error {
		f.zfs("create", f.p+"/a")
		f.zfs("snapshot", f.p+"/a@s")
		f.zfs("clone", f.p+"/a@s", f.p+"/c")
		if err := f.zfs("destroy", "-d", f.p+"/a@s"); err != nil {
			return err
		}
		if v, _ := f.get(f.p+"/a@s", "defer_destroy"); v != "on" {
			return fmt.Errorf("defer_destroy=%q", v)
		}
		return nil
	}},
	{row: "DEL-05", what: "destroying the last clone reaps a deferred origin", check: func(f *factEnv) error {
		f.zfs("create", f.p+"/a")
		f.zfs("snapshot", f.p+"/a@s")
		f.zfs("clone", f.p+"/a@s", f.p+"/c")
		f.zfs("destroy", "-d", f.p+"/a@s")
		if err := f.zfs("destroy", f.p+"/c"); err != nil {
			return err
		}
		if f.exists(f.p + "/a@s") {
			return errors.New("origin survived its last clone")
		}
		return nil
	}},
	{row: "DEL-07", what: "a plain destroy of a held snapshot fails as Busy", check: func(f *factEnv) error {
		f.zfs("create", f.p+"/a")
		f.zfs("snapshot", f.p+"/a@s")
		if err := f.zfs("hold", "keep", f.p+"/a@s"); err != nil {
			return err
		}
		defer f.zfs("release", "keep", f.p+"/a@s")
		err := f.z.Destroy(context.Background(), f.p+"/a@s", zfs.DestroyOptions{})
		if kindOf(err) != zfs.KindBusy {
			return fmt.Errorf("got %v (kind %v), want busy", err, kindOf(err))
		}
		return nil
	}},
	{row: "DEL-05/holds", what: "`destroy -d` of a held snapshot defers; the last release reaps it", check: func(f *factEnv) error {
		f.zfs("create", f.p+"/a")
		f.zfs("snapshot", f.p+"/a@s")
		f.zfs("hold", "keep", f.p+"/a@s")
		if err := f.zfs("destroy", "-d", f.p+"/a@s"); err != nil {
			return err
		}
		if v, _ := f.get(f.p+"/a@s", "userrefs"); v != "1" {
			return fmt.Errorf("userrefs=%q", v)
		}
		if err := f.zfs("release", "keep", f.p+"/a@s"); err != nil {
			return err
		}
		if f.exists(f.p + "/a@s") {
			return errors.New("released deferred snapshot survived")
		}
		return nil
	}},
	{row: "DEL-06", what: "a plain destroy of a filesystem with a snapshot fails as HasChildren", check: func(f *factEnv) error {
		f.zfs("create", f.p+"/a")
		f.zfs("snapshot", f.p+"/a@s")
		err := f.z.Destroy(context.Background(), f.p+"/a", zfs.DestroyOptions{})
		if kindOf(err) != zfs.KindHasChildren || !f.exists(f.p+"/a@s") {
			return fmt.Errorf("got %v (kind %v), want has children and the snapshot kept", err, kindOf(err))
		}
		return nil
	}},
	{row: "OWN-12", what: "a plain destroy of a filesystem with a child dataset fails as HasChildren", check: func(f *factEnv) error {
		f.zfs("create", f.p+"/a")
		f.zfs("create", f.p+"/a/child")
		err := f.z.Destroy(context.Background(), f.p+"/a", zfs.DestroyOptions{})
		if kindOf(err) != zfs.KindHasChildren || !f.exists(f.p+"/a/child") {
			return fmt.Errorf("got %v (kind %v)", err, kindOf(err))
		}
		return nil
	}},
	{row: "OWN-12/zvol", what: "nothing can be created below a zvol", check: func(f *factEnv) error {
		f.zfs("create", "-s", "-V", "1048576", f.p+"/z")
		if err := f.zfs("create", f.p+"/z/child"); err == nil {
			return errors.New("created a dataset below a zvol")
		}
		return nil
	}},
	{row: "DEL-03", what: "a plain destroy of a snapshot with a clone fails as HasClones", check: func(f *factEnv) error {
		f.zfs("create", f.p+"/a")
		f.zfs("snapshot", f.p+"/a@s")
		f.zfs("clone", f.p+"/a@s", f.p+"/c")
		err := f.z.Destroy(context.Background(), f.p+"/a@s", zfs.DestroyOptions{})
		if kindOf(err) != zfs.KindHasClones {
			return fmt.Errorf("got %v (kind %v)", err, kindOf(err))
		}
		return nil
	}},
	// DeleteVolume reads a snapshot's dependents from this property instead
	// of scanning the whole parent for origins (ADR 0007)
	{row: "DEL-03/clones", what: "a snapshot's clones property names its clones wherever they live; none reads as `-` or empty",
		check: func(f *factEnv) error {
			f.zfs("create", f.p+"/a")
			f.zfs("create", f.p+"/o")
			f.zfs("snapshot", f.p+"/a@s")
			f.zfs("snapshot", f.p+"/a@t")
			if err := f.zfs("clone", f.p+"/a@s", f.p+"/o/x"); err != nil {
				return err
			}
			c, _ := f.get(f.p+"/a@s", "clones")
			none, _ := f.get(f.p+"/a@t", "clones")
			if c != f.p+"/o/x" || none != "-" && none != "" {
				return fmt.Errorf("clones %q, of an unused snapshot %q", c, none)
			}
			return nil
		}},
	{row: "OWN-02/rename", what: "rename keeps guid and local markers, and moves a clone's origin with it", check: func(f *factEnv) error {
		f.zfs("create", "-o", "zfs-endpoint:csi_volume_name=a", f.p+"/a")
		f.zfs("snapshot", f.p+"/a@s")
		f.zfs("clone", f.p+"/a@s", f.p+"/c")
		g1, _ := f.get(f.p+"/a", "guid")
		if err := f.zfs("rename", f.p+"/a", f.p+"/b"); err != nil {
			return err
		}
		g2, _ := f.get(f.p+"/b", "guid")
		o, _ := f.get(f.p+"/c", "origin")
		if g1 == "" || g1 != g2 || o != f.p+"/b@s" || f.source(f.p+"/b", "zfs-endpoint:csi_volume_name") != "local" {
			return fmt.Errorf("guid %s->%s origin %s source %s", g1, g2, o, f.source(f.p+"/b", "zfs-endpoint:csi_volume_name"))
		}
		return nil
	}},
	{row: "OWN-06", what: "`zfs send -R | zfs recv` delivers user properties with source `received`",
		simSkip: "the Sim has no send/recv: the harness fakes it with Sim.Receive, which is this very belief",
		check: func(f *factEnv) error {
			f.zfs("create", "-o", "zfs-endpoint:managed_resource=true", f.p+"/a")
			f.zfs("snapshot", f.p+"/a@s")
			if _, err := f.runner.Run(context.Background(), "sh", "-c", `zfs send -R "$1" | zfs recv "$2"`, "sh", f.p+"/a@s", f.p+"/b"); err != nil {
				return err
			}
			if src := f.source(f.p+"/b", "zfs-endpoint:managed_resource"); src != "received" {
				return fmt.Errorf("source %q", src)
			}
			return nil
		}},
	// libzfs_sendrecv.c: a -p stream carries each snapshot's properties
	// (snapprops) and zfs_receive_one sets them on the received snapshot
	// as received (ZFS_IOC_SET_PROP, zc_cookie = B_TRUE). The Sim's
	// SendRecv dropped them.
	{row: "OWN-06/snapshot", what: "`zfs send -p | zfs recv` keeps a snapshot's user properties (source `received`) and its guid",
		check: func(f *factEnv) error {
			const p = "zfs-endpoint:csi_snapshot_name"
			f.zfs("create", f.p+"/a")
			if err := f.zfs("snapshot", "-o", p+"=snap-1", f.p+"/a@s"); err != nil {
				return err
			}
			g1, _ := f.get(f.p+"/a@s", "guid")
			if err := f.sendRecv(f.p+"/a@s", f.p+"/b"); err != nil {
				return err
			}
			v, _ := f.get(f.p+"/b@s", p)
			g2, _ := f.get(f.p+"/b@s", "guid")
			if src := f.source(f.p+"/b@s", p); v != "snap-1" || src != "received" || g1 == "" || g1 != g2 {
				return fmt.Errorf("%s=%q (source %q), guid %s->%s", p, v, src, g1, g2)
			}
			return nil
		}},
	{row: "VOL-11", what: "a thick zvol's refreservation is at least its volsize; a sparse one's is 0", check: func(f *factEnv) error {
		f.zfs("create", "-V", "1048576", f.p+"/thick")
		f.zfs("create", "-s", "-V", "1048576", f.p+"/sparse")
		r, _ := f.get(f.p+"/thick", "refreservation")
		s, _ := f.get(f.p+"/sparse", "refreservation")
		if n, _ := strconv.ParseUint(r, 10, 64); n < 1048576 || s != "0" {
			return fmt.Errorf("thick %s sparse %s", r, s)
		}
		return nil
	}},
	{row: "SNP-11", what: "a zvol snapshot keeps the volsize it was cut at after the zvol grows",
		check: func(f *factEnv) error {
			f.zfs("create", "-s", "-V", "1048576", f.p+"/z")
			f.zfs("snapshot", f.p+"/z@s")
			f.zfs("set", "volsize=2097152", f.p+"/z")
			if v, _ := f.get(f.p+"/z@s", "volsize"); v != "1048576" {
				return fmt.Errorf("snapshot volsize %s", v)
			}
			return nil
		}},
	// wave8 believed the opposite (not atomic) without a pool to check it;
	// zfs_main batches a filesystem's snapshots into one lzc_destroy_snaps,
	// which destroys none when any is held (libzfs_core.c). The endpoint
	// never runs -r; the fact pins that a held snapshot leaves the tree whole.
	{row: "CR-03", what: "`destroy -r` stopped by a held snapshot destroys none of that filesystem's snapshots",
		check: func(f *factEnv) error {
			f.zfs("create", f.p+"/a")
			f.zfs("snapshot", f.p+"/a@s1")
			f.zfs("snapshot", f.p+"/a@s2")
			f.zfs("hold", "keep", f.p+"/a@s2")
			defer f.zfs("release", "keep", f.p+"/a@s2")
			if err := f.zfs("destroy", "-r", f.p+"/a"); err == nil {
				return errors.New("destroy -r of a held tree succeeded")
			}
			if !f.exists(f.p+"/a@s1") || !f.exists(f.p+"/a") {
				return errors.New("destroy -r of a held tree destroyed part of it")
			}
			return nil
		}},
	{row: "A3", what: "a snapshot marked for deferred destroy can still be cloned (the model allows re-cloning a deferred clone source)", check: func(f *factEnv) error {
		f.zfs("create", f.p+"/a")
		f.zfs("snapshot", f.p+"/a@s")
		f.zfs("hold", "keep", f.p+"/a@s")
		defer f.zfs("release", "keep", f.p+"/a@s")
		f.zfs("destroy", "-d", f.p+"/a@s")
		return f.zfs("clone", f.p+"/a@s", f.p+"/c")
	}},
	// space accounting (dsl_dir_space_available, dsl_dataset_space): sizes
	// in bytes, since the Sim does not parse suffixes; under a quota on
	// f.p so the other tests sharing the pool cannot move the numbers
	{row: "SPC-01", what: "a reservation leaves its own dataset's available unchanged; a child's refreservation inside it is charged once; a refreservation adds to its own dataset's available; refquota caps available",
		check: func(f *factEnv) error {
			g, p, c := f.p, f.p+"/p", f.p+"/p/c"
			if err := f.zfs("set", "quota=268435456", g); err != nil {
				return err
			}
			if err := f.zfs("create", "-o", "reservation=67108864", p); err != nil {
				return err
			}
			if err := availNear(f, p, 256<<20); err != nil {
				return fmt.Errorf("after the reservation: %w", err)
			}
			if err := f.zfs("create", "-o", "refreservation=33554432", c); err != nil {
				return err
			}
			if err := availNear(f, p, 224<<20); err != nil {
				return fmt.Errorf("after the child's refreservation: %w", err)
			}
			if err := availNear(f, c, 256<<20); err != nil {
				return fmt.Errorf("the child: %w", err)
			}
			if err := f.zfs("create", "-o", "refquota=16777216", g+"/d"); err != nil {
				return err
			}
			return availNear(f, g+"/d", 16<<20)
		}},
}

// availNear: name's available is want within 1 MiB (the real pool's
// metadata: referenced, unique; the Sim takes both as 0).
func availNear(f *factEnv, name string, want uint64) error {
	v, err := f.get(name, "available")
	if err != nil {
		return err
	}
	got, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return err
	}
	if d := int64(got) - int64(want); d > 1<<20 || d < -(1<<20) {
		return fmt.Errorf("%s available %d, want %d (within 1 MiB)", name, got, want)
	}
	return nil
}

func TestRealFacts(t *testing.T) {
	pool := os.Getenv("ZFS_TEST_POOL")
	if pool == "" && os.Getenv("ZFS_REQUIRE_REAL") != "" {
		t.Fatal("ZFS_REQUIRE_REAL is set but ZFS_TEST_POOL is not")
	}
	for i, fc := range facts {
		t.Run(fc.row, func(t *testing.T) {
			f := &factEnv{}
			if pool != "" {
				f.runner = zfs.LocalRunner{}
				f.p = fmt.Sprintf("%s/facts-%d-%d", pool, time.Now().UnixNano(), i)
			} else {
				if fc.simSkip != "" {
					t.Skip("Sim: " + fc.simSkip)
				}
				f.sim = zfstest.New("tank")
				f.runner = f.sim
				f.p = "tank/f"
			}
			f.z = zfs.New(f.runner)
			if err := f.z.CreateFilesystem(context.Background(), f.p, nil, true); err != nil {
				t.Fatal(err)
			}
			if pool != "" {
				t.Cleanup(func() { f.zfs("destroy", "-R", f.p) })
			}
			err := fc.check(f)
			switch {
			case pool == "" && fc.simDiverges != "":
				if err == nil {
					t.Errorf("%s: the Sim now gets this right (%s): drop simDiverges", fc.what, fc.simDiverges)
				}
			case err != nil:
				t.Errorf("%s: %v", fc.what, err)
			}
		})
	}
}
