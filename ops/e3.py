import sys
root = sys.argv[1]

def sub(path, pairs):
    p = root + '/' + path
    s = open(p).read()
    for old, new in pairs:
        assert old in s, (path, old[:80])
        s = s.replace(old, new, 1)
    open(p, 'w').write(s)

sub('internal/lock/lock.go', [
('''import (
	"context"
	"sort"
	"sync"
)''', '''import (
	"context"
	"sort"
	"sync"
	"time"
)'''),
('''// key is a held key and its waiters, oldest first. A waiter's channel is
// closed when the key is handed to it.
type key struct{ waiters []chan struct{} }''', '''// key is a held key, who holds it since when (for messages: Holder), and
// its waiters, oldest first. A waiter's channel is closed when the key is
// handed to it.
type key struct {
	waiters []waiter
	owner   string
	since   time.Time
}

type waiter struct {
	ch    chan struct{}
	owner string
}

type ownerKey struct{}

// WithOwner returns ctx naming the owner (a request id) that the locks
// taken with it record, for Holder.
func WithOwner(ctx context.Context, owner string) context.Context {
	return context.WithValue(ctx, ownerKey{}, owner)
}

// Owner is the owner WithOwner put in ctx, or "".
func Owner(ctx context.Context) string {
	o, _ := ctx.Value(ownerKey{}).(string)
	return o
}

// Holder reports who holds name (its owner, "" if the taker named none)
// and since when; ok is false when it is free.
func (k *Keyed) Holder(name string) (owner string, since time.Time, ok bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	h, ok := k.held[name]
	if !ok {
		return "", time.Time{}, false
	}
	return h.owner, h.since, true
}'''),
('''		h, busy := k.held[name]
		if !busy {
			k.held[name] = &key{}
			taken = append(taken, name)
			k.mu.Unlock()
			continue
		}
		ch := make(chan struct{})
		h.waiters = append(h.waiters, ch)''', '''		h, busy := k.held[name]
		if !busy {
			k.held[name] = &key{owner: Owner(ctx), since: time.Now()}
			taken = append(taken, name)
			k.mu.Unlock()
			continue
		}
		ch := make(chan struct{})
		h.waiters = append(h.waiters, waiter{ch, Owner(ctx)})'''),
('''// TryAcquire locks every key only if none is held, without waiting. It
// reports false (and holds nothing) when any key is busy. Never waiting
// means it cannot take part in a wait cycle, whatever the key order.
func (k *Keyed) TryAcquire(keys ...string) (func(), bool) {
	keys = dedupe(keys)''', '''// TryAcquire locks every key only if none is held, without waiting. It
// reports false (and holds nothing) when any key is busy. Never waiting
// means it cannot take part in a wait cycle, whatever the key order.
func (k *Keyed) TryAcquire(keys ...string) (func(), bool) {
	return k.TryAcquireContext(context.Background(), keys...)
}

// TryAcquireContext is TryAcquire recording ctx's Owner. ctx is not
// waited on.
func (k *Keyed) TryAcquireContext(ctx context.Context, keys ...string) (func(), bool) {
	keys = dedupe(keys)
	owner, now := Owner(ctx), time.Now()'''),
('''	for _, name := range keys {
		k.held[name] = &key{}
	}''', '''	for _, name := range keys {
		k.held[name] = &key{owner: owner, since: now}
	}'''),
('''	next := h.waiters[0]
	h.waiters = h.waiters[1:]
	close(next)
}

func (h *key) remove(ch chan struct{}) {
	for i, w := range h.waiters {
		if w == ch {''', '''	next := h.waiters[0]
	h.waiters = h.waiters[1:]
	h.owner, h.since = next.owner, time.Now()
	close(next.ch)
}

func (h *key) remove(ch chan struct{}) {
	for i, w := range h.waiters {
		if w.ch == ch {'''),
])

sub('internal/server/daemon.go', [
('''// WithRequestID returns ctx carrying the request id.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDCtx{}, id)
}''', '''// WithRequestID returns ctx carrying the request id, also as the owner of
// the locks the call takes (lock.WithOwner), which a call refused on them
// names.
func WithRequestID(ctx context.Context, id string) context.Context {
	return lock.WithOwner(context.WithValue(ctx, requestIDCtx{}, id), id)
}'''),
('''	pb "github.com/wommy/empty/gen/zfsendpoint/v1"
)''', '''	pb "github.com/wommy/empty/gen/zfsendpoint/v1"
	"github.com/wommy/empty/internal/lock"
)'''),
])

sub('internal/endpoint/endpoint.go', [
('''		if release, ok := e.locks.TryAcquire(keys...); ok {
			return release, nil
		}
		return nil, errorf(Aborted, "operation pending on %v", keys)
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	release, err := e.locks.Acquire(ctx, keys...)
	if err != nil {
		return nil, errorf(Aborted, "operation pending on %v", keys)
	}''', '''		if release, ok := e.locks.TryAcquireContext(ctx, keys...); ok {
			return release, nil
		}
		return nil, errorf(Aborted, "operation pending on %v%s", keys, e.pending(keys, 0))
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	start := time.Now()
	release, err := e.locks.Acquire(ctx, keys...)
	if err != nil {
		return nil, errorf(Aborted, "operation pending on %v%s", keys, e.pending(keys, time.Since(start)))
	}'''),
])
