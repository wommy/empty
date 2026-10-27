package main

import (
	"strings"
	"testing"
)

func TestLockParentIsExclusive(t *testing.T) {
	dir := t.TempDir()
	unlock, err := lockParent(dir, "tank/k8s/vols")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockParent(dir, "tank/k8s/vols"); err == nil || !strings.Contains(err.Error(), "already manages") {
		t.Fatalf("second lock: %v", err)
	}
	if u2, err := lockParent(dir, "tank/other"); err != nil {
		t.Fatalf("other parent: %v", err)
	} else {
		u2()
	}
	unlock()
	u3, err := lockParent(dir, "tank/k8s/vols")
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	u3()
}

// Nested parents conflict both ways; siblings do not.
func TestLockParentRefusesNestedParents(t *testing.T) {
	dir := t.TempDir()
	unlock, err := lockParent(dir, "tank/k8s")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"tank/k8s/vols", "tank/k8s/vols/deep", "tank"} {
		if u, err := lockParent(dir, p); err == nil || !strings.Contains(err.Error(), "already manages") {
			if u != nil {
				u()
			}
			t.Errorf("%s beside tank/k8s: %v", p, err)
		}
	}
	unlock()

	a, err := lockParent(dir, "tank/k8s/a")
	if err != nil {
		t.Fatal(err)
	}
	defer a()
	b, err := lockParent(dir, "tank/k8s/b")
	if err != nil {
		t.Fatalf("sibling: %v", err)
	}
	defer b()
	if u, err := lockParent(dir, "tank/k8s"); err == nil {
		u()
		t.Fatal("ancestor of running endpoints admitted")
	}
	// a failed attempt leaves nothing held: a's subtree is still locked
	// and tank/other is still free
	if _, err := lockParent(dir, "tank/k8s/a"); err == nil {
		t.Fatal("second endpoint for tank/k8s/a")
	}
	o, err := lockParent(dir, "tank/other")
	if err != nil {
		t.Fatalf("unrelated parent: %v", err)
	}
	o()
}
