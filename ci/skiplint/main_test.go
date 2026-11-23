package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixture = `package p_test

import (
	"os"
	"testing"
)

type harness struct{ sim *int }

func (h *harness) Sim(t *testing.T) *int {
	if h.sim == nil {
		t.Skip("sim")
	}
	return h.sim
}

func requireSim(t *testing.T) {
	if os.Getenv("ZFS_TEST_POOL") != "" {
		t.Skip("sim")
	}
}

func failOn(h *harness, t *testing.T) { *h.Sim(t) = 1 }

func TestTop(t *testing.T) { requireSim(t) }

func TestViaHelper(t *testing.T) { failOn(&harness{}, t) }

func TestNamedSub(t *testing.T) {
	t.Run("real", func(t *testing.T) {})
	t.Run("only sim", func(t *testing.T) { requireSim(t) })
}

func TestLoopSub(t *testing.T) {
	for _, n := range []string{"a", "b"} {
		t.Run(n, func(t *testing.T) {
			if n == "b" {
				(&harness{}).Sim(t)
			}
		})
	}
}

func TestGoroutineDoesNotCount(t *testing.T) {
	go func() { requireSim(t) }()
}

func TestReal(t *testing.T) {}
`

func run(t *testing.T, list string) (errs, notes []string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "p"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "p", "p_test.go"), []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	lp := filepath.Join(dir, "skips.txt")
	if err := os.WriteFile(lp, []byte(list), 0o644); err != nil {
		t.Fatal(err)
	}
	errs, notes, err := check(lp, "vm", dir, []string{"p"})
	if err != nil {
		t.Fatal(err)
	}
	return errs, notes
}

const full = `# header
vm p TestTop          # Sim
vm p TestViaHelper    # Sim, through failOn
vm p TestNamedSub/only_sim
vm p TestLoopSub/b
`

func TestComplete(t *testing.T) {
	if errs, _ := run(t, full); len(errs) != 0 {
		t.Fatalf("want no error, got %q", errs)
	}
}

func TestMissingEntry(t *testing.T) {
	for _, drop := range []string{"TestTop", "TestViaHelper", "TestNamedSub/only_sim", "TestLoopSub/b"} {
		var keep []string
		for _, l := range strings.Split(full, "\n") {
			if !strings.Contains(l, " "+drop+" ") && !strings.HasSuffix(l, " "+drop) {
				keep = append(keep, l)
			}
		}
		errs, _ := run(t, strings.Join(keep, "\n"))
		if len(errs) != 1 || !strings.HasPrefix(errs[0], strings.TrimSuffix(drop, "/b")) {
			t.Errorf("without %s: got %q, want one error naming it", drop, errs)
		}
	}
}

func TestStaleEntry(t *testing.T) {
	errs, _ := run(t, full+"vm p TestGone\nvm q TestTop\nnfs p TestGone2\n")
	if len(errs) != 2 || !strings.Contains(errs[0], "q is not a real-pool package") || !strings.Contains(errs[1], "has no func TestGone") {
		t.Fatalf("got %q, want the unknown package and the missing test", errs)
	}
}

func TestUnmodelledEntryIsANote(t *testing.T) {
	errs, notes := run(t, full+"vm p TestReal\n")
	if len(errs) != 0 || len(notes) != 2 || !strings.Contains(notes[0], "TestReal") {
		t.Fatalf("got errors %q, notes %q", errs, notes)
	}
}
