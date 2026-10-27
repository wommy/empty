package endpoint

// Real-nfsd replay (wave9 nfs-diff): set each corpus sharenfs value on a
// real dataset, read back /etc/exports.d/zfs.exports and /var/lib/nfs/etab,
// and diff them against the reference models (internal/nfsref) and the
// simulator. Runs in the real-ZFS CI VM as root (ZFS_TEST_POOL, nfsd
// threads > 0); skipped elsewhere. The report goes to $NFS_REPLAY_REPORT,
// default nfs-replay.log in the working directory (ci/real-zfs-guest.sh
// points it into the shard output it copies back, and ci/real-zfs-vm.sh
// puts it in the job summary). It is not parallel: the corpus writes
// malformed zfs.exports lines, which end exportfs's read of the file for
// every dataset, so the guest also keeps it in the TestFileExport* shard.
//
// It fails on what the endpoint relies on: a canonical (ShareNFS-shaped)
// value whose real lines differ from the reference or the simulator, and
// a verifier claim (serves, exposure) the reference mountd model refutes
// on the real etab. Every other difference is a reference-model
// ambiguity: reported, and fatal only with NFS_REPLAY_STRICT=1.

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/wommy/empty/internal/nfsref"
	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

// replayCase is one corpus entry: sharenfs on the volume, optionally on
// its parent first. {mp} is replaced by the volume's mountpoint.
type replayCase struct {
	name, value, parent string
}

var replayCorpus = []replayCase{
	{"off", "off", ""},
	{"on", "on", ""},
	{"bare rw", "rw", ""},
	{"bare ro", "ro", ""},
	{"one /32 rw", "rw=10.0.0.5/32", ""},
	{"one /32 ro", "ro=10.0.0.5/32", ""},
	{"one /16", "rw=10.1.0.0/16", ""},
	{"canonical mix", "rw=10.0.0.5/32:10.0.0.6/32,ro=10.1.0.0/16", ""},
	{"same net rw then ro", "rw=10.0.0.0/24,ro=10.0.0.0/24", ""},
	{"same net ro then rw", "ro=10.0.0.0/24,rw=10.0.0.0/24", ""},
	{"overlap same mode", "rw=10.0.0.0/24:10.0.0.5/32", ""},
	{"ro inside rw", "rw=10.0.0.0/16,ro=10.0.0.0/24", ""},
	{"host bits set", "rw=10.0.0.5/24", ""},
	{"prefix 024", "rw=10.0.0.0/024", ""},
	{"prefix +24", "rw=10.0.0.0/+24", ""},
	{"dotted mask", "rw=10.0.0.0/255.255.255.0", ""},
	{"prefix 33", "rw=10.0.0.0/33", ""},
	{"bare address", "rw=10.0.0.5", ""},
	{"address and /32", "rw=10.0.0.5:10.0.0.5/32", ""},
	{"at subnet", "rw=@10.0.0.0/8", ""},
	{"empty list", "rw=", ""},
	{"trailing colon", "rw=10.0.0.5/32:", ""},
	{"double colon", "rw=10.0.0.5/32::10.0.0.6/32", ""},
	{"star", "rw=*", ""},
	{"unresolvable name", "rw=node-a.invalid", ""},
	{"crossmnt", "rw=10.0.0.5/32,crossmnt", ""},
	{"no_root_squash", "rw=10.0.0.5/32,no_root_squash", ""},
	{"root_mapping", "ro=10.0.0.5/32,root_mapping=0", ""},
	{"sec krb5", "sec=krb5,rw=10.0.0.5/32", ""},
	{"sec switch", "rw=10.0.0.5/32,sec=krb5,ro=10.1.0.0/16", ""},
	{"sec null", "sec,rw=10.0.0.5/32", ""},
	{"hash host", "rw=#", ""},
	{"space in list", "rw=10.0.0.5/32 10.0.0.6/32", ""},
	{"quote", `rw="10.0.0.5/32`, ""},
	{"bad anonuid", "anonuid=x,rw=10.0.0.5/32", ""},
	{"no hosts", "sync", ""},
	{"unknown key", "bogus", ""},
	{"empty", "", ""},
	{"ipv6 literal", "rw=[::1]", ""},
	{"mountpoint option", "rw=10.0.0.5/32,mountpoint=/nonexistent", ""},
	{"parent on", "rw=10.0.0.5/32", "on"},
	{"parent subnet", "rw=10.0.0.5/32", "rw=@10.0.0.0/8"},
}

// replayHost is where the corpus is replayed: the real host or, to test
// the harness itself, a simulator.
type replayHost interface {
	set(name, value string) error // zfs set sharenfs=value name
	files() (nfsref.ExportsFiles, error)
	etab() (string, error)
}

type realHost struct{ ctx context.Context }

func (h realHost) set(name, v string) error {
	_, err := zfs.LocalRunner{}.Run(h.ctx, "zfs", "set", "sharenfs="+v, name)
	return err
}

func (realHost) files() (nfsref.ExportsFiles, error) { return readExports() }

func (realHost) etab() (string, error) {
	b, err := os.ReadFile("/var/lib/nfs/etab")
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return string(b), err
}

type simHost struct{ sim *zfstest.Sim }

func (h simHost) set(name, v string) error {
	_, err := h.sim.Run(context.Background(), "zfs", "set", "sharenfs="+v, name)
	return err
}

func (h simHost) files() (nfsref.ExportsFiles, error) {
	return nfsref.ExportsFiles{Dir: map[string][]byte{"zfs.exports": []byte(h.sim.Exports())}}, nil
}

func (h simHost) etab() (string, error) {
	b, err := h.sim.ReadEtab(context.Background())
	return string(b), err
}

// newReplaySim is a simulator holding parent and ds at mountpoints pmp and mp.
func newReplaySim(t *testing.T, parent, ds, mp, pmp string) *zfstest.Sim {
	ctx := context.Background()
	sim := zfstest.New(strings.SplitN(parent, "/", 2)[0])
	sz := zfs.New(sim)
	if err := sz.CreateFilesystem(ctx, parent, map[string]string{"mountpoint": pmp, "sharenfs": "off"}, true); err != nil {
		t.Fatal(err)
	}
	if err := sz.CreateFilesystem(ctx, ds, map[string]string{"mountpoint": mp}, false); err != nil {
		t.Fatal(err)
	}
	return sim
}

func TestNFSReplay(t *testing.T) {
	pool := os.Getenv("ZFS_TEST_POOL")
	if pool == "" {
		if os.Getenv("ZFS_REQUIRE_REAL") != "" {
			t.Fatal("ZFS_REQUIRE_REAL is set but ZFS_TEST_POOL is not")
		}
		t.Skip("needs a real pool (ZFS_TEST_POOL) and a running nfsd")
	}
	b, err := os.ReadFile("/proc/fs/nfsd/threads")
	if n, _ := strconv.Atoi(strings.TrimSpace(string(b))); err != nil || n == 0 {
		t.Skip("needs a running NFS server (nfsd threads > 0)")
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	// cleanups run after the test returns, last registered first: cancel
	// only once the destroy below has used ctx
	t.Cleanup(cancel)
	run := func(args ...string) error {
		_, err := zfs.LocalRunner{}.Run(ctx, "zfs", args...)
		return err
	}
	parent := fmt.Sprintf("%s/nfsreplay-%d", pool, time.Now().UnixNano())
	ds := parent + "/v"
	if err := run("create", "-o", "sharenfs=off", parent); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = run("set", "sharenfs=off", ds)
		_ = run("set", "sharenfs=off", parent)
		if err := run("destroy", "-r", parent); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	if err := run("create", ds); err != nil {
		t.Fatal(err)
	}
	z := zfs.New(zfs.LocalRunner{})
	get := func(name string) string {
		d, err := z.Get(ctx, name, "mountpoint")
		if err != nil {
			t.Fatal(err)
		}
		return d.Props["mountpoint"].Value
	}
	mp, pmp := get(ds), get(parent)
	sim := newReplaySim(t, parent, ds, mp, pmp)
	rep, hard, soft := replayRun(t, realHost{ctx}, sim, parent, ds, mp, pmp, start)
	elapsed := time.Since(start)
	out := os.Getenv("NFS_REPLAY_REPORT")
	if out == "" {
		out = "nfs-replay.log"
	}
	if err := os.WriteFile(out, []byte(rep), 0o644); err != nil {
		t.Errorf("report: %v", err)
	}
	t.Logf("report %s: %d failures, %d ambiguities, %v", out, hard, soft, elapsed)
	if hard > 0 || (soft > 0 && os.Getenv("NFS_REPLAY_STRICT") != "") {
		t.Errorf("%d replay failures, %d ambiguities; see %s:\n%s", hard, soft, out, rep)
	}
	if elapsed > 30*time.Second {
		t.Errorf("replay took %v, budget 30s", elapsed)
	}
}

// TestNFSReplayOnSim runs the harness with a simulator as the host, so
// the harness itself is tested in the unit job: the simulator is built on
// the reference models, so only the verifier checks can fail here.
func TestNFSReplayOnSim(t *testing.T) {
	t.Parallel()
	parent, ds := "tank/nfsreplay", "tank/nfsreplay/v"
	mp, pmp := "/tank/nfsreplay/v", "/tank/nfsreplay"
	host := simHost{newReplaySim(t, parent, ds, mp, pmp)}
	rep, hard, soft := replayRun(t, host, newReplaySim(t, parent, ds, mp, pmp), parent, ds, mp, pmp, time.Now())
	if hard > 0 || soft > 0 {
		t.Fatalf("%d failures, %d ambiguities:\n%s", hard, soft, rep)
	}
	if testing.Verbose() {
		t.Log(rep)
	}
}

// replayRun replays the corpus on host and diffs each step against the
// reference models and sim (replaying the same history).
func replayRun(t *testing.T, host replayHost, sim *zfstest.Sim, parent, ds, mp, pmp string, start time.Time) (string, int, int) {
	ctx := context.Background()
	rmp, rpmp := realpathOr(mp), realpathOr(pmp)
	var rep strings.Builder
	fmt.Fprintf(&rep, "# NFS replay: %s (mountpoint %s)\n\n", ds, mp)
	if v, err := os.ReadFile("/proc/fs/nfsd/export_features"); err == nil {
		fmt.Fprintf(&rep, "export_features: %s", v)
	}
	var hard, soft int
	diff := func(fatal bool, what, got, want string) {
		if got == want {
			return
		}
		tag := "AMBIGUITY"
		if fatal {
			tag, hard = "FAIL", hard+1
		} else {
			soft++
		}
		fmt.Fprintf(&rep, "  %s %s:\n    host: %q\n    want: %q\n", tag, what, got, want)
	}
	simSet := func(name, v string) error {
		_, err := sim.Run(ctx, "zfs", "set", "sharenfs="+v, name)
		return err
	}

	parentV := "off"
	for _, c := range replayCorpus {
		if time.Since(start) > 25*time.Second {
			t.Errorf("replay budget: stopped before %q after %v", c.name, time.Since(start))
			break
		}
		fmt.Fprintf(&rep, "\n## %s: sharenfs=%q", c.name, c.value)
		if c.parent != "" {
			fmt.Fprintf(&rep, " (parent %q)", c.parent)
		}
		rep.WriteString("\n")
		want := c.parent
		if want == "" {
			want = "off"
		}
		if want != parentV {
			if err := host.set(parent, want); err != nil {
				t.Fatalf("parent: %v", err)
			}
			if err := simSet(parent, want); err != nil {
				t.Fatalf("sim parent: %v", err)
			}
			parentV = want
		}
		hostErr := host.set(ds, c.value)
		simErr := simSet(ds, c.value)
		refErr := nfsref.ValidateSharenfs(c.value)
		cs, isCanon := canonicalShareNFS(c.value, DefaultNFSMinPrefix)
		canonical := isCanon && len(cs) > 0 && c.parent == ""
		diff(true, "zfs set accepted", fmt.Sprint(hostErr == nil), fmt.Sprint(refErr == nil))
		diff(true, "sim zfs set accepted", fmt.Sprint(simErr == nil), fmt.Sprint(hostErr == nil))
		if hostErr != nil {
			fmt.Fprintf(&rep, "  zfs set refused: %v\n", hostErr)
			continue
		}
		files, err := host.files()
		if err != nil {
			t.Fatal(err)
		}
		zfsExports := string(files.Dir["zfs.exports"])
		hostEtab, err := host.etab()
		if err != nil {
			t.Fatal(err)
		}

		// 1. libshare: our mountpoint's lines in zfs.exports
		var refLines []string
		if c.value != "off" {
			refLines, _ = nfsref.ExportsLines(mp, c.value)
		}
		emp := nfsref.EscapeMountpoint(mp)
		diff(canonical, "zfs.exports lines", strings.Join(linesOf(zfsExports, emp), ""), strings.Join(refLines, ""))
		diff(canonical, "sim zfs.exports lines", strings.Join(linesOf(sim.Exports(), emp), ""), strings.Join(linesOf(zfsExports, emp), ""))

		// 2. exportfs: the reference on the host's files, our paths' lines
		refEtab, refT := nfsref.ExportfsRA(files, nil, realpathOr)
		for _, p := range []string{rmp, rpmp} {
			diff(canonical, "etab lines of "+p, strings.Join(nfsref.EtabLinesOf(hostEtab, p), ""), strings.Join(nfsref.EtabLinesOf(refEtab, p), ""))
		}
		for _, d := range refT.Diags {
			fmt.Fprintf(&rep, "  ref exportfs: %s\n", d.Msg)
		}
		// 3. the simulator's etab for the same history
		simEtab, _ := sim.ReadEtab(ctx)
		for _, p := range []string{rmp, rpmp} {
			diff(canonical, "sim etab lines of "+p, strings.Join(nfsref.EtabLinesOf(string(simEtab), p), ""), strings.Join(nfsref.EtabLinesOf(hostEtab, p), ""))
		}

		// 4. our verifier on the host's etab, against mountd's model of it
		live := ParseEtab([]byte(hostEtab))
		m := nfsref.LoadEtab(hostEtab, nil)
		var hosts []string
		for _, x := range live[rmp] {
			hosts = append(hosts, x.Host+"("+x.etabMode()+")")
		}
		fmt.Fprintf(&rep, "  etab clients: %s\n", strings.Join(hosts, " "))
		for _, x := range cs {
			if !live.serves(rmp, x) {
				diff(canonical, "verifier serves "+x.Host+" "+x.mode(), "false", "true")
			}
		}
		if x := live.exposure(rmp, cs); len(x) > 0 {
			fmt.Fprintf(&rep, "  verifier exposure: %s\n", strings.Join(x, "; "))
		} else if len(cs) > 0 {
			if why := failOpen(m, rmp, cs); why != "" {
				diff(true, "verifier soundness (no exposure, but the mountd model)", why, "")
			}
		}
		env := nfsref.Env{}
		if _, ok := host.(realHost); ok {
			env.IsMountpoint = isMountpoint
		}
		var grants []string
		for _, a := range []string{"10.0.0.5", "10.0.0.6", "10.1.2.3", "10.9.9.9"} {
			grants = append(grants, a+"="+m.Access(rmp, mustAddr(a), env).Mode.String())
		}
		fmt.Fprintf(&rep, "  mountd model: %s\n", strings.Join(grants, " "))
	}
	fmt.Fprintf(&rep, "\n%d cases, %d failures, %d ambiguities, %v\n", len(replayCorpus), hard, soft, time.Since(start).Round(time.Millisecond))
	return rep.String(), hard, soft
}

// readExports reads /etc/exports and /etc/exports.d as exportfs does.
func readExports() (nfsref.ExportsFiles, error) {
	files := nfsref.ExportsFiles{Dir: map[string][]byte{}}
	if b, err := os.ReadFile("/etc/exports"); err == nil {
		files.Exports = b
	} else if !errors.Is(err, os.ErrNotExist) {
		return files, err
	}
	ents, err := os.ReadDir("/etc/exports.d")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return files, err
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/etc/exports.d", e.Name()))
		if err != nil {
			return files, err
		}
		files.Dir[e.Name()] = b
	}
	return files, nil
}

// linesOf returns the zfs.exports lines of an escaped mountpoint.
func linesOf(file, mp string) []string {
	var out []string
	for _, l := range strings.SplitAfter(file, "\n") {
		if i := strings.IndexAny(l, "\t \n"); i == len(mp) && strings.HasPrefix(l, mp) {
			out = append(out, l)
		}
	}
	return out
}

func realpathOr(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// isMountpoint is check_is_mountpoint: a different device from the parent.
func isMountpoint(p string) bool {
	var a, b os.FileInfo
	var err error
	if a, err = os.Lstat(p); err != nil {
		return false
	}
	if b, err = os.Lstat(filepath.Join(p, "..")); err != nil {
		return false
	}
	return !os.SameFile(a, b) && devOf(a) != devOf(b)
}

func devOf(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Dev)
	}
	return 0
}

func mustAddr(s string) netip.Addr { return netip.MustParseAddr(s) }
