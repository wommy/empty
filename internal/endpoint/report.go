package endpoint

import (
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
)

// ReconcileSummary counts a Reconcile answer: the line an operator (and a
// journald query, ADR 0006) reads instead of the whole plan. The CLI
// prints it as "summary:"; the endpoint logs it as "reconcile result".
type ReconcileSummary struct {
	// actions, by reason: one per volume or orphan collected
	Uncommitted        int
	Undesired          int
	OrphanCloneSources int
	// Exposures are skips of volumes served to NFS clients the endpoint
	// would not grant today: an uncommitted create still exported, a
	// network broader than -nfs-min-prefix
	Exposures int
	// Skipped counts the skips by SkipKind.
	Skipped map[string]int
	// Findings is what an operator should look at: every action, and every
	// skip whose kind is not routine (FindingKind).
	Findings int
}

// SkipKind is a short, stable key for a skip reason: the reason's
// constant, or "refused" for a PlanDeleteVolume refusal (CSI snapshots,
// dependent clones, held or foreign snapshots, datasets below a volume).
func SkipKind(reason string) string {
	if k, ok := skipKinds[reason]; ok {
		return k
	}
	for _, p := range []struct{ prefix, kind string }{
		{SkipFailed + ":", "failed"},
		{SkipNotAttempted + ":", "not_attempted"},
		{SkipNFSBelowFloor, "nfs_below_floor"},
	} {
		if strings.HasPrefix(reason, p.prefix) {
			return p.kind
		}
	}
	return "refused"
}

var skipKinds = map[string]string{
	SkipTooYoung:              "too_young",
	SkipNoCreation:            "no_creation",
	SkipEmptyDesired:          "empty_desired",
	SkipDesiredMissing:        "desired_missing",
	SkipUnmanagedSource:       "unmanaged_source",
	SkipUnstampedCloneSource:  "unstamped_clone_source",
	SkipUnaddressable:         "unaddressable",
	SkipDependentFailed:       "dependent_failed",
	SkipBusy:                  "busy",
	SkipChangedSinceScan:      "changed",
	SkipLegacyNFSClients:      "legacy_nfs_clients",
	SkipNFSServerNotLocal:     "nfs_server_not_local",
	SkipNFSServerUnadvertised: "nfs_server_unadvertised",
	SkipMountpointNotPinned:   "mountpoint_not_pinned",
	SkipUncommittedExported:   "uncommitted_exported",
}

// routine are the skip kinds that are no finding: they say a volume is
// young, in flight, outside an empty set's judgement, or not the
// endpoint's at all.
var routine = map[string]bool{
	"too_young": true, "empty_desired": true, "desired_missing": true,
	"busy": true, "changed": true, "unmanaged_source": true, "unstamped_clone_source": true,
}

// FindingKind reports whether a skip of this kind is a finding.
func FindingKind(kind string) bool { return !routine[kind] }

// SummarizeReconcile counts the reasons of a Reconcile answer's actions
// and skips. countActions false (an applied run: they are done) counts
// the actions but leaves them out of Findings.
func SummarizeReconcile(actions, skips []string, countActions bool) ReconcileSummary {
	s := ReconcileSummary{Skipped: map[string]int{}}
	for _, r := range actions {
		switch r {
		case ReasonUncommitted:
			s.Uncommitted++
		case ReasonUndesired:
			s.Undesired++
		case ReasonOrphanCloneSource:
			s.OrphanCloneSources++
		}
	}
	if countActions {
		s.Findings = s.Uncommitted + s.Undesired + s.OrphanCloneSources
	}
	for _, r := range skips {
		k := SkipKind(r)
		s.Skipped[k]++
		if k == "uncommitted_exported" || k == "nfs_below_floor" {
			s.Exposures++
		}
		if FindingKind(k) {
			s.Findings++
		}
	}
	return s
}

// String is the summary as one line of key=value counts, skip kinds
// sorted.
func (s ReconcileSummary) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "uncommitted=%d undesired=%d orphan_clone_sources=%d exposures=%d findings=%d skipped=%d",
		s.Uncommitted, s.Undesired, s.OrphanCloneSources, s.Exposures, s.Findings, s.skipped())
	for _, k := range slices.Sorted(maps.Keys(s.Skipped)) {
		fmt.Fprintf(&b, " skipped_%s=%d", k, s.Skipped[k])
	}
	return b.String()
}

func (s ReconcileSummary) skipped() int {
	n := 0
	for _, v := range s.Skipped {
		n += v
	}
	return n
}

// LogAttrs are the summary's counts as slog attributes, the skips in a
// "skipped" group.
func (s ReconcileSummary) LogAttrs() []any {
	var sk []any
	for _, k := range slices.Sorted(maps.Keys(s.Skipped)) {
		sk = append(sk, slog.Int(k, s.Skipped[k]))
	}
	return []any{
		"uncommitted", s.Uncommitted, "undesired", s.Undesired, "orphan_clone_sources", s.OrphanCloneSources,
		"exposures", s.Exposures, "findings", s.Findings, slog.Group("skipped", sk...),
	}
}
