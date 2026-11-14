package endpoint

import (
	"context"
	"fmt"
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
// or above floor (0: off). bound is why avail is only a lower bound on
// what a new child gets ("" when exact), for the refusal to say. Pure.
func checkHeadroom(avail, need, floor uint64, what, bound string) error {
	if floor == 0 || avail >= need && avail-need >= floor {
		return nil
	}
	gets := fmt.Sprintf("gets %d bytes", avail)
	if bound != "" {
		gets = fmt.Sprintf("gets at least %d bytes (a lower bound: %s)", avail, bound)
	}
	return errorf(ResourceExhausted, "%s: a new child of the parent %s and this reserves %d, which would leave less than the %d-byte floor (-min-free-bytes)",
		what, gets, need, floor)
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
	avail, bound, err := e.childSpace(ctx, e.cfg.Parent)
	if err != nil {
		return err
	}
	return checkHeadroom(avail, need, e.cfg.MinFreeBytes, what, bound)
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
// So available - usedbyrefreservation is D exactly when no refquota caps
// available, and at most D when one does. zfs reports
// usedbyrefreservation only for a dir with the used breakdown
// (DD_FLAG_USED_BREAKDOWN, dsl_dir.c dsl_dir_stats); an older dir prints
// "-", which must not read as 0 (that would add nothing back and
// overestimate D). There the add-back is bounded by refreservation:
// available - refreservation is short of D by min(unique,
// refreservation), the parent's own data.
//
// Under a refquota D cannot be read from P; it is one level up
// (dsl_dir_space_available recursing into the parent dir):
//
//	D = min(D(parent) + max(0, reservation(P) - used(P)), quota(P) - used(P))
//
// where quota 0 is no limit. A pool root has no level up (its D is the
// pool's adjusted size less used, which zfs get does not report), and a
// level up may be unreadable: the lower bound then stands, and a refusal
// says it is one.

// parentSpace is a dataset's space properties, as the guard reads them in
// one get. breakdown: zfs reported usedbyrefreservation (urefres).
type parentSpace struct {
	avail, refres, urefres, refquota, quota, resv, used uint64
	breakdown                                           bool
}

var parentSpaceProps = []string{"available", "refreservation", "usedbyrefreservation", "refquota", "quota", "reservation", "used"}

// needsUp reports whether childAvail needs the level up: only a refquota
// hides the dir's space from available.
func (s parentSpace) needsUp() bool { return s.refquota > 0 }

// childAvail is a lower bound on the space a new child of s gets
// (dsl_dir_space_available of s's dir), and whether it is exact, given an
// exact up: the same for s's parent, when hasUp. Pure.
func childAvail(s parentSpace, up uint64, hasUp bool) (uint64, bool) {
	sub := func(a, b uint64) uint64 {
		if a < b {
			return 0
		}
		return a - b
	}
	addBack := s.refres
	if s.breakdown {
		addBack = s.urefres
	}
	low := sub(s.avail, addBack)
	if !s.needsUp() {
		return low, s.breakdown || s.refres == 0
	}
	if !hasUp {
		return low, false
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
	// d is D when up is exact, and low <= D: low above d means up was not
	return max(low, d), d >= low
}

// childSpace reads childAvail for a new child of name: one get, and one
// more per level up only while a refquota hides the dir's space. bound
// says why the result is only a lower bound ("" when exact). A level up
// that cannot be read leaves the lower bound in hand, not an error.
func (e *Endpoint) childSpace(ctx context.Context, name string) (uint64, string, error) {
	d, err := e.zfs.Get(ctx, name, parentSpaceProps...)
	if zfs.IsNotFound(err) && name == e.cfg.Parent {
		return 0, "", parentUnavailable(name, err)
	}
	if err != nil {
		return 0, "", zfsErr(err, "inspect parent")
	}
	var s parentSpace
	for _, f := range []struct {
		prop string
		v    *uint64
	}{{"available", &s.avail}, {"refreservation", &s.refres}, {"usedbyrefreservation", &s.urefres}, {"refquota", &s.refquota}, {"quota", &s.quota}, {"reservation", &s.resv}, {"used", &s.used}} {
		if *f.v, err = d.Props[f.prop].Uint(); err != nil {
			return 0, "", errorf(Internal, "%s: %s %q: %v", name, f.prop, d.Props[f.prop].Value, err)
		}
	}
	// "-" (no used breakdown) and an absent value both read as 0 above
	v := d.Props["usedbyrefreservation"].Value
	s.breakdown = v != "-" && v != ""
	up, hasUp, upBound := uint64(0), false, ""
	if s.needsUp() {
		if i := strings.LastIndexByte(name, '/'); i < 0 {
			upBound = name + " is a pool root with a refquota, which caps its own available but not its children's, and zfs does not report theirs"
		} else if u, b, err := e.childSpace(ctx, name[:i]); err != nil {
			upBound = fmt.Sprintf("%s has a refquota and reading the level up '%s' failed: %v", name, name[:i], err)
		} else {
			up, hasUp, upBound = u, true, b
		}
	}
	n, exact := childAvail(s, up, hasUp)
	switch {
	case exact && upBound == "":
		return n, "", nil
	case upBound != "":
		return n, upBound, nil
	case !s.breakdown:
		return n, name + " reports no usedbyrefreservation, so all of its refreservation is taken as unused", nil
	}
	return n, name + "'s level up reads below its own available", nil
}
