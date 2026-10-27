import sys
root = sys.argv[1]

def sub(path, pairs):
    p = root + '/' + path
    s = open(p).read()
    for old, new in pairs:
        assert old in s, (path, old[:80])
        s = s.replace(old, new, 1)
    open(p, 'w').write(s)

sub('cmd/zfs-endpoint/startup.go', [('''//   - 0: also a stop (SIGTERM) during startup, which is not a failure.
//   - 1: anything else.
const (
	exitTempFail = 75 // EX_TEMPFAIL
	exitConfig   = 78 // EX_CONFIG
)''', '''//   - 0: also a stop (SIGTERM) during startup, which is not a failure.
//   - 1: anything else.
//
// `zfs-endpoint reconcile` exits 78 for bad flags, 1 for a failed call or
// a partial apply, and, with -fail-on-findings, exitFindings for a report
// with findings (so the timer's unit fails and shows in systemctl
// --failed).
const (
	exitFindings = 3
	exitTempFail = 75 // EX_TEMPFAIL
	exitConfig   = 78 // EX_CONFIG
)''')])

sub('cmd/zfs-endpoint/reconcile.go', [
('''-min-age, and it reports drift in "skipped".
`''', '''-min-age, and it reports drift in "skipped".

The report ends with an "undesired:" line (against -max-deletions) and a
"summary:" line of counts. With -fail-on-findings it exits 3 when the
summary counts any finding (an action, or a skip that is not routine:
endpoint.FindingKind), so the timer's unit fails visibly.
`'''),
('''	timeout := fs.Duration("timeout", 5*time.Minute, "deadline for the call")''',
'''	timeout := fs.Duration("timeout", 5*time.Minute, "deadline for the call")
	failOnFindings := fs.Bool("fail-on-findings", false, fmt.Sprintf("exit %d when the summary counts a finding (the timer's unit passes it)", exitFindings))'''),
('''	printPlan(out, resp, *apply, *verbose)
	// an applied run that left a group failed or unattempted is partial:
	// exit 1 (not 78), so a wrapper does not read it as done
	if *apply {
		for _, s := range resp.GetSkipped() {
			if r := s.GetReason(); strings.HasPrefix(r, endpoint.SkipFailed) || strings.HasPrefix(r, endpoint.SkipNotAttempted) {
				return fmt.Errorf("reconcile on %s: applied in part: %s: %s", path, s.GetTarget(), r)
			}
		}
	}
	return nil
}''', '''	limit := cmp.Or(int(*maxDel), endpoint.DefaultMaxDeletions)
	if *unlimited {
		limit = 0
	}
	sum := printPlan(out, resp, *apply, *verbose, limit)
	// an applied run that left a group failed or unattempted is partial:
	// exit 1 (not 78), so a wrapper does not read it as done
	if *apply {
		for _, s := range resp.GetSkipped() {
			if r := s.GetReason(); strings.HasPrefix(r, endpoint.SkipFailed) || strings.HasPrefix(r, endpoint.SkipNotAttempted) {
				return fmt.Errorf("reconcile on %s: applied in part: %s: %s", path, s.GetTarget(), r)
			}
		}
	}
	if *failOnFindings && sum.Findings > 0 {
		return &exitError{exitFindings, fmt.Errorf("reconcile on %s: %d findings (summary: %s)", path, sum.Findings, sum)}
	}
	return nil
}'''),
('''// printPlan writes the plan (or, applied, what ran) and its digest. The
// committed volumes an empty desired set leaves unjudged are counted, not
// listed, unless verbose: on a report they say nothing but "the set was
// empty".
func printPlan(w io.Writer, r *pb.ReconcileResponse, applied, verbose bool) {''',
'''// printPlan writes the plan (or, applied, what ran) and its digest. The
// committed volumes an empty desired set leaves unjudged are counted, not
// listed, unless verbose: on a report they say nothing but "the set was
// empty". It ends with the undesired count against maxDel (0: no cap) and
// the summary line, which it returns.
func printPlan(w io.Writer, r *pb.ReconcileResponse, applied, verbose bool, maxDel int) endpoint.ReconcileSummary {'''),
('''	if unjudged > 0 {
		fmt.Fprintf(w, "  (%d committed volumes: %s; -v lists them)\\n", unjudged, endpoint.SkipEmptyDesired)
	}
''', '''	if unjudged > 0 {
		fmt.Fprintf(w, "  (%d committed volumes: %s; -v lists them)\\n", unjudged, endpoint.SkipEmptyDesired)
	}
	var acts, skips []string
	for _, a := range r.GetActions() {
		acts = append(acts, a.GetReason())
	}
	for _, s := range r.GetSkipped() {
		skips = append(skips, s.GetReason())
	}
	sum := endpoint.SummarizeReconcile(acts, skips, !applied)
	switch {
	case maxDel == 0:
		fmt.Fprintf(w, "undesired: %d (no max)\\n", sum.Undesired)
	case sum.Undesired > maxDel:
		fmt.Fprintf(w, "undesired: %d (max %d: apply would be refused)\\n", sum.Undesired, maxDel)
	default:
		fmt.Fprintf(w, "undesired: %d (max %d)\\n", sum.Undesired, maxDel)
	}
	fmt.Fprintf(w, "summary: %s\\n", sum)
'''),
('''		fmt.Fprintf(w, "to apply exactly this plan: zfs-endpoint reconcile <the same flags> -apply -digest %s\\n", r.GetPlanDigest())
	}
}''', '''		fmt.Fprintf(w, "to apply exactly this plan: zfs-endpoint reconcile <the same flags> -apply -digest %s\\n", r.GetPlanDigest())
	}
	return sum
}'''),
('import (\n\t"bufio"\n', 'import (\n\t"bufio"\n\t"cmp"\n'),
])

sub('internal/server/server.go', [('''	if err != nil {
		return nil, grpcErr(err)
	}
	if !r.GetDryRun() {
		// what was destroyed''', '''	if err != nil {
		return nil, grpcErr(err)
	}
	logReconcileResult(ctx, plan, r.GetDryRun())
	if !r.GetDryRun() {
		// what was destroyed''')])
