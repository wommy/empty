#!/usr/bin/env bash
# the guest's stamp pipeline on the simulator, then ci/test-times.awk
set -uo pipefail
here=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave13/cw
repo=/home/user/empty/.claude/worktrees/agent-a53361ecab5fd8058
stamp() {
  local l
  while IFS= read -r l; do printf '@%s %s\n' "$EPOCHREALTIME" "$l"; done
}
marks='=== (RUN|PAUSE|CONT) |--- (PASS|FAIL|SKIP): '
cd "$here/bin"
./endpoint.test -test.count=1 -test.v -test.timeout=4m -test.run '^(TestCrashAtEveryMutationConverges|TestPropertyRoundTrip|TestPagination|TestReconcileAgainstDesiredSet|TestExpand)$' 2>&1 |
  tee "$here/st.log" | grep --line-buffered -E "$marks" | stamp >"$here/st.marks"
echo "rc=${PIPESTATUS[0]}"
mawk -v shard=endpoint.0 -f "$repo/ci/test-times.awk" "$here/st.marks"
echo "-- same log, no stamps:"
mawk -v shard=endpoint.0 -f "$repo/ci/test-times.awk" "$here/st.log"
tail -n 3 "$here/st.log"
