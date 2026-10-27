package zfsdiff

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testPool = "zfsep-test"
const testRoot = testPool + "/zd/s0"

// diffModelSim runs seq on a fresh model and a fresh simulator.
func diffModelSim(seq Seq) *Divergence {
	ctx := context.Background()
	a := Exec(ctx, NewModelRunner(testPool), testRoot, seq)
	b := Exec(ctx, NewSimRunner(testPool), testRoot, seq)
	return Compare(a, b)
}

// Known are model-vs-simulator divergences triaged as ambiguous: the
// simulator keeps its behaviour until the real-pool replay settles them.
// Each names the corpus file that does.
var Known = []struct {
	ID, Corpus string
	Match      func(d *Divergence) bool
}{
	{"create-vol-resv-auto", "create-vol-resv-auto.txt", func(d *Divergence) bool {
		return verb(d.Cmd) == "create" && strings.Contains(d.Cmd, "refreservation=auto")
	}},
}

func known(d *Divergence) string {
	for _, k := range Known {
		if k.Match(d) {
			return k.ID
		}
	}
	return ""
}

// diffSkippingKnown drops steps with a known divergence and compares
// again, so one ambiguity does not end the sequence.
func diffSkippingKnown(seq Seq) (*Divergence, Seq, []string) {
	var ids []string
	for {
		d := diffModelSim(seq)
		if d == nil || d.Step == 0 {
			return d, seq, ids
		}
		id := known(d)
		if id == "" {
			return d, seq, ids
		}
		ids = append(ids, id)
		seq.Steps = slices.Delete(slices.Clone(seq.Steps), d.Step-1, d.Step)
	}
}

// TestRandomSurvey generates $ZFSDIFF_SEEDS sequences ($ZFSDIFF_STEPS
// steps, default 14),
// minimises one divergence per signature and logs them; with
// ZFSDIFF_OUT it writes the minimised sequences there.
func TestRandomSurvey(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("ZFSDIFF_SEEDS"))
	if n == 0 {
		return // a development survey; TestRandomModelVsSim is the regression test
	}
	if drop := os.Getenv("ZFSDIFF_DROP"); drop != "" {
		saved := Projection
		defer func() { Projection = saved }()
		var keep []string
		for _, p := range Projection {
			if !strings.Contains(","+drop+",", ","+p+",") {
				keep = append(keep, p)
			}
		}
		Projection = keep
	}
	type found struct {
		seq   Seq
		d     *Divergence
		count int
	}
	bySig := map[string]*found{}
	knownCount := map[string]int{}
	ok, total := map[string]int{}, map[string]int{}
	stats := func(seq Seq) {
		for _, o := range Exec(context.Background(), NewModelRunner(testPool), testRoot, seq)[1:] {
			v := verb(o.Cmd)
			total[v]++
			if o.Code == 0 {
				ok[v]++
			}
		}
	}
	for seed := 0; seed < n; seed++ {
		steps := 14
		if s := os.Getenv("ZFSDIFF_STEPS"); s != "" {
			steps, _ = strconv.Atoi(s)
		}
		seq := NewGen(uint64(seed)).Seq(fmt.Sprintf("seed%d", seed), steps)
		d, seq, ids := diffSkippingKnown(seq)
		for _, id := range ids {
			knownCount[id]++
		}
		stats(seq)
		if d == nil {
			continue
		}
		if f := bySig[d.Sig]; f != nil {
			f.count++
			continue
		}
		min := Minimise(seq, diffModelSim)
		bySig[d.Sig] = &found{min, diffModelSim(min), 1}
	}
	sigs := make([]string, 0, len(bySig))
	for s := range bySig {
		sigs = append(sigs, s)
	}
	sort.Strings(sigs)
	var b strings.Builder
	for _, s := range sigs {
		f := bySig[s]
		fmt.Fprintf(&b, "=== %s (x%d)\n%s%s\n\n", s, f.count, f.seq.Format(), f.d)
	}
	verbs := make([]string, 0, len(total))
	for v := range total {
		verbs = append(verbs, v)
	}
	sort.Strings(verbs)
	fmt.Fprintf(&b, "known: %v\nmodel step success by verb:", knownCount)
	for _, v := range verbs {
		fmt.Fprintf(&b, " %s %d/%d", v, ok[v], total[v])
	}
	b.WriteString("\n")
	t.Logf("%d signatures\n%s", len(sigs), b.String())
	if out := os.Getenv("ZFSDIFF_OUT"); out != "" {
		_ = os.WriteFile(out, []byte(b.String()), 0o644)
	}
}

// TestCorpusModelVsSim replays the corpus on the model and the simulator.
// Every divergence must be a Known ambiguity or a sequence the simulator
// does not implement (exit 3: rename, destroy -R).
func TestCorpusModelVsSim(t *testing.T) {
	seqs, err := Corpus()
	if err != nil {
		t.Fatal(err)
	}
	if len(seqs) < 50 {
		t.Fatalf("corpus has %d sequences", len(seqs))
	}
	for _, seq := range seqs {
		d := diffModelSim(seq)
		if d == nil {
			continue
		}
		if id := known(d); id != "" {
			t.Logf("%s: known ambiguity %s", seq.Name, id)
			continue
		}
		t.Errorf("%s: model and simulator diverge\n%s", seq.Name, d)
	}
}

// TestReplayHarnessOnSim runs the real-pool replay machinery with one
// shared simulator standing in for the pool: every sequence must run
// without a harness error, and only the Known ambiguities may differ.
func TestReplayHarnessOnSim(t *testing.T) {
	seqs, err := Corpus()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	shared := NewSimRunner(testPool)
	target := Target{Runner: shared, Cleanup: func(ctx context.Context, root string) error {
		// the simulator has no `zfs holds`: release the corpus's tags
		for _, tag := range []string{"t1", "t2"} {
			out, _ := shared.Run(ctx, "zfs", "get", "-Hp", "-o", "name", "-r", "-t", "snapshot", "type", root)
			for _, snap := range strings.Fields(string(out)) {
				if strings.Contains(snap, "@") {
					_, _ = shared.Run(ctx, "zfs", "release", tag, snap)
				}
			}
		}
		_, err := shared.Run(ctx, "zfs", "destroy", "-r", root)
		return err
	}}
	if _, err := shared.Run(ctx, "zfs", "create", testPool+"/zd"); err != nil {
		t.Fatal(err)
	}
	rs := ReplayAll(ctx, target, testPool, testPool+"/zd", seqs, 4, time.Time{})
	for _, r := range rs {
		switch r.Verdict() {
		case "agree":
		case "model-bug": // the simulator stands in for the pool here
			if id := known(r.RealMod); id == "" {
				t.Errorf("%s: model differs from the simulator-as-pool:\n%s", r.Seq.Name, r.RealMod)
			}
		default:
			t.Errorf("%s: %s %v", r.Seq.Name, r.Verdict(), r.Err)
		}
	}
	rep := Report(rs)
	if !strings.HasPrefix(rep, "zfsdiff replay: 100 sequences:") && !strings.HasPrefix(rep, fmt.Sprintf("zfsdiff replay: %d sequences:", len(seqs))) {
		t.Fatalf("report header: %q", strings.SplitN(rep, "\n", 2)[0])
	}
	t.Log(strings.SplitN(rep, "\n", 2)[0])
}
