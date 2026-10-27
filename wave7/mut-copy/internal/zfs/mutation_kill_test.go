package zfs

import (
	"errors"
	"strings"
	"testing"
)

// classify: every stderr phrase, with the kind it must map to, written out
// independently of the patterns table (a table-driven test that ranges over
// `patterns` would pass whatever the table said).
func TestClassifyEveryPhrase(t *testing.T) {
	for _, tc := range []struct {
		text string
		want Kind
	}{
		{"has dependent clones", KindHasClones}, {"snapshot is cloned", KindHasClones},
		{"has children", KindHasChildren},
		{"parent does not exist", KindParentNotFound}, {"no such parent", KindParentNotFound},
		{"dataset already exists", KindExists}, {"pool or dataset exists", KindExists},
		{"dataset does not exist", KindNotFound}, {"no such pool or dataset", KindNotFound},
		{"could not find any snapshots", KindNotFound},
		{"dataset is busy", KindBusy}, {"pool or dataset is busy", KindBusy}, {"it's being held", KindBusy},
		{"out of space", KindNoSpace}, {"size is greater than available space", KindNoSpace},
		{"invalid property value", KindInvalidProperty}, {"bad property", KindInvalidProperty},
		{"invalid property", KindInvalidProperty}, {"property is read-only", KindInvalidProperty},
		{"size is less than current used or reserved space", KindInvalidProperty},
		{"must be one of", KindInvalidProperty}, {"must be power of 2", KindInvalidProperty},
		{"key is not loaded", KindPrecondition}, {"pool must be upgraded", KindPrecondition},
		{"permission denied", KindPermission},
		{"no such pool", KindUnavailable}, {"pool i/o is currently suspended", KindUnavailable},
		{"pool is unavailable", KindUnavailable}, {"pool is read-only", KindUnavailable},
		{"does not support this operation", KindUnavailable},
	} {
		for _, in := range []string{"cannot x 'tank/a': " + tc.text + "\n", strings.ToUpper(tc.text)} {
			if got := classify(in); got != tc.want {
				t.Errorf("classify(%q) = %v, want %v", in, got, tc.want)
			}
		}
	}
	if classify("something else entirely") != KindUnknown {
		t.Error("unmatched text must be KindUnknown")
	}
}

func TestErrorIsMatchesKindOnlyForBareSentinels(t *testing.T) {
	e := &Error{Kind: KindNotFound, Op: "destroy", Dataset: "tank/a", Stderr: "dataset does not exist"}
	if !errors.Is(e, ErrNotFound) {
		t.Error("sentinel must match on kind")
	}
	for _, tgt := range []*Error{{Kind: KindNotFound, Op: "destroy"}, {Kind: KindNotFound, Dataset: "tank/a"}, {Kind: KindExists}} {
		if errors.Is(e, tgt) {
			t.Errorf("target %+v is not a bare sentinel and must not match", tgt)
		}
	}
}

func TestErrorTextPrefersStderrThenCause(t *testing.T) {
	cause := errors.New("exit status 1")
	if s := (&Error{Kind: KindNotFound, Op: "get", Dataset: "d", Stderr: "boom\n", Err: cause}).Error(); !strings.Contains(s, "boom") || strings.Contains(s, "exit status") {
		t.Errorf("stderr must win: %q", s)
	}
	if s := (&Error{Kind: KindNotFound, Op: "get", Dataset: "d", Err: cause}).Error(); !strings.Contains(s, "exit status 1") {
		t.Errorf("empty stderr falls back to the cause: %q", s)
	}
}

// parseColumns reads untrusted output: malformed lines are errors, never
// panics or silent misreads.
func TestParseColumnsRejectsMalformedLines(t *testing.T) {
	for _, line := range []string{"tank/a\ttype\tvolume\n", "tank/a\ttype\n", "tank/a\n", "a\tb\tc\n"} {
		if _, err := parseColumns([]byte(line), []string{"type"}); err == nil {
			t.Errorf("%q: want error", line)
		}
	}
}

func TestParseColumnsIgnoresPropertiesNotAskedFor(t *testing.T) {
	out := "tank/a\ttype\tvolume\t-\ntank/a\tbogus\tx\tlocal\n"
	c, err := parseColumns([]byte(out), []string{"type", "volsize"})
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Cell(0, 0); got.Value != "volume" || got.Source != "-" {
		t.Errorf("column 0 clobbered by an unrequested property: %+v", got)
	}
	if got := c.Cell(0, 1); got.Value != "-" {
		t.Errorf("unset column changed: %+v", got)
	}
}

func TestParseColumnsSkipsBlankLines(t *testing.T) {
	out := "tank/a\ttype\tvolume\t-\n\ntank/b\ttype\tfilesystem\t-\n"
	c, err := parseColumns([]byte(out), []string{"type"})
	if err != nil || len(c.Names) != 2 {
		t.Fatalf("names %v err %v", c, err)
	}
}
