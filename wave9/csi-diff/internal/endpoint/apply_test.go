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
		OpClone: "clone", OpSet: "set", OpDestroy: "destroy", 0: "OpKind(0)", 99: "OpKind(99)",
	} {
		if got := k.String(); got != want {
			t.Errorf("%d: %q, want %q", k, got, want)
		}
	}
}
