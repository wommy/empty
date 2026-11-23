package zfsdiff

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestTraceScript runs $ZFSDIFF_SCRIPT on the model and the simulator and
// prints both traces (a development aid).
func TestTraceScript(t *testing.T) {
	script := os.Getenv("ZFSDIFF_SCRIPT")
	if script == "" {
		return
	}
	seq, err := Parse("explore", strings.ReplaceAll(script, ";", "\n"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	root := "zfsep-test/zd/s0"
	for _, b := range []struct {
		name string
		r    interface {
			Run(context.Context, string, ...string) ([]byte, error)
		}
	}{{"model", NewModelRunner("zfsep-test")}, {"sim", NewSimRunner("zfsep-test")}} {
		tr := Exec(ctx, b.r, root, seq)
		t.Logf("==== %s", b.name)
		for _, o := range tr {
			t.Logf("%s -> %d %s %s %q", o.Cmd, o.Code, o.Class, o.Kind, strings.TrimSpace(o.Stderr))
		}
		t.Logf("final:\n%s", strings.Join(tr[len(tr)-1].Proj, "\n"))
	}
}

// TestShowCorpus prints the model's trace of $ZFSDIFF_SHOW (corpus names,
// comma separated; a development aid).
func TestShowCorpus(t *testing.T) {
	names := os.Getenv("ZFSDIFF_SHOW")
	if names == "" {
		return
	}
	seqs, err := Corpus()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range seqs {
		if strings.Contains(","+names+",", ","+s.Name+",") || names == "all" {
			tr := Exec(context.Background(), NewModelRunner(testPool), testRoot, s)
			t.Logf("==== %s\n%s", s.Name, Render(tr))
		}
	}
}
