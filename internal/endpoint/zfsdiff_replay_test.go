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
// reference model, and writes what diverged to $ZFSDIFF_REPORT (the CI
// guest copies it back). It runs here, in endpoint.test, because that is
// the binary ci/real-zfs-guest.sh already runs as root against
// ZFS_TEST_POOL.
//
// On a pool it replays zfsdiff.PRSample (about a fifth of the corpus:
// ADR 0005's one-minute PR budget), or the whole corpus when
// ZFSDIFF_FULL=1 (nightly.yml). It stops starting sequences at its
// budget and fails past it. Without a pool it replays the whole corpus
// on the model and the simulator only, so it never skips.
//
// Divergences are reported, not failed, unless ZFSDIFF_STRICT=1: a
// divergence is a triage item (sim bug, model bug, ambiguity), not an
// endpoint regression. A harness or cleanup error always fails.
func TestZFSDiffReplay(t *testing.T) {
	seqs, err := zfsdiff.Corpus()
	if err != nil {
		t.Fatal(err)
	}
	pool := os.Getenv("ZFS_TEST_POOL")
	if pool == "" {
		if os.Getenv("ZFS_REQUIRE_REAL") != "" {
			t.Fatal("ZFS_REQUIRE_REAL is set but ZFS_TEST_POOL is not")
		}
		ctx := context.Background()
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
	// PR: ~5s expected (ci/real-zfs-weights.txt); nightly: the corpus
	startBudget, budget := 12*time.Second, 15*time.Second
	if os.Getenv("ZFSDIFF_FULL") == "1" {
		startBudget, budget = 150*time.Second, 180*time.Second
	} else {
		seqs = zfsdiff.Sample(seqs, zfsdiff.PRSample)
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), budget+30*time.Second)
	// registered first, so it runs last: no cleanup below runs on a
	// cancelled ctx (and none uses ctx: an expired replay still cleans up)
	t.Cleanup(cancel)
	base := fmt.Sprintf("%s/zfsdiff-%d-%d", pool, os.Getpid(), time.Now().UnixNano())
	c := zfs.New(zfs.LocalRunner{})
	if err := c.CreateFilesystem(ctx, base, nil, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.WithoutCancel(ctx), zfsdiff.CleanupTimeout)
		defer ccancel()
		if err := zfsdiff.CleanupReal(cctx, base); err != nil {
			t.Errorf("cleanup %s: %v", base, err)
		}
	})
	rs := zfsdiff.ReplayAll(ctx, zfsdiff.RealTarget, pool, base, seqs, 4, start.Add(startBudget))
	elapsed := time.Since(start)
	report := zfsdiff.Report(rs)
	t.Logf("%s (%s)", strings.SplitN(report, "\n", 2)[0], elapsed.Round(time.Millisecond))
	if path := os.Getenv("ZFSDIFF_REPORT"); path != "" {
		if err := os.WriteFile(path, []byte(report+fmt.Sprintf("\n%s in %s\n", strings.SplitN(report, "\n", 2)[0], elapsed.Round(time.Millisecond))), 0o644); err != nil {
			t.Errorf("report: %v", err)
		}
	} else {
		t.Log(report)
	}
	strict := os.Getenv("ZFSDIFF_STRICT") == "1"
	for _, r := range rs {
		switch v := r.Verdict(); {
		case v == "harness-error":
			t.Errorf("%s: %v", r.Seq.Name, r.Err)
		case v == "not-run":
			t.Errorf("%s: not started within the %v budget", r.Seq.Name, startBudget)
		case v != "agree" && strict:
			t.Errorf("%s: %s", r.Seq.Name, v)
		}
	}
	if elapsed > budget {
		t.Errorf("replay of %d sequences took %v, budget %v", len(seqs), elapsed, budget)
	}
}
