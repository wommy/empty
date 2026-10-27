package endpoint

import (
	"context"
	"errors"
	"fmt"

	"github.com/wommy/empty/internal/zfs"
)

// apply runs a plan in order. Each op's tolerance makes replays converge:
// a retry after a partial failure re-plans from a fresh scan and re-applies.
func (e *Endpoint) apply(ctx context.Context, plan Plan) error {
	// an unknown kind is a planner bug: refuse the plan before any op runs,
	// rather than half-apply it or skip the op as if it were done
	for i, op := range plan {
		if _, ok := opNames[op.Kind]; !ok {
			return &Error{Code: Internal, Msg: fmt.Sprintf("op %d/%d: unknown kind %v on %s", i+1, len(plan), op.Kind, op.Target)}
		}
	}
	for i, op := range plan {
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
		default:
			// unreachable after the check above; never treat it as done
			return &Error{Code: Internal, Msg: "unknown op kind " + op.Kind.String()}
		}
		switch {
		case err == nil:
		case op.OkExists && errors.Is(err, zfs.ErrExists):
		case op.OkMissing && zfs.IsNotFound(err):
		case op.Kind == OpClone && zfs.IsNotFound(err):
			return &Error{Code: NotFound, Msg: "clone source " + op.Source + " not found", Err: err}
		default:
			return opErr(err, i, plan)
		}
	}
	return nil
}

// opErr maps the failure of op i of plan by operation as well as by Kind:
//   - busy on a destroy is "in use", which CSI's DeleteVolume and
//     DeleteSnapshot report as FAILED_PRECONDITION; elsewhere it is
//     retryable (zfsErr)
//   - an unclassified failure after an earlier op of the plan already
//     changed something is Aborted, not Internal: a retry re-scans and
//     converges, while a final code would strand the half-applied plan
//     (external-provisioner never deletes what it never got). Before any
//     change it stays Internal.
func opErr(err error, i int, plan Plan) error {
	op := plan[i]
	e := zfsErr(err, fmt.Sprintf("op %d/%d %s %s", i+1, len(plan), op.Kind, op.Target)).(*Error)
	switch {
	case op.Kind == OpDestroy && errors.Is(err, zfs.ErrBusy):
		e.Code = FailedPrecondition
	case e.Code == Internal && i > 0:
		e.Code = Aborted
		e.Msg += " (earlier ops applied; a retry converges)"
	}
	return e
}
