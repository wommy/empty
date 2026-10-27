package zfs

import (
	"context"
	"testing"
)

// recRunner records argv and answers with canned output.
type recRunner struct {
	calls [][]string
	out   string
}

func (r *recRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, args)
	return []byte(r.out), nil
}

// H-11: the name and property checks are no looser than zfs, and nothing
// a caller passes as a name can become an option on the command line.
func TestNamesAndPropsNeverBecomeOptions(t *testing.T) {
	t.Parallel()
	// zfs refuses "." and ".." as a component (entity_namecheck:
	// NAME_ERR_SELF_REF, NAME_ERR_PARENT_REF)
	for _, n := range []string{"tank/..", "tank/../x", "tank/./x", "a/b/..", "tank/.", "tank/x/."} {
		if ValidDataset(n) {
			t.Errorf("ValidDataset(%q) = true, zfs refuses it", n)
		}
	}
	for _, n := range []string{"tank/x@.", "tank/x@..", "tank/../x@s", "tank/.@s"} {
		if ValidSnapshot(n) {
			t.Errorf("ValidSnapshot(%q) = true, zfs refuses it", n)
		}
	}
	// dots inside a component stay legal
	for _, n := range []string{"tank/a.b", "tank/...", "tank/.x", "tank/x."} {
		if !ValidDataset(n) {
			t.Errorf("ValidDataset(%q) = false", n)
		}
	}
	for _, n := range []string{"tank/x@a.b", "tank/x@...", "tank/x@.s"} {
		if !ValidSnapshot(n) {
			t.Errorf("ValidSnapshot(%q) = false", n)
		}
	}

	ctx := context.Background()
	rr := &recRunner{out: "tank/x\ttype\tfilesystem\t-\n"}
	c := New(rr)
	// Set passes k=v without -o, so a key starting with '-' would reach
	// zfs's getopt as a flag cluster
	if err := c.Set(ctx, "tank/x", map[string]string{"-u": "1"}); err == nil {
		t.Errorf("Set accepted property %q", "-u")
	}
	if err := c.Inherit(ctx, "tank/x", "-r"); err == nil {
		t.Errorf("Inherit accepted property %q", "-r")
	}
	// Get, List and ListColumns join the property list into one argv word
	if _, err := c.Get(ctx, "tank/x", "-r"); err == nil {
		t.Errorf("Get accepted property %q", "-r")
	}
	if _, err := c.List(ctx, "tank", ListOptions{Props: []string{"type", "-H"}}); err == nil {
		t.Errorf("List accepted property %q", "-H")
	}
	if _, err := c.ListColumns(ctx, "tank", ListOptions{Props: []string{"type,name", "-t"}}); err == nil {
		t.Errorf("ListColumns accepted properties %q", []string{"type,name", "-t"})
	}
	if len(rr.calls) != 0 {
		t.Errorf("zfs ran: %q", rr.calls)
	}
	// what callers pass stays accepted
	if _, err := c.Get(ctx, "tank/x", "all"); err != nil {
		t.Errorf("Get all: %v", err)
	}
	if _, err := c.Get(ctx, "tank/x", "type", "p:managed_resource"); err != nil {
		t.Errorf("Get of a user property: %v", err)
	}
}
