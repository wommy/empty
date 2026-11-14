package endpoint

import (
	"context"
	"testing"

	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

// An op kind apply does not know is a program bug: the whole plan is
// refused as Internal before anything runs, never skipped as if done.
func TestApplyRefusesAnUnknownOpKind(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sim := zfstest.New("tank")
	e, err := New(zfs.New(sim), Config{Parent: "tank"})
	if err != nil {
		t.Fatal(err)
	}
	plan := Plan{{Kind: OpCreateFS, Target: "tank/a"}, {Kind: OpKind(99), Target: "tank/b"}}
	err = e.apply(ctx, plan)
	if CodeOf(err) != Internal {
		t.Fatalf("got %v, want Internal", err)
	}
	if sim.Has("tank/a") {
		t.Fatal("ops before the unknown kind ran")
	}
}

func TestOpKindString(t *testing.T) {
	for k, want := range map[OpKind]string{
		OpCreateFS: "create-filesystem", OpCreateVol: "create-volume", OpSnapshot: "snapshot",
		OpClone: "clone", OpSet: "set", OpDestroy: "destroy", OpMount: "mount", 0: "OpKind(0)", 99: "OpKind(99)",
	} {
		if got := k.String(); got != want {
			t.Errorf("%d: %q, want %q", k, got, want)
		}
	}
}

// A destroy the planner did not tie to a guid is a program bug too: it
// could hit whatever holds the name by then (OWN-15). Refused before
// anything runs.
func TestApplyRefusesADestroyWithoutGUID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sim := zfstest.New("tank")
	e, err := New(zfs.New(sim), Config{Parent: "tank"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.zfs.CreateFilesystem(ctx, "tank/b", nil, false); err != nil {
		t.Fatal(err)
	}
	plan := Plan{{Kind: OpCreateFS, Target: "tank/a"}, {Kind: OpDestroy, Target: "tank/b", OkMissing: true}}
	if err := e.apply(ctx, plan); CodeOf(err) != Internal {
		t.Fatalf("got %v, want Internal", err)
	}
	if sim.Has("tank/a") || !sim.Has("tank/b") {
		t.Fatal("the plan ran")
	}
}
