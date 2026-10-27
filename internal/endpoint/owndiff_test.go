package endpoint_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/zfs"
)

// The ownership differential: the Table's ownership verdict for every row
// must agree with specOwn, a classifier written straight from the spec's
// OWN rows (wave8 spec.md), reading the raw `zfs get -j -p` document with
// encoding/json: no decoder, Row or Table code of the endpoint in it. It
// runs over hand-made shapes (the simulator's fixtures), every crash point
// of the crash table (the state a crash leaves, before the retry) and
// model-suite traces, after every step. A test only: it adds no code path.

// ownVerdict is one row's ownership: what the Table answers with
// OwnedVolume, ServedVolume, OwnedSnapshot and OwnedCloneSource.
type ownVerdict struct {
	Vol, Served, CSISnap, CloneSrc bool
	// CloneFor is the clone a clone-source snapshot is for.
	CloneFor string
}

type rawProp struct {
	Value  string `json:"value"`
	Source struct {
		Type string `json:"type"`
		Data string `json:"data"`
	} `json:"source"`
}

type rawDoc struct {
	Datasets map[string]struct {
		Name       string             `json:"name"`
		Type       string             `json:"type"`
		Properties map[string]rawProp `json:"properties"`
	} `json:"datasets"`
}

// specOwn classifies one dataset under parent, given its type and
// properties as zfs printed them. inScope is false for the parent and for
// anything deeper than a volume, which is never the endpoint's (OWN-12).
func specOwn(parent, prefix, name, typ string, props map[string]rawProp) (id string, v ownVerdict, inScope bool) {
	rel, ok := strings.CutPrefix(name, parent+"/")
	if !ok {
		return "", v, false
	}
	vol, snap, isSnap := strings.Cut(rel, "@")
	if strings.Contains(vol, "/") {
		return rel, v, false // OWN-12: a child dataset below a volume
	}
	// OWN-01: ownership is read only from user properties whose source is
	// exactly local; received, inherited, default, temporary and none
	// never confer it
	local := func(p string) string {
		if pr, ok := props[prefix+":"+p]; ok && pr.Source.Type == "LOCAL" {
			return pr.Value
		}
		return ""
	}
	managed := local("managed_resource") == "true"
	if !isSnap {
		if typ != "FILESYSTEM" && typ != "VOLUME" {
			return rel, v, true
		}
		// OWN-02: managed AND csi_volume_name equal to its id, both local;
		// OWN-03: managed without a name is Invalid, never owned
		v.Vol = managed && local("csi_volume_name") == rel
		// VOL-09: served only once committed
		v.Served = v.Vol && local("provision_success") == "true"
		return rel, v, true
	}
	// OWN-04: managed, csi_snapshot_name set, and the source volume marker
	// empty or its own volume, all local
	src := local("csi_snapshot_source_volume_id")
	v.CSISnap = managed && local("csi_snapshot_name") != "" && (src == "" || src == vol)
	// OWN-13, OWN-14: a clone source by its stamp, managed plus
	// clone_source_for=<clone>, on the reserved name for that clone. The
	// spec rows overlap where a snapshot carries both a CSI name and a clone
	// stamp; OWN-04 wins there (the endpoint never cuts such a snapshot:
	// CreateSnapshot refuses the reserved prefix), see the step-0 report.
	clone, reserved := strings.CutPrefix(snap, "volume-source-for-volume-")
	if reserved && clone != "" && managed && local("clone_source_for") == clone && local("csi_snapshot_name") == "" {
		v.CloneSrc, v.CloneFor = true, clone
	}
	return rel, v, true
}

// headOwn is the Table's verdict for a row.
func headOwn(r endpoint.Row) ownVerdict {
	clone, cs := r.OwnedCloneSource()
	if !cs {
		clone = ""
	}
	return ownVerdict{Vol: r.OwnedVolume(), Served: r.ServedVolume(), CSISnap: r.OwnedSnapshot(), CloneSrc: cs, CloneFor: clone}
}

// ownDiff scans parent with ep and, separately, with a raw `zfs get -j -p`
// through run, and returns the rows compared and every disagreement.
func ownDiff(ctx context.Context, ep *endpoint.Endpoint, run zfs.Runner, parent, prefix string) (int, []string, error) {
	tbl, err := ep.ScanWhole(ctx)
	if err != nil {
		return 0, nil, fmt.Errorf("scan: %w", err)
	}
	props := []string{"type"}
	for _, p := range []string{"managed_resource", "provision_success", "csi_volume_name", "csi_snapshot_name",
		"csi_snapshot_source_volume_id", "clone_source_for"} {
		props = append(props, prefix+":"+p)
	}
	out, err := run.Run(ctx, "zfs", "get", "-j", "-p", "-t", "filesystem,volume,snapshot", "-r", strings.Join(props, ","), parent)
	if err != nil {
		return 0, nil, fmt.Errorf("raw get: %w", err)
	}
	var doc rawDoc
	if err := json.Unmarshal(out, &doc); err != nil {
		return 0, nil, fmt.Errorf("raw get: %w", err)
	}
	var diffs []string
	seen := map[string]bool{}
	n := 0
	for _, name := range slices.Sorted(func(yield func(string) bool) {
		for k := range doc.Datasets {
			if !yield(k) {
				return
			}
		}
	}) {
		d := doc.Datasets[name]
		id, want, in := specOwn(parent, prefix, name, d.Type, d.Properties)
		row, ok := tbl.Get(id)
		switch {
		case !in && id != "" && ok:
			diffs = append(diffs, fmt.Sprintf("%s: out of scope (OWN-12), but the Table has it: %+v", id, headOwn(row)))
		case !in:
		case !ok:
			diffs = append(diffs, fmt.Sprintf("%s: missing from the Table; spec %+v", id, want))
		default:
			n++
			seen[id] = true
			if got := headOwn(row); got != want {
				diffs = append(diffs, fmt.Sprintf("%s: Table %+v, spec %+v", id, got, want))
			}
		}
	}
	for r := range tbl.All() {
		if !seen[r.ID] {
			diffs = append(diffs, fmt.Sprintf("%s: in the Table, not in the raw scan", r.ID))
		}
	}
	return n, diffs, nil
}

func checkOwn(t *testing.T, ctx context.Context, ep *endpoint.Endpoint, run zfs.Runner, parent, prefix, where string) int {
	t.Helper()
	n, diffs, err := ownDiff(ctx, ep, run, parent, prefix)
	if err != nil {
		t.Fatalf("%s: %v", where, err)
	}
	for _, d := range diffs {
		t.Errorf("%s: %s", where, d)
	}
	return n
}

// TestOwnershipDifferentialFixtures: every ownership shape the spec names,
// made by hand on the simulator, under the default and the adoption
// prefix (OWN-17).
func TestOwnershipDifferentialFixtures(t *testing.T) {
	t.Parallel()
	requireSim(t)
	ctx := context.Background()
	ep, h := setup(t)
	sim := h.Sim(t)
	const P = endpoint.DefaultPropertyPrefix + ":"
	q := h.Parent + "/"
	adm := func(args ...string) {
		t.Helper()
		if _, err := sim.Run(ctx, "zfs", args...); err != nil {
			t.Fatalf("zfs %v: %v", args, err)
		}
	}
	for _, r := range []endpoint.CreateVolumeRequest{
		{Name: "a", CapacityBytes: 16 * cmib},
		{Name: "b", Kind: endpoint.Block, CapacityBytes: 16 * cmib, Sparse: true},
	} {
		if _, err := ep.CreateVolume(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ep.CreateSnapshot(ctx, "s", "a"); err != nil {
		t.Fatal(err)
	}
	for _, r := range []endpoint.CreateVolumeRequest{
		{Name: "c", SourceVolumeID: "a"},     // a@volume-source-for-volume-c, stamped
		{Name: "r", SourceSnapshotID: "a@s"}, // a restore
	} {
		if _, err := ep.CreateVolume(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	adm("create", q+"foreign")                                                                                               // OWN-05
	adm("create", "-o", P+"managed_resource=true", q+"half")                                                                 // OWN-03
	adm("create", "-o", P+"managed_resource=true", "-o", P+"csi_volume_name=uncommitted", q+"uncommitted")                   // VOL-09
	adm("create", "-o", P+"managed_resource=true", "-o", P+"csi_volume_name=a", "-o", P+"provision_success=true", q+"alias") // OWN-02
	adm("create", "-o", P+"managed_resource=true", "-o", P+"csi_volume_name=-", q+"dash")
	adm("create", "-o", P+"managed_resource=TRUE", "-o", P+"csi_volume_name=upper", q+"upper")
	adm("create", "-o", "democratic-csi:managed_resource=true", "-o", "democratic-csi:csi_volume_name=demo",
		"-o", "democratic-csi:provision_success=true", q+"demo") // OWN-17
	adm("create", "-o", P+"managed_resource=true", "-o", P+"csi_volume_name=a/child", q+"a/child") // OWN-12
	adm("create", q+"recv")                                                                        // OWN-06, OWN-01
	for k, v := range map[string]string{"managed_resource": "true", "csi_volume_name": "recv", "provision_success": "true"} {
		sim.Receive(q+"recv", P+k, v)
	}
	if err := sim.SendRecv(q+"a@s", q+"copy"); err != nil { // OWN-06
		t.Fatal(err)
	}
	for _, s := range [][]string{
		{"a@admin"},                      // OWN-10
		{"a@volume-source-for-volume-x"}, // OWN-13: the name alone
		{"a@volume-source-for-volume-y", P + "managed_resource=true"},                               // half a stamp
		{"a@volume-source-for-volume-z", P + "managed_resource=true", P + "clone_source_for=other"}, // stamped for another
		{"a@volume-source-for-volume-w", P + "managed_resource=true", P + "clone_source_for=w", P + "csi_snapshot_name=w"},
		{"a@volume-source-for-volume-", P + "managed_resource=true", P + "clone_source_for="},
		{"a@other", P + "managed_resource=true", P + "csi_snapshot_name=other", P + "csi_snapshot_source_volume_id=b"}, // OWN-04
		{"a@nosrc", P + "managed_resource=true", P + "csi_snapshot_name=nosrc"},
		{"a@noname", P + "managed_resource=true"},
		{"foreign@fs", P + "managed_resource=true", P + "csi_snapshot_name=fs", P + "csi_snapshot_source_volume_id=foreign"},
		{"foreign@volume-source-for-volume-q", P + "managed_resource=true", P + "clone_source_for=q"},
		// OWN-04 does not ask the volume to be owned: a CSI-marked snapshot
		// with no source marker on a foreign volume is a CSI snapshot to
		// both (the core design downgrades it; see the step-0 report)
		{"alias@legacy", P + "managed_resource=true", P + "csi_snapshot_name=legacy"},
		{"half@legacy", P + "managed_resource=true", P + "csi_snapshot_name=legacy2"},
	} {
		args := []string{"snapshot"}
		for _, o := range s[1:] {
			args = append(args, "-o", o)
		}
		adm(append(args, q+s[0])...)
	}
	run := h.Z.Runner
	n := checkOwn(t, ctx, ep, run, h.Parent, endpoint.DefaultPropertyPrefix, "default prefix")
	n += checkOwn(t, ctx, h.endpoint(t, "democratic-csi", nil), run, h.Parent, "democratic-csi", "adoption prefix")
	// OWN-01: markers inherited from the parent own nothing
	adm("set", P+"managed_resource=true", h.Parent)
	adm("set", P+"csi_volume_name=inh", h.Parent)
	adm("set", P+"provision_success=true", h.Parent)
	adm("create", q+"inh")
	n += checkOwn(t, ctx, ep, run, h.Parent, endpoint.DefaultPropertyPrefix, "inherited markers")
	t.Logf("%d rows compared", n)
}

// TestOwnershipDifferentialCrashes: the state every crash point of the
// crash table leaves, before the retry: half-made volumes, half-stamped
// clone sources, released origins.
func TestOwnershipDifferentialCrashes(t *testing.T) {
	t.Parallel()
	requireSim(t)
	for _, c := range crashCases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			rows := 0
			for k := 0; ; k++ {
				reached := false
				for _, after := range []bool{false, true} {
					ep0, h := setupNFS(t, "192.0.2.10") // as crashEndpoint
					if c.nfs {
						h.NFS(t)
					}
					if c.setup != nil {
						if err := c.setup(ctx, ep0, h); err != nil {
							t.Fatalf("setup: %v", err)
						}
					}
					fr := &faultRunner{Runner: h.Z.Runner, crashAt: k, after: after, dead: true}
					_ = c.op(ctx, crashEndpoint(t, h, fr, c.late)) // a crash is the point
					reached = fr.crashed
					if !reached {
						break
					}
					rows += checkOwn(t, ctx, crashEndpoint(t, h, h.Z.Runner, c.late), h.Z.Runner, h.Parent,
						endpoint.DefaultPropertyPrefix, fmt.Sprintf("k%d after=%v", k, after))
				}
				if !reached {
					break
				}
			}
			if rows == 0 {
				t.Fatal("no crash point compared a row")
			}
			t.Logf("%d rows compared", rows)
		})
	}
}

// TestOwnershipDifferentialModel: model-suite traces (CSI calls, admin
// snapshots, clones, receives, renames, holds, faults), compared after
// every step.
func TestOwnershipDifferentialModel(t *testing.T) {
	t.Parallel()
	requireSim(t)
	const seqs, steps = 8, 30
	for seed := range uint64(seqs) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			g := newSimRig(t)
			r := &runner{g: g, m: newModel(), protected: map[string]bool{}, masks: map[string]int{}}
			trace := genTrace(rngSource{rand.New(rand.NewPCG(seed+1, 0x0e5d))}, steps)
			rows := 0
			for i, st := range trace {
				if f := r.step(ctx, i, st); f != nil {
					// the model suite's own finding (TestModel reports it);
					// the pool is still a state to compare
					t.Logf("model divergence at step %d: %v", i, f)
				}
				rows += checkOwn(t, ctx, g.ep, g.admin.Runner, g.parent, endpoint.DefaultPropertyPrefix, fmt.Sprintf("step %d %s", i, st))
			}
			t.Logf("%d rows compared", rows)
		})
	}
}
