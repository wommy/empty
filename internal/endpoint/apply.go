package endpoint

import (
	"context"
	"errors"

	"github.com/wommy/empty/internal/zfs"
)

// apply runs a plan in order. Each op's tolerance makes replays converge:
// a retry after a partial failure re-plans from a fresh scan and re-applies.
func (e *Endpoint) apply(ctx context.Context, plan Plan) error {
	for _, op := range plan {
		var err error
		switch op.Kind {
		case OpCreateFS:
			// never -p: `zfs create -p` exits 0 on an existing dataset, which
			// would let us stamp ownership onto someone else's
			err = e.zfs.CreateFilesystem(ctx, op.Target, op.Props, false)
		case OpCreateVol:
			err = e.zfs.CreateVolume(ctx, op.Target, op.Size, op.Sparse, op.Props)
		case OpSnapshot:
			err = e.zfs.Snapshot(ctx, op.Target, op.Props)
		case OpClone:
			err = e.zfs.Clone(ctx, op.Source, op.Target, op.Props)
		case OpSet:
			err = e.zfs.Set(ctx, op.Target, op.Props)
		case OpDestroy:
			err = e.zfs.Destroy(ctx, op.Target, zfs.DestroyOptions{Recursive: op.Recursive, Defer: op.Defer})
		}
		switch {
		case err == nil:
		case op.OkExists && errors.Is(err, zfs.ErrExists):
		case op.OkMissing && zfs.IsNotFound(err):
		case op.Kind == OpClone && zfs.IsNotFound(err):
			return &Error{Code: NotFound, Msg: "clone source " + op.Source + " not found", Err: err}
		default:
			return zfsErr(err, opName(op.Kind)+" "+op.Target)
		}
	}
	return nil
}

func opName(k OpKind) string {
	switch k {
	case OpCreateFS, OpCreateVol:
		return "create"
	case OpSnapshot:
		return "snapshot"
	case OpClone:
		return "clone"
	case OpSet:
		return "set"
	case OpDestroy:
		return "destroy"
	}
	return "op"
}
