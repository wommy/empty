package endpoint

import "testing"

// Every skip reason has a kind of its own: "refused" is left to
// PlanDeleteVolume's refusals, whose texts vary.
func TestSkipKinds(t *testing.T) {
	t.Parallel()
	for _, r := range []string{
		SkipTooYoung, SkipNoCreation, SkipEmptyDesired, SkipDesiredMissing, SkipUnmanagedSource,
		SkipUnstampedCloneSource, SkipUnaddressable, SkipFailed + ": x", SkipDependentFailed,
		SkipNotAttempted + ": context canceled", SkipBusy, SkipChangedSinceScan, SkipLegacyNFSClients,
		SkipNFSServerNotLocal, SkipNFSServerUnadvertised, SkipNFSBelowFloor + ": 10.0.0.0/8 rw",
		SkipMountpointNotPinned, SkipUncommittedExported,
	} {
		if k := SkipKind(r); k == "refused" {
			t.Errorf("%q has no kind", r)
		}
	}
	if k := SkipKind(SkipDependentFailed); k != "dependent_failed" {
		t.Errorf("SkipDependentFailed is %q, not not_attempted", k)
	}
	if k := SkipKind("volume b has dependent clone c"); k != "refused" {
		t.Errorf("a refusal is %q", k)
	}
	s := SummarizeReconcile([]string{ReasonUncommitted, ReasonWithVolume, ReasonUndesired, ReasonOrphanCloneSource},
		[]string{SkipTooYoung, SkipBusy, SkipUncommittedExported, SkipNFSBelowFloor + ": x", "volume b has snapshot b@s"}, true)
	want := "uncommitted=1 undesired=1 orphan_clone_sources=1 exposures=2 findings=6 skipped=5 skipped_busy=1 skipped_nfs_below_floor=1 skipped_refused=1 skipped_too_young=1 skipped_uncommitted_exported=1"
	if got := s.String(); got != want {
		t.Errorf("summary\n got %s\nwant %s", got, want)
	}
	// applied: the actions are done, not findings
	if s := SummarizeReconcile([]string{ReasonUncommitted}, nil, false); s.Findings != 0 || s.Uncommitted != 1 {
		t.Errorf("applied summary %+v", s)
	}
}
