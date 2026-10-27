package lock

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestDoneContextTakesNoFreeKey(t *testing.T) {
	t.Parallel()
	var k Keyed
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if rel, err := k.Acquire(ctx, "vol:a"); err == nil || rel != nil {
		t.Fatalf("acquired with a done context: %v", err)
	}
	if k.Held("vol:a") {
		t.Fatal("key left held")
	}
}

// Waiters get a key in arrival order.
func TestWaitersAreServedInOrder(t *testing.T) {
	t.Parallel()
	synctest.Test(t, waitersAreServedInOrder)
}

func waitersAreServedInOrder(t *testing.T) {
	var k Keyed
	rel, _ := k.Acquire(context.Background(), "vol:a")
	var mu sync.Mutex
	var order []int
	var wg sync.WaitGroup
	for i := range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := k.Acquire(context.Background(), "vol:a")
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			order = append(order, i)
			mu.Unlock()
			r()
		}()
		waitWaiters(t, &k, "vol:a", i+1)
	}
	rel()
	wg.Wait()
	for i, v := range order {
		if v != i {
			t.Fatalf("order %v, want 0..4", order)
		}
	}
}

func waitWaiters(t *testing.T, k *Keyed, name string, n int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		k.mu.Lock()
		got := len(k.held[name].waiters)
		k.mu.Unlock()
		if got == n {
			return
		}
	}
	t.Fatalf("waiters never reached %d", n)
}

// A waiter competing with re-acquirers in a tight loop still gets the key.
func TestNoStarvation(t *testing.T) {
	t.Parallel()
	synctest.Test(t, noStarvation)
}

func noStarvation(t *testing.T) {
	var k Keyed
	stop := make(chan struct{})
	var hammer sync.WaitGroup
	for range 8 {
		hammer.Add(1)
		go func() {
			defer hammer.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				r, _ := k.Acquire(context.Background(), "vol:a")
				time.Sleep(200 * time.Microsecond)
				r()
			}
		}()
	}
	time.Sleep(10 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	r, err := k.Acquire(ctx, "vol:a")
	waited := time.Since(start)
	close(stop)
	if err == nil {
		r()
	}
	hammer.Wait() // before any Fatal: the bubble must end with no goroutines
	if err != nil {
		t.Fatalf("victim starved: %v", err)
	}
	// in the bubble time is exact: first come, first served means at most
	// one hold per hammer queued ahead of the victim, plus the current one
	if hold := 200 * time.Microsecond; waited > 9*hold {
		t.Fatalf("victim waited %v, %d holds; first come first served allows 9", waited, waited/hold)
	}
}

// Waiters that give up leave nothing held and no stale queue entries, even
// when a handover races the cancellation.
func TestCancelledWaitersLeaveNothingBehind(t *testing.T) {
	t.Parallel()
	synctest.Test(t, cancelledWaitersLeaveNothingBehind)
}

func cancelledWaitersLeaveNothingBehind(t *testing.T) {
	var k Keyed
	for range 200 {
		rel, _ := k.Acquire(context.Background(), "vol:a")
		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		for range 10 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if r, err := k.Acquire(ctx, "vol:b", "vol:a"); err == nil {
					r()
				}
			}()
		}
		go cancel()
		rel()
		wg.Wait()
		if k.Held("vol:a") || k.Held("vol:b") {
			t.Fatal("key left held after every waiter finished")
		}
	}
}

func TestTryAcquire(t *testing.T) {
	t.Parallel()
	synctest.Test(t, tryAcquire)
}

func tryAcquire(t *testing.T) {
	var k Keyed
	rel, ok := k.TryAcquire("a", "b")
	if !ok || !k.Held("a") || !k.Held("b") {
		t.Fatal("free keys not taken")
	}
	if _, ok := k.TryAcquire("b", "c"); ok {
		t.Fatal("busy key taken")
	}
	if k.Held("c") {
		t.Fatal("a refused try holds a key")
	}
	// a waiter on a try-held key wakes when it is released
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		r, err := k.Acquire(ctx, "a")
		if err == nil {
			r()
		}
		done <- err
	}()
	rel()
	rel() // idempotent
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if rel2, ok := k.TryAcquire("a", "b", "c"); !ok {
		t.Fatal("released keys not free")
	} else {
		rel2()
	}
}
