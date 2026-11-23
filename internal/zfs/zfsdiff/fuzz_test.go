package zfsdiff

import (
	"encoding/binary"
	"fmt"
	"testing"
)

// FuzzDiff drives the generator from fuzz input and fails on any
// model-vs-simulator divergence that is not a Known ambiguity, after
// minimising it.
//
//	GOTOOLCHAIN=go1.27.3 go test ./internal/zfs/zfsdiff -run '^$' -fuzz FuzzDiff -fuzztime 60s
func FuzzDiff(f *testing.F) {
	for _, s := range []uint64{0, 1, 25, 54, 91, 121, 265, 292, 594, 678, 1502, 1679} {
		b := binary.LittleEndian.AppendUint64(nil, s)
		f.Add(append(b, 30))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 9 {
			return
		}
		seed := binary.LittleEndian.Uint64(data)
		n := 4 + int(data[8])%40
		seq := NewGen(seed).Seq(fmt.Sprintf("fuzz-%d-%d", seed, n), n)
		d, seq, _ := diffSkippingKnown(seq)
		if d == nil {
			return
		}
		min := Minimise(seq, diffModelSim)
		t.Fatalf("model and simulator diverge; minimised:\n%s%s", min.Format(), diffModelSim(min))
	})
}

// TestRandomModelVsSim is the seeded generator as a regression test: 30
// fixed seeds of 25 steps must agree up to the Known ambiguities
// (TestRandomSurvey and FuzzDiff go further).
func TestRandomModelVsSim(t *testing.T) {
	n := 30
	if testing.Short() {
		n = 8
	}
	for seed := 0; seed < n; seed++ {
		seq := NewGen(uint64(seed)).Seq(fmt.Sprintf("seed%d", seed), 25)
		if d, seq, _ := diffSkippingKnown(seq); d != nil {
			min := Minimise(seq, diffModelSim)
			t.Errorf("seed %d diverges; minimised:\n%s%s", seed, min.Format(), diffModelSim(min))
		}
	}
}
