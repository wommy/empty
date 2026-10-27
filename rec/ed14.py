import os
os.chdir('/home/user/empty/.claude/worktrees/agent-ad45c3ab7d0b822f7')
def sub(p, pairs):
    s=open(p).read()
    for old,new in pairs:
        assert s.count(old)==1, (p, old)
        s=s.replace(old,new)
    open(p,'w').write(s)

sub('api/zfsendpoint/v1/endpoint.proto', [
('''message ReconcileRequest {
  // The complete set of volume ids the caller wants kept. Every other owned,
  // committed volume is collected. Empty collects no committed volume: it
  // cannot be told apart from a caller that sent nothing.
  repeated string desired_volume_ids = 1;
  // Nothing created less than this long ago is touched (in-flight creates,
  // volumes newer than the caller's desired set). 0 means one hour.
  uint64 min_age_seconds = 2;
  // Return the plan without locking or applying it.
  bool dry_run = 3;
}''','''message ReconcileRequest {
  // The complete set of volume ids the caller wants kept. Every other owned,
  // committed volume is collected. Empty collects no committed volume: it
  // cannot be told apart from a caller that sent nothing. A set naming none
  // of the endpoint's volumes counts as empty.
  repeated string desired_volume_ids = 1;
  // Nothing created (or committed) less than this long ago is touched
  // (in-flight creates, volumes newer than the caller's desired set).
  // 0 means one hour; below 600 (ten minutes) is INVALID_ARGUMENT.
  uint64 min_age_seconds = 2;
  // Return the plan without locking or applying it.
  bool dry_run = 3;
  // At most this many committed volumes may be collected as undesired in
  // one run; a plan with more applies nothing and fails with
  // FAILED_PRECONDITION (a truncated or wrong desired set must not empty
  // the parent). 0 means 10. Uncommitted rows and orphan clone sources do
  // not count.
  uint32 max_deletions = 4;
  // Lift max_deletions. Setting both is INVALID_ARGUMENT.
  bool unlimited_deletions = 5;
  // Optional: the plan_digest of a dry run. When set, the run applies
  // only if it plans exactly what that dry run did, else ABORTED (dry-run
  // again); what runs is always a subset of it.
  string plan_digest = 6;
}'''),
('''message ReconcileResponse {
  // done (or, for a dry run, planned), in order
  repeated ReconcileAction actions = 1;
  repeated ReconcileSkip skipped = 2;
}''','''message ReconcileResponse {
  // done (or, for a dry run, planned), in order
  repeated ReconcileAction actions = 1;
  // left alone, with why; an action that failed is here with
  // "failed: <error>", and the run went on
  repeated ReconcileSkip skipped = 2;
  // digest of the plan (for a dry run, to echo in plan_digest)
  string plan_digest = 3;
}'''),
])

sub('internal/server/server.go', [
('''	plan, err := s.EP.Reconcile(ctx, endpoint.ReconcileRequest{
		DesiredVolumeIDs: r.GetDesiredVolumeIds(),
		MinAge:           time.Duration(age) * time.Second,
		DryRun:           r.GetDryRun(),
	})
	if err != nil {
		return nil, grpcErr(err)
	}
	out := &pb.ReconcileResponse{}''','''	plan, err := s.EP.Reconcile(ctx, endpoint.ReconcileRequest{
		DesiredVolumeIDs:   r.GetDesiredVolumeIds(),
		MinAge:             time.Duration(age) * time.Second,
		DryRun:             r.GetDryRun(),
		MaxDeletions:       int(r.GetMaxDeletions()),
		UnlimitedDeletions: r.GetUnlimitedDeletions(),
		PlanDigest:         r.GetPlanDigest(),
	})
	if err != nil {
		return nil, grpcErr(err)
	}
	out := &pb.ReconcileResponse{PlanDigest: plan.Digest}'''),
])

sub('internal/endpoint/reconcile.go', [
('''type ReconcileRequest struct {
	DesiredVolumeIDs []string
	// MinAge; 0 means DefaultReconcileMinAge.
	MinAge time.Duration
	// DryRun returns the plan without locking or applying it.
	DryRun bool
}''','''type ReconcileRequest struct {
	DesiredVolumeIDs []string
	// MinAge; 0 means DefaultReconcileMinAge, below MinReconcileMinAge is
	// refused.
	MinAge time.Duration
	// DryRun returns the plan without locking or applying it.
	DryRun bool
	// MaxDeletions caps the committed volumes collected as undesired in
	// one run: a plan above it applies nothing and is FailedPrecondition.
	// 0 means DefaultMaxDeletions; UnlimitedDeletions lifts the cap (both
	// set is InvalidArgument). Uncommitted rows and orphan clone sources
	// do not count: they never depend on the desired set.
	MaxDeletions       int
	UnlimitedDeletions bool
	// PlanDigest, when set, is a dry run's Digest: the run applies only if
	// its unlocked plan is exactly that one, else Aborted. The locked
	// re-plan is always a subset of the unlocked plan.
	PlanDigest string
}

// DefaultMaxDeletions is the cap on undesired volumes one Reconcile
// collects when the caller names none: enough for routine churn, small
// enough that a truncated desired set (one page of a paginated list)
// cannot empty the parent.
const DefaultMaxDeletions = 10

// Digest identifies the plan's actions (targets and reasons, in order):
// what a dry run returns for a destructive run to echo.
func (r ReconcilePlan) Digest() string {
	h := sha256.New()
	for _, a := range r.Actions {
		fmt.Fprintf(h, "%s\\x00%s\\x00%s\\n", a.Op.Kind, a.Target, a.Reason)
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

func (r ReconcilePlan) undesired() int {
	n := 0
	for _, a := range r.Actions {
		if a.Reason == ReasonUndesired {
			n++
		}
	}
	return n
}'''),
('''type ReconcilePlan struct {
	Actions []ReconcileAction
	Skipped []ReconcileSkip
''','''type ReconcilePlan struct {
	Actions []ReconcileAction
	Skipped []ReconcileSkip
	// Digest, as Reconcile returns it, is the digest of the plan made
	// before any lock (for a dry run, the plan returned).
	Digest string
'''),
('''	if r.MinAge != 0 && r.MinAge < MinReconcileMinAge {''','''	if r.MaxDeletions < 0 || r.MaxDeletions > 0 && r.UnlimitedDeletions {
		return ReconcilePlan{}, errorf(InvalidArgument, "max deletions %d with unlimited=%v", r.MaxDeletions, r.UnlimitedDeletions)
	}
	if r.MinAge != 0 && r.MinAge < MinReconcileMinAge {'''),
('''	if r.DryRun || len(plan.Actions) == 0 {
		return plan, nil
	}
''','''	plan.Digest = plan.Digest()
	if r.DryRun || len(plan.Actions) == 0 {
		return plan, nil
	}
	if r.PlanDigest != "" && r.PlanDigest != plan.Digest {
		return plan, errorf(Aborted, "the plan changed since the dry run (digest %s, now %s): dry-run again", r.PlanDigest, plan.Digest)
	}
	if limit := cmp.Or(r.MaxDeletions, DefaultMaxDeletions); !r.UnlimitedDeletions && plan.undesired() > limit {
		// nothing is applied, not even the collections that do not depend
		// on the desired set: a caller sending a wrong set should look
		return plan, errorf(FailedPrecondition, "would collect %d volumes not in the desired set, more than max deletions %d: "+
			"check the desired set with a dry run, then raise max deletions", plan.undesired(), limit)
	}
	digest := plan.Digest
'''),
('''	return e.applyReconcile(ctx, plan, func(g int) {''','''	done := e.applyReconcile(ctx, plan, func(g int) {'''),
])
s=open('internal/endpoint/reconcile.go').read()
old='''				delete(releases, v)
			}
		}
	}), nil
}'''
assert s.count(old)==1
s=s.replace(old,'''				delete(releases, v)
			}
		}
	})
	done.Digest = digest
	return done, nil
}''')
s=s.replace('''import (
	"context"
''','''import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
''',1)
open('internal/endpoint/reconcile.go','w').write(s)
