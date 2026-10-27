package zfsdiff

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/wommy/empty/internal/zfs"
)

// RealRunner runs the real zfs(8) on this host.
type RealRunner struct{ zfs.LocalRunner }

// SendRecv runs `zfs send -p SNAP | zfs recv DST`.
func (RealRunner) SendRecv(ctx context.Context, snap, dst string) error {
	cmd := exec.CommandContext(ctx, "bash", "-c", `set -o pipefail; zfs send -p "$1" | zfs recv "$2"`, "bash", snap, dst)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		code := -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		return &zfs.ExitError{Args: []string{"send|recv", snap, dst}, Code: code, Stderr: stderr.String(), Underlay: err}
	}
	return nil
}

// JSONCheck runs `zfs get -j -p` over root with the Projection and
// compares it with the `-Hp` projection proj (as Exec records it). It
// returns the mismatches.
func JSONCheck(ctx context.Context, r zfs.Runner, root string, proj []string) []string {
	out, err := r.Run(ctx, "zfs", "get", "-j", "-p", "-r", "-t", "filesystem,volume,snapshot",
		strings.Join(Projection, ","), root)
	if err != nil {
		return []string{"get -j failed: " + err.Error()}
	}
	var doc struct {
		Datasets map[string]struct {
			Properties map[string]struct {
				Value  string `json:"value"`
				Source struct {
					Type string `json:"type"`
					Data string `json:"data"`
				} `json:"source"`
			} `json:"properties"`
		} `json:"datasets"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return []string{"get -j output does not decode: " + err.Error()}
	}
	var lines []string
	for name, d := range doc.Datasets {
		for p, v := range d.Properties {
			src := map[string]string{"NONE": "-", "DEFAULT": "default", "LOCAL": "local",
				"TEMPORARY": "temporary", "RECEIVED": "received"}[v.Source.Type]
			if v.Source.Type == "INHERITED" {
				src = "inherited from " + v.Source.Data
			}
			if src == "" {
				src = "?" + v.Source.Type
			}
			lines = append(lines, strings.ReplaceAll(name+"\t"+p+"\t"+v.Value+"\t"+src, root, "$R"))
		}
	}
	sort.Strings(lines)
	onlyJ, onlyH := lineDiff(lines, proj)
	var msgs []string
	for _, l := range onlyJ {
		msgs = append(msgs, "json only: "+l)
	}
	for _, l := range onlyH {
		msgs = append(msgs, "-Hp only:  "+l)
	}
	return msgs
}

// CleanupReal releases every user hold under root and destroys it.
func CleanupReal(ctx context.Context, root string) error {
	r := zfs.LocalRunner{}
	out, err := r.Run(ctx, "zfs", "get", "-Hp", "-o", "name,value", "-r", "-t", "snapshot", "userrefs", root)
	if err != nil {
		return err
	}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(l, "\t")
		if len(f) != 2 || f[1] == "0" {
			continue
		}
		holds, err := r.Run(ctx, "zfs", "holds", "-H", f[0])
		if err != nil {
			return err
		}
		for _, h := range strings.Split(strings.TrimSpace(string(holds)), "\n") {
			if g := strings.Split(h, "\t"); len(g) >= 2 {
				if _, err := r.Run(ctx, "zfs", "release", g[1], f[0]); err != nil {
					return err
				}
			}
		}
	}
	_, err = r.Run(ctx, "zfs", "destroy", "-r", root)
	return err
}

// Replay is one corpus sequence run on the real pool, the simulator and
// the model.
type Replay struct {
	Seq              Seq
	Real, Sim, Model []Outcome
	RealSim, RealMod *Divergence
	ModSim           *Divergence
	JSON             []string
	Err              error
}

// Verdict names what the replay found.
func (r *Replay) Verdict() string {
	switch {
	case r.Err != nil:
		return "harness-error"
	case r.RealSim == nil && r.RealMod == nil:
		return "agree"
	case r.RealSim != nil && r.RealMod == nil:
		return "sim-bug" // the model matches the pool, the simulator does not
	case r.RealSim == nil && r.RealMod != nil:
		return "model-bug"
	case r.ModSim == nil:
		return "both-wrong" // model and simulator agree with each other, not with the pool
	}
	return "all-differ"
}

// Target is the pool replays run against: its runner (a SendRecver for
// @sendrecv steps) and how to remove a sequence's root afterwards.
type Target struct {
	Runner  zfs.Runner
	Cleanup func(ctx context.Context, root string) error
}

// RealTarget is the real zfs(8) on this host.
var RealTarget = Target{Runner: RealRunner{}, Cleanup: CleanupReal}

// ReplayAll runs seqs on target under base/s<i> (workers at a time) and
// on a fresh simulator and model for each, with pool as their pool.
func ReplayAll(ctx context.Context, target Target, pool, base string, seqs []Seq, workers int) []*Replay {
	out := make([]*Replay, len(seqs))
	idx := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idx {
				root := base + "/s" + strconv.Itoa(i)
				rp := &Replay{Seq: seqs[i]}
				rp.Real = Exec(ctx, target.Runner, root, seqs[i])
				if _, isReal := target.Runner.(RealRunner); isReal {
					rp.JSON = JSONCheck(ctx, target.Runner, root, rp.Real[len(rp.Real)-1].Proj)
				}
				rp.Sim = Exec(ctx, NewSimRunner(pool), root, seqs[i])
				rp.Model = Exec(ctx, NewModelRunner(pool), root, seqs[i])
				rp.RealSim, rp.RealMod, rp.ModSim = Compare(rp.Real, rp.Sim), Compare(rp.Real, rp.Model), Compare(rp.Model, rp.Sim)
				if err := target.Cleanup(ctx, root); err != nil {
					rp.Err = fmt.Errorf("cleanup %s: %w", root, err)
				}
				out[i] = rp
			}
		}()
	}
	for i := range seqs {
		idx <- i
	}
	close(idx)
	wg.Wait()
	return out
}

// Report renders replays: a summary, then each sequence that diverged
// with the divergences and the real trace.
func Report(rs []*Replay) string {
	var b strings.Builder
	count := map[string]int{}
	for _, r := range rs {
		count[r.Verdict()]++
	}
	keys := make([]string, 0, len(count))
	for k := range count {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintf(&b, "zfsdiff replay: %d sequences:", len(rs))
	for _, k := range keys {
		fmt.Fprintf(&b, " %s=%d", k, count[k])
	}
	b.WriteString("\n\n")
	for _, r := range rs {
		fmt.Fprintf(&b, "%-40s %s\n", r.Seq.Name, r.Verdict())
	}
	for _, r := range rs {
		if r.Verdict() == "agree" && len(r.JSON) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n==== %s: %s\n%s", r.Seq.Name, r.Verdict(), r.Seq.Format())
		if r.Err != nil {
			fmt.Fprintf(&b, "error: %v\n", r.Err)
		}
		for _, x := range []struct {
			what string
			d    *Divergence
		}{{"real vs sim", r.RealSim}, {"real vs model", r.RealMod}, {"model vs sim", r.ModSim}} {
			if x.d != nil {
				fmt.Fprintf(&b, "-- %s (A=%s):\n%s\n", x.what, strings.Fields(x.what)[0], x.d)
			}
		}
		for _, j := range r.JSON {
			fmt.Fprintf(&b, "-- get -j: %s\n", j)
		}
		fmt.Fprintf(&b, "-- real trace:\n%s", Render(r.Real))
	}
	return b.String()
}
