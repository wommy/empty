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
	_, err := e.applyN(ctx, plan)
	return err
}

// applyN is apply that also says how many ops completed before an error.
func (e *Endpoint) applyN(ctx context.Context, plan Plan) (int, error) {
	// an unknown kind is a planner bug: refuse the plan before any op runs,
	// rather than half-apply it or skip the op as if it were done
	for i, op := range plan {
		if _, ok := opNames[op.Kind]; !ok {
			return 0, &Error{Code: Internal, Msg: fmt.Sprintf("op %d/%d: unknown kind %v on %s", i+1, len(plan), op.Kind, op.Target)}
		}
		// a destroy the plan did not tie to the dataset it inspected could
		// hit whatever holds the name by then (OWN-15)
		if op.Kind == OpDestroy && op.GUID == "" {
			return 0, &Error{Code: Internal, Msg: fmt.Sprintf("op %d/%d: destroy %s without the guid the plan read", i+1, len(plan), op.Target)}
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
			if err = e.destroy(ctx, op); errors.As(err, new(*Error)) {
				// replaced since the plan: no zfs command failed
				return i, &Error{Code: CodeOf(err), Msg: fmt.Sprintf("op %d/%d %s %s: %v", i+1, len(plan), op.Kind, op.Target, err)}
			}
		case OpMount:
			err = e.zfs.Mount(ctx, op.Target)
		case OpSetRoot:
			if err = e.setRoot(ctx, op); err != nil {
				return i, rootErr(err, i, plan)
			}
		default:
			// unreachable after the check above; never treat it as done
			return i, &Error{Code: Internal, Msg: "unknown op kind " + op.Kind.String()}
		}
		switch {
		case err == nil:
		case op.OkExists && errors.Is(err, zfs.ErrExists):
		case op.OkMissing && zfs.IsNotFound(err):
		default:
			return i, opErr(err, i, plan)
		}
	}
	return len(plan), nil
}

// destroy runs a destroy op. A busy destroy settles the host's device
// events once (Config.Settle) and is retried once: the hold may be udev
// probing a zvol device the endpoint just created. Still busy, it is the
// caller's busy (opErr: FAILED_PRECONDITION on a destroy). A failed settle
// reports the busy it was meant to clear.
//
// Each destroy runs only while the target still has the guid the plan
// read (sameDataset): renamed away and replaced by another dataset since,
// the name is no longer the plan's, and the destroy is Aborted (a retry
// re-scans and plans against what is there now). Gone, it is the
// destroy's not-found (OkMissing).
func (e *Endpoint) destroy(ctx context.Context, op Op) error {
	o := zfs.DestroyOptions{Recursive: op.Recursive, Defer: op.Defer}
	if err := e.sameDataset(ctx, op); err != nil {
		return err
	}
	err := e.zfs.Destroy(ctx, op.Target, o)
	if e.cfg.Settle == nil || !errors.Is(err, zfs.ErrBusy) {
		return err
	}
	if e.cfg.Settle(ctx) != nil {
		return err
	}
	if err := e.sameDataset(ctx, op); err != nil {
		return err
	}
	return e.zfs.Destroy(ctx, op.Target, o)
}

// sameDataset checks that op's target still has the guid the plan read.
//
// zfs has no compare-and-destroy: `zfs destroy` takes a name, and the
// guid is read by a separate command just before it. A rename and
// re-create inside that window (two administrative commands landing
// between two of ours, milliseconds apart) still goes unseen. Closing it
// takes a channel program (`zfs program`: zfs.get_prop guid, then
// zfs.sync.destroy, in one sync task), which needs root rather than a
// `zfs allow` grant. What the check closes is the window from the scan to
// the destroy, which spans the whole plan and the lock waits.
func (e *Endpoint) sameDataset(ctx context.Context, op Op) error {
	d, err := e.zfs.Get(ctx, op.Target, "guid")
	if err != nil {
		return err // a zfs failure, mapped as the destroy's
	}
	if g := d.Props["guid"].Value; g != op.GUID {
		return errorf(Aborted, "%s is not the dataset the plan inspected (guid %s, now %s): it was replaced since; retry", op.Target, op.GUID, g)
	}
	return nil
}

// setRoot runs an OpSetRoot through Config.Root.
func (e *Endpoint) setRoot(ctx context.Context, op Op) error {
	a, err := rootOf(op.Props)
	if err != nil {
		return &Error{Code: Internal, Msg: "planner bug: " + err.Error()}
	}
	return e.cfg.Root.SetRoot(ctx, op.Target, op.Path, a.uid, a.gid, a.mode)
}

// rootErr maps the failure of an OpSetRoot, op i of plan: a classified
// one (*Error, from LocalRoot) keeps its code; anything else is
// unclassified, as opErr treats it (Aborted once an earlier op applied).
func rootErr(err error, i int, plan Plan) error {
	what := fmt.Sprintf("op %d/%d %s %s", i+1, len(plan), plan[i].Kind, plan[i].Target)
	var e *Error
	if errors.As(err, &e) {
		return &Error{Code: e.Code, Msg: what + ": " + e.Msg, Err: e.Err}
	}
	code := Internal
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		code = DeadlineExceeded
	case errors.Is(err, context.Canceled):
		code = Aborted
	case i > 0:
		return &Error{Code: Aborted, Msg: what + " (earlier ops applied; a retry converges)", Err: err}
	}
	return &Error{Code: code, Msg: what, Err: err}
}

// opErr maps the failure of op i of plan through the error table
// (errRules), by operation as well as by kind: op i > 0 counts as after a
// change, since an earlier op of the plan ran.
func opErr(err error, i int, plan Plan) error {
	op := plan[i]
	msg := fmt.Sprintf("op %d/%d %s %s", i+1, len(plan), op.Kind, op.Target)
	if op.Kind == OpClone && zfs.IsNotFound(err) {
		msg = "clone source " + op.Source + " not found"
	}
	e := zfsFail(op.Kind, err, false, msg)
	if i > 0 {
		return remapMutated(e, " (earlier ops applied; a retry converges)")
	}
	return e
}
