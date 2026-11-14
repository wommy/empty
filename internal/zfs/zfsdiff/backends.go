package zfsdiff

import (
	"context"
	"errors"
	"strings"

	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

// RootProps mimic `zpool create -O compression=lz4` in ci/real-zfs-vm.sh.
var RootProps = map[string]string{"compression": "lz4"}

// NewModelRunner returns a model of pool with RootProps.
func NewModelRunner(pool string) ModelRunner { return ModelRunner{NewModel(pool, RootProps)} }

// SimRunner adapts the simulator.
type SimRunner struct{ *zfstest.Sim }

// NewSimRunner returns a simulator with pool and RootProps set on it.
func NewSimRunner(pool string) SimRunner {
	s := zfstest.New(pool)
	for k, v := range RootProps {
		_, _ = s.Run(context.Background(), "zfs", "set", k+"="+v, pool)
	}
	s.Calls = nil
	s.PoolSize = 9 << 30 // the model's PoolFree
	return SimRunner{s}
}

// Run marks commands and options the simulator does not implement
// (rename, destroy -R) as
// unmodelled (exit 3), so a comparison stops there instead of reporting
// the gap at every step.
func (s SimRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := s.Sim.Run(ctx, name, args...)
	var ee *zfs.ExitError
	if errors.As(err, &ee) && (strings.HasPrefix(ee.Stderr, "unrecognized command") || strings.HasPrefix(ee.Stderr, "invalid option")) {
		return out, &zfs.ExitError{Code: 3, Stderr: ee.Stderr}
	}
	return out, err
}

// SendRecv uses the simulator's SendRecv helper when it has one.
func (s SimRunner) SendRecv(_ context.Context, snap, dst string) error {
	if r, ok := any(s.Sim).(interface{ SendRecv(snap, dst string) error }); ok {
		return r.SendRecv(snap, dst)
	}
	return &zfs.ExitError{Code: 3, Stderr: "simulator has no send/recv\n"}
}
