package endpoint_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfsdiff"
)

// TestZFSDiffReplay replays the zfsdiff corpus (internal/zfs/zfsdiff/
// testdata/zfsdiff, embedded) on a real pool, the simulator and the
// reference model, and writes what diverged to $ZFSDIFF_REPORT (the CI VM
// copies *.log back). It runs here, in endpoint.test, because that is the
// binary ci/real-zfs-vm.sh already runs as root against ZFS_TEST_POOL.
//
// Without a pool it replays the corpus on the model and the simulator
// only (the same check as zfsdiff's TestCorpusModelVsSim), so it never
// skips. Divergences are reported, not failed, unless ZFSDIFF_STRICT=1:
// a divergence is a triage item (sim bug, model bug, ambiguity), not an
// endpoint regression.
func TestZFSDiffReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	seqs, err := zfsdiff.Corpus()
	if err != nil {
		t.Fatal(err)
	}
	pool := os.Getenv("ZFS_TEST_POOL")
	if pool == "" {
		if os.Getenv("ZFS_REQUIRE_REAL") != "" {
			t.Fatal("ZFS_REQUIRE_REAL is set but ZFS_TEST_POOL is not")
		}
		n := 0
		for _, s := range seqs {
			m := zfsdiff.Exec(ctx, zfsdiff.NewModelRunner("tank"), "tank/zd", s)
			sm := zfsdiff.Exec(ctx, zfsdiff.NewSimRunner("tank"), "tank/zd", s)
			if d := zfsdiff.Compare(m, sm); d != nil {
				n++
				t.Logf("%s: model vs simulator: %s", s.Name, d)
			}
		}
		t.Logf("%d corpus sequences on model and simulator, %d diverge (see zfsdiff.Known)", len(seqs), n)
		return
	}
	start := time.Now()
	base := fmt.Sprintf("%s/zfsdiff-%d-%d", pool, os.Getpid(), time.Now().UnixNano())
	c := zfs.New(zfs.LocalRunner{})
	if err := c.CreateFilesystem(ctx, base, nil, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := zfsdiff.CleanupReal(context.Background(), base); err != nil {
			t.Errorf("cleanup %s: %v", base, err)
		}
	})
	rs := zfsdiff.ReplayAll(ctx, zfsdiff.RealTarget, pool, base, seqs, 4)
	report := zfsdiff.Report(rs)
	t.Logf("%s (%s)", strings.SplitN(report, "\n", 2)[0], time.Since(start).Round(time.Millisecond))
	if path := os.Getenv("ZFSDIFF_REPORT"); path != "" {
		if err := os.WriteFile(path, []byte(report), 0o644); err != nil {
			t.Errorf("report: %v", err)
		}
	} else {
		t.Log(report)
	}
	for _, r := range rs {
		switch v := r.Verdict(); {
		case v == "harness-error":
			t.Errorf("%s: %v", r.Seq.Name, r.Err)
		case v != "agree" && os.Getenv("ZFSDIFF_STRICT") == "1":
			t.Errorf("%s: %s", r.Seq.Name, v)
		}
	}
	if ctx.Err() != nil {
		t.Errorf("replay ran out of time: %v", ctx.Err())
	}
}
