#!/bin/bash
# Per-test durations of the tests this batch added, and the already-fixed checks.
W=/home/user/empty/.claude/worktrees/agent-a1ef6cfe663da83d2
E=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave13/secbatch
cd "$W"
NEW='^(TestCallWithoutDeadlineGetsOne|TestHandlerPanicIsInternalAndTheDaemonServes|TestWholeParentScansAreBounded|TestNamesAndPropsNeverBecomeOptions|TestSnapshotComponentNeverDots|TestErrorsDoNotEchoUnboundedInput|TestStderrIsBounded|TestLoggedAndReturnedInputIsBounded|TestReconcileBoundsItsOwnAnswer|TestReconcileRefusesAnOversizedDesiredSet|TestChildRunsInTheCLocale|TestParseEtabSurvivesALongLine|TestSocketNeverSharesTheLockDirectory|TestDefaultSocketHasItsOwnDirectory|TestPeerCredentialsAreLogged|TestEveryMethodHasARole|TestRolesPerListener|TestAdminPeerIsRootOrTheDaemonsUser|TestAdminRPCsOnlyOnTheAdminSocket|TestAdminSocketRefusals|TestUnitAdminSocketIsApart|TestExposedByAncestor|TestStartupRefusesACrossmntAncestor|TestUnitLint|TestFilesystemsMountWithoutSetuidExecOrDevices)$'
FIXED='^(TestShortDeadlineStillTakesAFreeLock|TestPlanCreateDoesNotAdoptUnnamedManaged|TestSocketPathFitsSunPath|TestDefaultSocketLimit|TestStartupConfigRefusals|TestLockParentDoesNotFollowSymlinks|TestStopDuringStartupIsClean|TestListenSharedRelaxesOnlyTheDirectoryMode)$'
go test -count=1 -v -run "$NEW" ./... 2>&1 | grep -E '^(--- |ok|FAIL)' > "$E/new-tests-times.txt"
go test -count=1 -race -v -run "$NEW" ./... 2>&1 | grep -E '^(--- |ok|FAIL)' > "$E/new-tests-times-race.txt"
go test -count=1 -v -run "$FIXED" ./... 2>&1 | grep -E '^(--- |ok|FAIL)' > "$E/already-fixed.txt"
echo "new tests: $(grep -c '^--- PASS' "$E/new-tests-times.txt") pass, $(grep -c '^--- FAIL' "$E/new-tests-times.txt") fail"
awk '/^--- PASS/ {gsub(/[()s]/,"",$4); t+=$4} END {printf "sum of top-level durations: %.2fs\n", t}' "$E/new-tests-times.txt"
awk '/^--- PASS/ {gsub(/[()s]/,"",$4); t+=$4} END {printf "race: sum of top-level durations: %.2fs\n", t}' "$E/new-tests-times-race.txt"
sort -t'(' -k2 -rn "$E/new-tests-times-race.txt" | grep '^--- ' | head -5
echo "already fixed:"; cat "$E/already-fixed.txt"
