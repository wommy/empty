// Package lock provides keyed mutual exclusion for endpoint operations.
//
// The baseline (democratic-csi) uses a process-global Set checked and filled
// per request and fails fast with ABORTED; it also omits the source snapshot
// from CreateVolume's keys, so a clone can race the snapshot's deletion. Here
// an operation names every resource it touches, keys are taken in sorted
// order (no deadlock between overlapping key sets), and callers wait up to
// their context deadline before giving up.
//
// Each key is handed over first come, first served: a release passes the
// key straight to its oldest waiter, so a steady stream of re-acquirers
// cannot starve one that has been waiting.
package lock

import (
	"context"
	"sort"
	"sync"
)

// Keyed is a set of named locks. The zero value is ready to use.
type Keyed struct {
	mu   sync.Mutex
	held map[string]*key // present while held
}

// key is a held key and its waiters, oldest first. A waiter's channel is
// closed when the key is handed to it.
type key struct{ waiters []chan struct{} }

// Acquire locks every key, waiting for holders to release. It returns a
// release func, or ctx.Err() if the context ends first (nothing is held
// then). A context that has already ended takes nothing, not even a free
// key: the caller has no time left to use it.
func (k *Keyed) Acquire(ctx context.Context, keys ...string) (func(), error) {
	keys = dedupe(keys)
	var taken []string
	release := func() {
		k.mu.Lock()
		defer k.mu.Unlock()
		for _, name := range taken {
			k.unlock(name)
		}
		taken = nil
	}
	for _, name := range keys {
		k.mu.Lock()
		if err := ctx.Err(); err != nil {
			k.mu.Unlock()
			release()
			return nil, err
		}
		if k.held == nil {
			k.held = map[string]*key{}
		}
		h, busy := k.held[name]
		if !busy {
			k.held[name] = &key{}
			taken = append(taken, name)
			k.mu.Unlock()
			continue
		}
		ch := make(chan struct{})
		h.waiters = append(h.waiters, ch)
		k.mu.Unlock()
		select {
		case <-ch:
			taken = append(taken, name)
		case <-ctx.Done():
			k.mu.Lock()
			select {
			case <-ch:
				// handed over while we gave up: ours, so pass it on
				taken = append(taken, name)
			default:
				h.remove(ch)
			}
			k.mu.Unlock()
			release()
			return nil, ctx.Err()
		}
	}
	var once sync.Once
	return func() { once.Do(release) }, nil
}

// TryAcquire locks every key only if none is held, without waiting. It
// reports false (and holds nothing) when any key is busy. Never waiting
// means it cannot take part in a wait cycle, whatever the key order.
func (k *Keyed) TryAcquire(keys ...string) (func(), bool) {
	keys = dedupe(keys)
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, name := range keys {
		if _, busy := k.held[name]; busy {
			return nil, false
		}
	}
	if k.held == nil {
		k.held = map[string]*key{}
	}
	for _, name := range keys {
		k.held[name] = &key{}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			k.mu.Lock()
			defer k.mu.Unlock()
			for _, name := range keys {
				k.unlock(name)
			}
		})
	}, true
}

// unlock hands name to its oldest waiter, or frees it. k.mu is held.
func (k *Keyed) unlock(name string) {
	h := k.held[name]
	if len(h.waiters) == 0 {
		delete(k.held, name)
		return
	}
	next := h.waiters[0]
	h.waiters = h.waiters[1:]
	close(next)
}

func (h *key) remove(ch chan struct{}) {
	for i, w := range h.waiters {
		if w == ch {
			h.waiters = append(h.waiters[:i], h.waiters[i+1:]...)
			return
		}
	}
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
