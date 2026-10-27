# Per top-level test times from `go test -test.v` output, one TSV row each:
#   shard test status parallel own_s cost_s wall_s subtests
# own_s    the test's own result line. A test whose subtests call t.Parallel
#          reports only its sequential part there (0.00s for a pure fan-out).
# cost_s   own_s plus every parallel (PAUSEd) descendant's own time: the
#          test-seconds it puts on the shard, parallel subtests summed.
# wall_s   first "=== RUN" (or "=== CONT", for a parallel test) to its result
#          line, parallel subtests included; "-" without timestamps.
# parallel 1 when the top-level test itself called t.Parallel.
# Lines may carry a timestamp prefix "@<unix seconds> " (ci/real-zfs-guest.sh
# adds one); plain logs give every column but wall_s.
#
# usage: awk -v shard=<name> -f ci/test-times.awk <log>
# Plain POSIX awk: the guest has only mawk.
{
  ts = ""
  if ($1 ~ /^@[0-9]+(\.[0-9]+)?$/) {
    ts = substr($1, 2) + 0
    sub(/^@[^ ]* /, "")
  }
  # a result line glued to unterminated output (as ci/check-skips.sh allows)
  if ($1 != "---" && match($0, /--- (PASS|FAIL|SKIP): /)) $0 = substr($0, RSTART)
}
$1 == "===" && ($2 == "RUN" || $2 == "PAUSE" || $2 == "CONT") {
  n = $3
  if ($2 == "RUN" && !(n in seen)) {
    seen[n] = 1
    if (ts != "") start[n] = ts
  }
  if ($2 == "PAUSE") paused[n] = 1
  # a parallel test's wall starts when it resumes, not while it waits
  if ($2 == "CONT" && ts != "" && !(n in resumed)) {
    resumed[n] = 1
    start[n] = ts
  }
  next
}
$1 == "---" && $2 ~ /^(PASS|FAIL|SKIP):$/ {
  n = $3
  d = $4
  gsub(/[()s]/, "", d)
  status[n] = substr($2, 1, length($2) - 1)
  dur[n] = d + 0
  if (ts != "") end[n] = ts
  if (index(n, "/") == 0 && !(n in top)) {
    top[n] = 1
    tops[++ntop] = n
  }
}
END {
  for (n in dur) {
    if (index(n, "/") == 0) continue
    t = n
    sub(/\/.*/, "", t)
    nsub[t]++
    # a parallel subtest's time is not inside its parent's result line
    if (n in paused) extra[t] += dur[n]
  }
  for (i = 1; i <= ntop; i++) {
    t = tops[i]
    wall = ((t in start) && (t in end)) ? sprintf("%.3f", end[t] - start[t]) : "-"
    printf "%s\t%s\t%s\t%d\t%.3f\t%.3f\t%s\t%d\n", shard, t, status[t], (t in paused) ? 1 : 0,
      dur[t], dur[t] + extra[t], wall, nsub[t] + 0
  }
}
