package zfsdiff

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestPRSampleNamesCorpusSequences: every PRSample name is a corpus
// sequence, so the PR-tier replay runs what it claims to.
func TestPRSampleNamesCorpusSequences(t *testing.T) {
	seqs, err := Corpus()
	if err != nil {
		t.Fatal(err)
	}
	got := Sample(seqs, PRSample)
	if len(got) != len(PRSample) {
		var names []string
		for _, s := range got {
			names = append(names, s.Name)
		}
		t.Fatalf("PRSample names %d sequences, the corpus has %d of them: %v", len(PRSample), len(got), names)
	}
	// no share turned on (see PRSample); a received or inherited share
	// would have been set by some step first
	for _, s := range got {
		for _, st := range s.Steps {
			for _, a := range st.Args {
				if k, v, ok := strings.Cut(a, "="); ok && (k == "sharenfs" || k == "sharesmb") && v != "off" {
					t.Errorf("%s: `%s` shares; the PR sample must not", s.Name, st)
				}
			}
		}
	}
}

// cancellingRunner cancels the replay's context on its first command,
// as an expiring deadline would mid-sequence.
type cancellingRunner struct {
	SimRunner
	cancel context.CancelFunc
}

func (r cancellingRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.cancel()
	return r.SimRunner.Run(ctx, name, args...)
}

// TestReplayCleanupOutlivesItsContext: a replay whose context ends
// mid-sequence still cleans up, on a live context, and the sequences it
// had not started are not-run, not harness errors.
func TestReplayCleanupOutlivesItsContext(t *testing.T) {
	seqs, err := Corpus()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var cleaned []string
	target := Target{
		Runner: cancellingRunner{NewSimRunner(testPool), cancel},
		Cleanup: func(cctx context.Context, root string) error {
			if cctx.Err() != nil {
				t.Errorf("cleanup of %s ran on a dead context: %v", root, cctx.Err())
			}
			cleaned = append(cleaned, root)
			return nil
		},
	}
	rs := ReplayAll(ctx, target, testPool, testPool+"/zd", seqs[:3], 1, time.Time{})
	if len(cleaned) != 1 || rs[0].NotRun || rs[0].Err != nil {
		t.Fatalf("first sequence: cleaned %v, replay %+v", cleaned, rs[0])
	}
	for _, r := range rs[1:] {
		if r.Verdict() != "not-run" {
			t.Errorf("%s: %s after the context ended", r.Seq.Name, r.Verdict())
		}
	}
	// a start budget already spent starts nothing
	for _, r := range ReplayAll(context.Background(), target, testPool, testPool+"/zd", seqs[:3], 2, time.Now().Add(-time.Second)) {
		if !r.NotRun {
			t.Errorf("%s ran past its start budget", r.Seq.Name)
		}
	}
}
