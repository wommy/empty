package lock

import (
	"context"
	"testing"
	"time"
)

func TestTryAcquire(t *testing.T) {
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
