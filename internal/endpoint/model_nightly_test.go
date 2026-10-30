//go:build nightly

package endpoint_test

// The model suite's nightly tier (ADR 0005): the long sweep and the check
// that every mask still hides a live divergence. The nightly explore job
// runs them with `-tags nightly -run '^TestExplore'`.

import (
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestExploreModelSweep runs MODEL_SEQS (20,000) sequences on the
// simulator, starting at a seed that moves on every day (logged, and in a
// failure's replay line), so successive nights cover new sequences. About
// 3 to 4 minutes on 4 cores.
func TestExploreModelSweep(t *testing.T) {
	checkMaskExpiry(t)
	seqs := envInt("MODEL_SEQS", 20000)
	day := uint64(time.Now().Unix() / 86400)
	seed0 := uint64(envInt("MODEL_SEED0", int(1+day%1000*uint64(seqs))))
	t.Logf("seeds %d..%d", seed0, seed0+uint64(seqs)-1)
	modelSweep(t, seqs, envInt("MODEL_LEN", 40), seed0)
}

// TestExploreModelMasksStillBite lifts one mask at a time and hunts for a
// failure in up to MODEL_SEQS (20,000) sequences. A mask that no longer
// finds one is stale: the divergence was fixed, or the mask is wrong, so
// the test fails and names it for deletion.
func TestExploreModelMasksStillBite(t *testing.T) {
	seqs, n, workers := envInt("MODEL_SEQS", 20000), envInt("MODEL_LEN", 40), envInt("MODEL_WORKERS", 8)
	for _, k := range knownOpens {
		t.Run(k.id, func(t *testing.T) {
			unmaskOnly = k.id
			defer func() { unmaskOnly = "" }()
			var found atomic.Uint64
			var wg sync.WaitGroup
			start := time.Now()
			for w := range workers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for seed := uint64(1 + w); seed <= uint64(seqs) && found.Load() == 0; seed += uint64(workers) {
						trace := genTrace(rngSource{rand.New(rand.NewPCG(seed, 0x5eed))}, n)
						if runTrace(t, newRig(t, fmt.Sprintf("b%d", seed)), trace) != nil {
							found.CompareAndSwap(0, seed)
						}
					}
				}()
			}
			wg.Wait()
			if found.Load() == 0 {
				t.Errorf("mask %s (%s) found nothing in %d sequences: delete it if the fix landed", k.id, k.row, seqs)
				return
			}
			t.Logf("mask %s still bites: seed %d (%v)", k.id, found.Load(), time.Since(start).Round(time.Millisecond))
		})
	}
}
