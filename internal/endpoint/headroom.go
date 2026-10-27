package endpoint

import (
	"context"
	"strconv"
	"strings"

	"github.com/wommy/empty/internal/zfs"
)

// The headroom guard (Config.MinFreeBytes, -min-free-bytes) refuses a plan
// that makes a volume, or reserves space, when the space a new child of
// the parent gets (childSpace, not the parent's own available) after it
// would fall below the floor: ResourceExhausted, before
// anything is applied. It keeps a pool shared with other datasets from
// being filled to the last byte by provisioning, where every later write
// (zfs's own metadata included) fails.
//
// It is advisory, not a reservation. The space is read once, under the
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
			// a clone starts with its origin snapshot's volsize, which a
			// volume expanded since no longer has, and no reservation; only
			// the snapshot this plan cuts is absent, and it is cut from the
			// volume as it stands
			after = space{}
			rel := strings.TrimPrefix(op.Source, p.Parent+"/")
			if snap, ok := t.Get(rel); ok && snap.Name == op.Source {
				after.volsize = snap.Volsize
			} else if vol, _, ok := strings.Cut(rel, "@"); ok {
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
	return errorf(ResourceExhausted, "%s: a new child of the parent gets %d bytes and this reserves %d, which would leave less than the %d-byte floor (-min-free-bytes)",
		what, avail, need, floor)
}

// headroom applies the guard to plan, made from t: it reads the parent's
// space only when the guard is on and the plan makes a dataset or
// reserves space.
func (e *Endpoint) headroom(ctx context.Context, t Table, plan Plan, what string) error {
	if e.cfg.MinFreeBytes == 0 {
		return nil
	}
	need, creates := e.plan.planReserves(t, plan)
	if need == 0 && !creates {
		return nil
	}
	avail, err := e.childSpace(ctx, e.cfg.Parent)
	if err != nil {
		return err
	}
	return checkHeadroom(avail, need, e.cfg.MinFreeBytes, what)
}

// What a new child of the parent can get is not the parent's available.
// OpenZFS (dsl_dataset.c dsl_dataset_space, dsl_dir.c
// dsl_dir_space_available), for a dataset P whose dsl_dir has space D:
//
//	available(P) = D + max(0, refreservation(P) - unique(P))
//	               capped at refquota(P) - referenced(P) when refquota is set
//	child space  = D (a new child, without reservation or quota of its own,
//	               gets its parent dir's dsl_dir_space_available)
//
// max(0, refreservation - unique) is P's usedbyrefreservation, charged to
// P's dir (dsl_dataset_set_refreservation_sync_impl, DD_USED_REFRSRV), so
// D already excludes it and available(P) adds it back for P alone. The
// refquota cap limits what P itself references, not its children.
//
// unique is not a property and the simulator has no usedbyrefreservation,
// so the add-back is bounded by refreservation: available -
// refreservation is at most D, short of it by min(unique, refreservation),
// the parent's own data. That holds capped or not. Under a refquota D
// cannot be read from P at all; it is one level up (dsl_dir_space_available
// recursing into the parent dir):
//
//	D = min(D(parent) + max(0, reservation(P) - used(P)), quota(P) - used(P))
//
// where quota 0 is no limit. A pool root has no level up; the bound stands.

// parentSpace is a dataset's space properties, as the guard reads them in
// one get.
type parentSpace struct {
	avail, refres, refquota, quota, resv, used uint64
}

var parentSpaceProps = []string{"available", "refreservation", "refquota", "quota", "reservation", "used"}

// needsUp reports whether childAvail needs the level up: only a refquota
// hides the dir's space from available.
func (s parentSpace) needsUp() bool { return s.refquota > 0 }

// childAvail is a lower bound on the space a new child of s gets
// (dsl_dir_space_available of s's dir), exact when s has no data of its
// own under a refreservation. up is the same for s's parent, when hasUp.
// Pure.
func childAvail(s parentSpace, up uint64, hasUp bool) uint64 {
	sub := func(a, b uint64) uint64 {
		if a < b {
			return 0
		}
		return a - b
	}
	low := sub(s.avail, s.refres)
	if !s.needsUp() || !hasUp {
		return low
	}
	d := up
	if extra := sub(s.resv, s.used); d+extra >= d {
		d += extra
	} else {
		d = ^uint64(0)
	}
	if s.quota > 0 {
		d = min(d, sub(s.quota, s.used))
	}
	return max(low, d)
}

// childSpace reads childAvail for a new child of name: one get, and one
// more per level up only while a refquota hides the dir's space.
func (e *Endpoint) childSpace(ctx context.Context, name string) (uint64, error) {
	d, err := e.zfs.Get(ctx, name, parentSpaceProps...)
	if zfs.IsNotFound(err) && name == e.cfg.Parent {
		return 0, parentUnavailable(name, err)
	}
	if err != nil {
		return 0, zfsErr(err, "inspect parent")
	}
	var s parentSpace
	for _, f := range []struct {
		prop string
		v    *uint64
	}{{"available", &s.avail}, {"refreservation", &s.refres}, {"refquota", &s.refquota}, {"quota", &s.quota}, {"reservation", &s.resv}, {"used", &s.used}} {
		if *f.v, err = d.Props[f.prop].Uint(); err != nil {
			return 0, errorf(Internal, "%s: %s %q: %v", name, f.prop, d.Props[f.prop].Value, err)
		}
	}
	up, hasUp := uint64(0), false
	if i := strings.LastIndexByte(name, '/'); i > 0 && s.needsUp() {
		if up, err = e.childSpace(ctx, name[:i]); err != nil {
			return 0, err
		}
		hasUp = true
	}
	return childAvail(s, up, hasUp), nil
}
