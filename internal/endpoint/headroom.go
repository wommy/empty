package endpoint

import (
	"context"
	"strconv"
	"strings"

	"github.com/wommy/empty/internal/zfs"
)

// The headroom guard (Config.MinFreeBytes, -min-free-bytes) refuses a plan
// that makes a volume, or reserves space, when the parent's available
// space after it would fall below the floor: ResourceExhausted, before
// anything is applied. It keeps a pool shared with other datasets from
// being filled to the last byte by provisioning, where every later write
// (zfs's own metadata included) fails.
//
// It is advisory, not a reservation. available is read once, under the
// volume's lock but not the pool's: two creates of different volumes, or
// anything else writing to the pool, can each pass the check and together
// cross the floor. Only ZFS's own reservations (a thick zvol's
// refreservation, reserve on a filesystem) actually hold space; this guard
// only stops the endpoint from asking for more once the pool is low.
//
// What a plan reserves is read from the plan itself (planReserves), so a
// retry whose plan is empty (CSI idempotency) or a resume that only
// commits is never refused: a floor reached since the first attempt must
// not strand a provision half made. A thin create reserves nothing but is
// still refused below the floor: it would write to a pool already short.
// A thick zvol's real refreservation (zvol_auto_resv) is volsize plus
// metadata, so volsize is a lower bound. A snapshot of a thick volume
// also takes space outside its reservation (its referenced bytes); the
// scan does not read referenced, and snapshots are not guarded.

// planReserves is what plan newly reserves, in bytes, judged against the
// table it was made from, and whether it makes a dataset (create or
// clone). Pure.
func (p Planner) planReserves(t Table, plan Plan) (need uint64, creates bool) {
	type space struct{ volsize, refres uint64 }
	state := map[string]space{}
	at := func(target string) space {
		if s, ok := state[target]; ok {
			return s
		}
		if row, ok := t.Get(strings.TrimPrefix(target, p.Parent+"/")); ok && row.Name == target {
			return space{row.Volsize, row.Refres}
		}
		return space{}
	}
	num := func(v string) (uint64, bool) {
		n, err := strconv.ParseUint(v, 10, 64)
		return n, err == nil
	}
	for _, op := range plan {
		before := at(op.Target)
		after := before
		switch op.Kind {
		case OpCreateVol:
			creates = true
			after = space{volsize: op.Size}
			if !op.Sparse {
				after.refres = op.Size
			}
		case OpCreateFS:
			creates = true
			after = space{}
			if n, ok := num(op.Props["refreservation"]); ok {
				after.refres = n
			}
		case OpClone:
			creates = true
			// a clone starts with its origin's volsize and no reservation
			after = space{}
			if vol, _, ok := strings.Cut(strings.TrimPrefix(op.Source, p.Parent+"/"), "@"); ok {
				if src, ok := t.Get(vol); ok {
					after.volsize = src.Volsize
				}
			}
			if n, ok := num(op.Props["refreservation"]); ok {
				after.refres = n
			}
		case OpSet:
			if n, ok := num(op.Props["volsize"]); ok {
				// a thick zvol's refreservation follows volsize (auto)
				if after.refres > 0 && n > after.volsize {
					after.refres += n - after.volsize
				}
				after.volsize = n
			}
			switch v := op.Props["refreservation"]; v {
			case "":
			case "auto":
				after.refres = after.volsize
			case "none":
				after.refres = 0
			default:
				if n, ok := num(v); ok {
					after.refres = n
				}
			}
		default:
			continue
		}
		state[op.Target] = after
		if after.refres > before.refres {
			need += after.refres - before.refres
		}
	}
	return need, creates
}

// checkHeadroom is the guard's decision: nil when avail less need stays at
// or above floor (0: off). Pure.
func checkHeadroom(avail, need, floor uint64, what string) error {
	if floor == 0 || avail >= need && avail-need >= floor {
		return nil
	}
	return errorf(ResourceExhausted, "%s: the parent has %d bytes available and this reserves %d, which would leave less than the %d-byte floor (-min-free-bytes)",
		what, avail, need, floor)
}

// headroom applies the guard to plan, made from t: it reads the parent's
// available space only when the guard is on and the plan makes a dataset
// or reserves space.
func (e *Endpoint) headroom(ctx context.Context, t Table, plan Plan, what string) error {
	if e.cfg.MinFreeBytes == 0 {
		return nil
	}
	need, creates := e.plan.planReserves(t, plan)
	if need == 0 && !creates {
		return nil
	}
	d, err := e.zfs.Get(ctx, e.cfg.Parent, "available")
	if zfs.IsNotFound(err) {
		return parentUnavailable(e.cfg.Parent, err)
	}
	if err != nil {
		return zfsErr(err, "inspect parent")
	}
	avail, err := d.Props["available"].Uint()
	if err != nil {
		return errorf(Internal, "parent %s: available %q: %v", e.cfg.Parent, d.Props["available"].Value, err)
	}
	return checkHeadroom(avail, need, e.cfg.MinFreeBytes, what)
}
