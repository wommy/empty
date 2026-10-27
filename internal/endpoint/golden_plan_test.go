package endpoint

import (
	_ "embed"
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wommy/empty/internal/zfs"
)

// Golden plan tests: each row is (Table fixture, request) and its expected
// output is the plan's text in testdata/plans.golden, so an invariant is a
// row and a behaviour change is a reviewable diff of that file:
//
//	go test ./internal/endpoint -run TestGoldenPlans -update
//
// Every row is also planned three times and must render identically: map
// iteration order must never reach a plan.

var update = flag.Bool("update", false, "rewrite testdata/*.golden")

var codeNames = map[Code]string{
	OK: "OK", Aborted: "Aborted", AlreadyExists: "AlreadyExists", DeadlineExceeded: "DeadlineExceeded",
	FailedPrecondition: "FailedPrecondition", Internal: "Internal", InvalidArgument: "InvalidArgument",
	NotFound: "NotFound", OutOfRange: "OutOfRange", ResourceExhausted: "ResourceExhausted",
	Unavailable: "Unavailable", Unimplemented: "Unimplemented",
}

type goldenRow struct {
	name string
	rows []Row
	plan func(Planner, Table) (Plan, error)
}

func create(r CreateVolumeRequest) func(Planner, Table) (Plan, error) {
	return func(p Planner, t Table) (Plan, error) { return p.PlanCreateVolume(t, r) }
}

func del(id string) func(Planner, Table) (Plan, error) {
	return func(p Planner, t Table) (Plan, error) { return p.PlanDeleteVolume(t, id) }
}

func expand(id string, c, l uint64) func(Planner, Table) (Plan, error) {
	return func(p Planner, t Table) (Plan, error) {
		plan, _, err := p.PlanExpandVolume(t, id, c, l)
		return plan, err
	}
}

func snapshot(name, vol string) func(Planner, Table) (Plan, error) {
	return func(p Planner, t Table) (Plan, error) {
		plan, _, err := p.PlanCreateSnapshot(t, name, vol)
		return plan, err
	}
}

func delSnap(id string) func(Planner, Table) (Plan, error) {
	return func(p Planner, t Table) (Plan, error) { return p.PlanDeleteSnapshot(t, id) }
}

func publish(id, live string, c NFSClient) func(Planner, Table) (Plan, error) {
	return func(p Planner, t Table) (Plan, error) {
		plan, _, err := p.PlanPublishExport(t, ParseEtab([]byte(live)), id, c)
		return plan, err
	}
}

func unpublish(id, live, host string) func(Planner, Table) (Plan, error) {
	return func(p Planner, t Table) (Plan, error) {
		plan, _, err := p.PlanUnpublishExport(t, ParseEtab([]byte(live)), id, host)
		return plan, err
	}
}

func reconcile(ids ...string) func(Planner, Table) (Plan, error) {
	return func(p Planner, t Table) (Plan, error) {
		d := Desired{VolumeIDs: ids, MinAge: time.Hour, Now: time.Unix(recNow, 0)}
		rp, err := p.PlanReconcile(t, d)
		return rp.Plan(), err
	}
}

var (
	zvol     = func(size uint64) func(*Row) { return func(r *Row) { r.Type, r.Volsize = zfs.TypeVolume, size } }
	internal = func(r *Row) { r.Managed, r.SnapshotName = false, "" }
	cloneOf  = func(o string) func(*Row) { return func(r *Row) { r.Origin = "tank/v/" + o } }
)

var goldenRows = []goldenRow{
	// create: what each request shape plans on an empty parent
	{"create/fs", nil, create(CreateVolumeRequest{Name: "a", CapacityBytes: 1 << 30})},
	{"create/fs-unlimited", nil, create(CreateVolumeRequest{Name: "a"})},
	{"create/fs-limit-only", nil, create(CreateVolumeRequest{Name: "a", LimitBytes: 1 << 20})},
	{"create/fs-reserve", nil, create(CreateVolumeRequest{Name: "a", CapacityBytes: 1 << 30, Reserve: true})},
	{"create/fs-props", nil, create(CreateVolumeRequest{Name: "a", Properties: map[string]string{"compression": "gzip-6", "recordsize": "16K", "atime": "off"}})},
	{"create/fs-props-xattr-on", nil, create(CreateVolumeRequest{Name: "a", Properties: map[string]string{"xattr": "on"}})},
	{"create/zvol-thick", nil, create(CreateVolumeRequest{Name: "z", Kind: Block, CapacityBytes: 1<<30 + 1})},
	{"create/zvol-sparse", nil, create(CreateVolumeRequest{Name: "z", Kind: Block, CapacityBytes: 1 << 30, Sparse: true})},
	{"create/zvol-volblocksize", nil, create(CreateVolumeRequest{Name: "z", Kind: Block, CapacityBytes: 1<<20 + 1, Properties: map[string]string{"volblocksize": "4K"}})},
	{"create/denied-prop", nil, create(CreateVolumeRequest{Name: "a", Properties: map[string]string{"mountpoint": "/etc"}})},
	{"create/bad-name", nil, create(CreateVolumeRequest{Name: "a/b"})},
	// create against existing rows: idempotency, convergence, refusal
	{"create/satisfied", []Row{vol("a", func(r *Row) { r.Refquota = 1 << 30 })}, create(CreateVolumeRequest{Name: "a", CapacityBytes: 1 << 30})},
	{"create/foreign", []Row{vol("a", func(r *Row) { r.Managed = false })}, create(CreateVolumeRequest{Name: "a"})},
	{"create/incompatible-size", []Row{vol("a", func(r *Row) { r.Refquota = 10 })}, create(CreateVolumeRequest{Name: "a", CapacityBytes: 11})},
	{"create/incompatible-kind", []Row{vol("a", func(r *Row) { r.Refquota = 10 })}, create(CreateVolumeRequest{Name: "a", Kind: Block, CapacityBytes: 1})},
	{"create/thick-on-sparse-zvol", []Row{vol("z", zvol(16384))}, create(CreateVolumeRequest{Name: "z", Kind: Block, CapacityBytes: 16384})},
	{"create/uncommitted-resume", []Row{vol("a", func(r *Row) { r.Committed = false })}, create(CreateVolumeRequest{Name: "a", CapacityBytes: 1 << 30})},
	{"create/uncommitted-clone-converges", []Row{
		vol("src", func(r *Row) { r.Type, r.Volsize, r.Refres = zfs.TypeVolume, 1<<30, 1<<30 }),
		snap("src@s", func(r *Row) { r.Volsize = 1 << 30 }),
		vol("dst", func(r *Row) {
			r.Type, r.Volsize, r.Committed = zfs.TypeVolume, 1<<30, false
			r.SourceType, r.SourceID = "snapshot", "src@s"
		}),
	}, create(CreateVolumeRequest{Name: "dst", Kind: Block, CapacityBytes: 2 << 30, SourceSnapshotID: "src@s"})},
	// content sources
	{"restore/fs-grow", []Row{vol("src", func(r *Row) { r.Refquota = 1 << 20 }), snap("src@s", nil)},
		create(CreateVolumeRequest{Name: "r", CapacityBytes: 2 << 20, SourceSnapshotID: "src@s"})},
	{"restore/zvol-grow", []Row{vol("src", zvol(1<<20)), snap("src@s", func(r *Row) { r.Volsize = 1 << 20 })},
		create(CreateVolumeRequest{Name: "r", Kind: Block, CapacityBytes: 2 << 20, SourceSnapshotID: "src@s"})},
	{"restore/larger-than-limit", []Row{vol("src", zvol(10<<20)), snap("src@s", func(r *Row) { r.Volsize = 10 << 20 })},
		create(CreateVolumeRequest{Name: "r", Kind: Block, CapacityBytes: 4 << 20, LimitBytes: 5 << 20, SourceSnapshotID: "src@s"})},
	{"restore/unknown-source", []Row{vol("src", nil)}, create(CreateVolumeRequest{Name: "r", SourceSnapshotID: "src@nope"})},
	{"restore/malformed-source", nil, create(CreateVolumeRequest{Name: "r", SourceSnapshotID: "non-existing-snapshot-id"})},
	{"clone/malformed-source", nil, create(CreateVolumeRequest{Name: "c", CapacityBytes: 1, SourceVolumeID: "no/such@volume"})},
	{"clone/from-volume-reserve", []Row{vol("src", func(r *Row) { r.Refquota = 1 << 20 })},
		create(CreateVolumeRequest{Name: "c", SourceVolumeID: "src", Reserve: true})},
	{"clone/from-uncommitted-volume", []Row{vol("src", func(r *Row) { r.Committed = false })},
		create(CreateVolumeRequest{Name: "c", SourceVolumeID: "src"})},
	// delete
	{"delete/plain", []Row{vol("a", nil)}, del("a")},
	{"delete/missing", nil, del("a")},
	{"delete/has-csi-snapshot", []Row{vol("a", nil), snap("a@s", nil)}, del("a")},
	{"delete/has-deferred-snapshot", []Row{vol("a", nil), snap("a@s", func(r *Row) { r.Deferred = true })}, del("a")},
	{"delete/clone-releases-source", []Row{vol("b", nil), snap("b@volume-source-for-volume-c", internal), vol("c", cloneOf("b@volume-source-for-volume-c"))}, del("c")},
	{"delete/cloned-source", []Row{vol("b", nil), snap("b@volume-source-for-volume-c", internal), vol("c", cloneOf("b@volume-source-for-volume-c"))}, del("b")},
	{"delete/unmanaged", []Row{vol("a", func(r *Row) { r.Managed = false })}, del("a")},
	// expand
	{"expand/zvol", []Row{vol("z", func(r *Row) { r.Type, r.Volsize, r.BlockSz = zfs.TypeVolume, 1<<20, 128<<10 })}, expand("z", 1<<20+1, 0)},
	{"expand/zvol-shrink-noop", []Row{vol("z", zvol(2<<20))}, expand("z", 1<<20, 0)},
	{"expand/fs-quota", []Row{vol("a", func(r *Row) { r.Refquota = 1 << 20 })}, expand("a", 2<<20, 0)},
	{"expand/fs-unlimited", []Row{vol("a", nil)}, expand("a", 1<<30, 0)},
	{"expand/missing", nil, expand("a", 1, 0)},
	// snapshots
	{"snapshot/new", []Row{vol("a", nil)}, snapshot("s", "a")},
	{"snapshot/exists", []Row{vol("a", nil), snap("a@s", nil)}, snapshot("s", "a")},
	{"snapshot/name-taken-elsewhere", []Row{vol("a", nil), vol("b", nil), snap("b@s", nil)}, snapshot("s", "a")},
	{"snapshot/reserved-prefix", []Row{vol("a", nil)}, snapshot("volume-source-for-volume-x", "a")},
	{"snapshot/long-name", []Row{vol("a", nil)}, snapshot(strings.Repeat("n", 128), "a")},
	{"delete-snapshot/plain", []Row{vol("a", nil), snap("a@s", nil)}, delSnap("a@s")},
	{"delete-snapshot/foreign", []Row{vol("a", nil), snap("a@admin", internal)}, delSnap("a@admin")},
	// file export
	{"publish/first", []Row{fsv("a", nil)}, publish("a", "", NFSClient{Host: "10.0.0.1/32"})},
	{"publish/second-ro", []Row{exported("a", "10.0.0.1/32=rw", "rw=10.0.0.1/32")}, publish("a", "/tank/v/a\t10.0.0.1/32(rw,sync)\n", NFSClient{Host: "10.9.0.0/16", ReadOnly: true})},
	{"publish/zvol", []Row{vol("z", zvol(1<<20))}, publish("z", "", NFSClient{Host: "10.0.0.1/32"})},
	{"unpublish/last", []Row{exported("a", "10.0.0.1/32=rw", "rw=10.0.0.1/32")}, unpublish("a", "/tank/v/a\t10.0.0.1/32(rw,sync)\n", "10.0.0.1/32")},
	// reconcile (destroys only, in dependency order)
	{"reconcile/undesired-and-chain", []Row{
		aged(vol("keep", nil)), aged(vol("drop", nil)), aged(vol("b", nil)),
		aged(snap("b@volume-source-for-volume-c", internal)), aged(vol("c", cloneOf("b@volume-source-for-volume-c"))),
	}, reconcile("keep")},
	{"reconcile/empty-desired-set", []Row{aged(vol("a", nil)), aged(vol("half", func(r *Row) { r.Committed = false }))}, reconcile()},
}

func renderGolden(r goldenRow) string {
	plan, err := r.plan(pl, NewTable(append([]Row(nil), r.rows...)))
	if err != nil {
		return fmt.Sprintf("error: %s\n", codeNames[CodeOf(err)])
	}
	if len(plan) == 0 {
		return "(empty)\n"
	}
	return plan.String()
}

// goldenPlans is embedded: the real-ZFS leg runs a prebuilt test binary
// with no source tree beside it.
//
//go:embed testdata/plans.golden
var goldenPlans string

func TestGoldenPlans(t *testing.T) {
	var b strings.Builder
	seen := map[string]bool{}
	for _, r := range goldenRows {
		if seen[r.name] {
			t.Fatalf("duplicate row %s", r.name)
		}
		seen[r.name] = true
		out := renderGolden(r)
		for range 2 {
			if again := renderGolden(r); again != out {
				t.Fatalf("%s: plan is not deterministic:\n%s---\n%s", r.name, out, again)
			}
		}
		fmt.Fprintf(&b, "## %s\n%s\n", r.name, out)
	}
	const path = "testdata/plans.golden"
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if got := b.String(); got != goldenPlans {
		t.Errorf("plans differ from %s (review, then -update):\n%s", path, lineDiff(goldenPlans, got))
	}
}

// lineDiff names the rows whose text changed, with both versions.
func lineDiff(want, got string) string {
	split := func(s string) map[string]string {
		m := map[string]string{}
		for _, sec := range strings.Split(s, "## ")[1:] {
			name, body, _ := strings.Cut(sec, "\n")
			m[name] = body
		}
		return m
	}
	w, g := split(want), split(got)
	var b strings.Builder
	for _, r := range goldenRows {
		if w[r.name] != g[r.name] {
			fmt.Fprintf(&b, "## %s\n- %s+ %s", r.name, strings.ReplaceAll(w[r.name], "\n", "\n- "), strings.ReplaceAll(g[r.name], "\n", "\n+ "))
		}
	}
	return b.String()
}
