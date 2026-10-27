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
