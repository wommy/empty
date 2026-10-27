// Package lock provides keyed mutual exclusion for endpoint operations.
//
// The baseline (democratic-csi) uses a process-global Set checked and filled
// per request and fails fast with ABORTED; it also omits the source snapshot
// from CreateVolume's keys, so a clone can race the snapshot's deletion. Here
// an operation names every resource it touches, keys are taken in sorted
// order (no deadlock between overlapping key sets), and callers wait up to
// their context deadline before giving up.
package lock

import (
	"context"
	"sort"
	"sync"
)

// Keyed is a set of named locks. The zero value is ready to use.
type Keyed struct {
	mu    sync.Mutex
	held  map[string]chan struct{} // closed when released
	count int
}

// Acquire locks every key, waiting for holders to release. It returns a
// release func, or ctx.Err() if the context ends first (nothing is held then).
func (k *Keyed) Acquire(ctx context.Context, keys ...string) (func(), error) {
	keys = dedupe(keys)
	var taken []string
	release := func() {
		k.mu.Lock()
		defer k.mu.Unlock()
		for _, key := range taken {
			close(k.held[key])
			delete(k.held, key)
		}
		taken = nil
	}
	for _, key := range keys {
		for {
			k.mu.Lock()
			if k.held == nil {
				k.held = map[string]chan struct{}{}
			}
			ch, busy := k.held[key]
			if !busy {
				k.held[key] = make(chan struct{})
				taken = append(taken, key)
				k.mu.Unlock()
				break
			}
			k.mu.Unlock()
			select {
			case <-ch:
			case <-ctx.Done():
				release()
				return nil, ctx.Err()
			}
		}
	}
	var once sync.Once
	return func() { once.Do(release) }, nil
}

// Held reports whether key is currently locked (for tests and metrics).
func (k *Keyed) Held(key string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	_, ok := k.held[key]
	return ok
}

func dedupe(keys []string) []string {
	out := append([]string(nil), keys...)
	sort.Strings(out)
	n := 0
	for i, s := range out {
		if s == "" || (i > 0 && s == out[i-1]) {
			continue
		}
		out[n] = s
		n++
	}
	return out[:n]
}
