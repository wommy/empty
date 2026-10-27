#!/usr/bin/env bash
# Regenerate ci/real-zfs-weights.txt from the per-test times of real-ZFS VM
# runs: the test-times.tsv the guest writes (ci/test-times.awk), one per
# part of the endpoint suite. ci.yml's endpoint jobs print it into the
# test step's log on every run (the group "test-times.tsv"), and upload it
# (artifact real-zfs-report-endpoint-<p>) only when the job fails: on a
# green run the upload sat on push-to-green (ADR 0005). Either input works.
#
# usage: ci/real-zfs-weights.sh [-s <source note>] [-l <endpoint.list>] <test-times.tsv | job log>...
#   a green run, from the job logs of both endpoint parts (one file per
#   part: `gh run view <run> --log --job <job>`, or the GitHub MCP
#   get_job_logs), with the commit's list:
#     go test -c -o /tmp/e.test ./internal/endpoint && /tmp/e.test -test.list '.*' >/tmp/endpoint.list
#     ci/real-zfs-weights.sh -s "run <run>" -l /tmp/endpoint.list /tmp/<run>/ep0.log /tmp/<run>/ep1.log
#   a failed run, from its artifacts (each carries its endpoint.list):
#     gh run download <run> -p 'real-zfs-report-endpoint-*' -D /tmp/tt
#     ci/real-zfs-weights.sh -s "run <run>" /tmp/tt/*/test-times.tsv
# A job log is told by its "<timestamp>Z " line prefix, and read from its
# test-times.tsv group. A log from before that group (runs up to
# 40549402887) is read from its endpoint.<i>.log groups (the shard logs)
# through ci/test-times.awk: no timestamps, so no wall_s; nproc from its
# "on <n> vCPUs" line; no run= header, so the directory holding the log
# stands for the run: put the parts of one run in one directory.
# The list (-l, else the first file's sibling endpoint.list) drops old
# weights of tests that no longer exist; without one they stay. The parts
# of one run (the TSV header's run=<id>) are one measurement: their rows
# add up as one file's would. The old file's `# note:` lines are carried
# over (stderr says how many): drop each whose hand-set weight the new
# runs now measure.
#
# A test's weight is what it adds to its shard's wall clock:
# - a sequential test: its wall, parallel subtests included (without
#   timestamps: its own time plus its parallel subtests' time / nproc);
# - a parallel test (t.Parallel at top level): its time, parallel subtests
#   summed, / nproc, since it shares the shard's nproc slots with the
#   shard's other parallel tests.
# A test split across shards (TestPropertyRoundTrip) sums its rows within
# a run, its parts' files together, and ci/real-zfs-plan.sh gives each
# shard 1/n of it. Several runs average. Rows from a nightly run (mode=full: the whole
# zfsdiff corpus or every crash point) are skipped, since they are not what
# a PR runs. An endpoint test with no measurement keeps its old weight.
# Only the shard balance depends on the weights, never the coverage.
set -euo pipefail

src= list=
while [ $# -gt 0 ]; do
  case $1 in
    -s) src=${2:?source note}; shift 2 ;;
    -l) list=${2:?endpoint.list}; shift 2 ;;
    *) break ;;
  esac
done
[ $# -gt 0 ] || { sed -n '2,/^set -/p' "$0" | sed '$d; s/^# \{0,1\}//' >&2; exit 2; }
[ -n "$list" ] || [ ! -f "$(dirname "$1")/endpoint.list" ] || list=$(dirname "$1")/endpoint.list
here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp)
logs=$(mktemp -d)
trap 'rm -f "$tmp"; rm -rf "${logs:?}"' EXIT

# a job log as the TSV the guest wrote: its test-times.tsv group, or (an
# older log) its shard logs through ci/test-times.awk under a made header
log2tsv() {
  local f=$1 d=$2 key
  key="log:$(dirname "$f" | tr ' ' '_')"
  mkdir -p "$d"
  awk -v d="$d" '
    # the line prefix: a timestamp, after a byte order mark on line 1
    { sub(/^[^0-9]*[0-9][0-9-]*T[0-9:.]+Z /, "") }
    /^##\[group\]test-times\.tsv$/ { intsv = 1; next }
    /^##\[group\]endpoint\.[0-9]+\.log$/ { cur = $0; sub(/^##\[group\]/, "", cur); next }
    /^##\[endgroup\]/ { intsv = 0; cur = ""; next }
    intsv { print >(d "/tsv"); next }
    cur != "" { print >(d "/" cur); next }
    / on [0-9]+ vCPUs$/ { n = $0; sub(/ vCPUs$/, "", n); sub(/.* on /, "", n); print n >(d "/nproc") }
    /^ +(ZFSDIFF_FULL|ZFS_CRASH_FULL): [^ ]/ { print "" >(d "/full") }
  ' "$f"
  if [ -s "$d/tsv" ]; then
    cat "$d/tsv"
    return
  fi
  set -- "$d"/endpoint.*.log
  [ -f "$1" ] || { echo "real-zfs-weights: $f has neither a test-times.tsv group nor endpoint shard logs" >&2; return; }
  local nproc=4 mode=pr s
  [ ! -s "$d/nproc" ] || nproc=$(awk 'END { print $1 }' "$d/nproc")
  [ ! -f "$d/full" ] || mode=full
  echo "# from the shard logs of job log $f (ci/real-zfs-weights.sh)"
  echo "# nproc=$nproc mode=$mode run=$key"
  for s in "$@"; do awk -v shard="$(basename "$s" .log)" -f "$here/test-times.awk" "$s"; done
}

files=()
i=0
for f in "$@"; do
  f=$(realpath "$f")
  if awk 'NR == 1 { r = $0 ~ /^[^0-9]*[0-9][0-9-]*T[0-9:.]+Z /; exit } END { exit !r }' "$f"; then
    i=$((i + 1))
    log2tsv "$f" "$logs/$i" >"$logs/$i.tsv"
    f=$logs/$i.tsv
  fi
  files+=("$f")
done
lst=()
[ -z "$list" ] || lst=(lst="$(realpath "$list")" "$(realpath "$list")")
cd "$here/.."
out=ci/real-zfs-weights.txt

awk -v src="$src" '
  FNR == 1 { nproc = 4; full = 0; key = FILENAME }
  /^#/ {
    # hand-set weights stay explained until a human drops the line
    if (FILENAME == old) { if ($0 ~ /^# note:/) notes[++nn] = $0; next }
    if (match($0, /nproc=[0-9]+/)) nproc = substr($0, RSTART + 6, RLENGTH - 6) + 0
    # the parts of one run sum as one measurement; "local" is no run
    if (match($0, /run=[^ ]+/) && substr($0, RSTART + 4, RLENGTH - 4) != "local") key = "run " substr($0, RSTART + 4, RLENGTH - 4)
    if ($0 ~ /mode=full/) full = 1
    if (full && !(FILENAME in warned)) { warned[FILENAME] = 1; print "skipping " FILENAME ": a nightly (mode=full) run" >"/dev/stderr" }
    next
  }
  FILENAME == old {
    if (NF == 2) prev[$1] = $2
    next
  }
  FILENAME == lst {
    exists[$1] = 1
    havelist = 1
    next
  }
  full { next }
  {
    # tabs, or spaces from a log viewer: no field holds a blank
    split($0, f, /[\t ]+/)
    if (f[1] !~ /^endpoint\./) next
    t = f[2]; par = f[4] + 0; own = f[5] + 0; cost = f[6] + 0; wall = f[7]
    if (par) w = cost / nproc
    else if (wall != "-") w = wall + 0
    else w = own + (cost - own) / nproc
    # a split test (ci/real-zfs-plan.sh) has a row per shard of the run,
    # over the files of its parts: they add up within a run, runs average
    sum[t] += w
    if (!((key, t) in run)) { run[key, t] = 1; cnt[t]++ }
  }
  END {
    print "# Hand-set a weight? Add one new `# note:` line for it after the others; never extend an existing line (merges conflict on it)."
    print "# Seconds each top-level endpoint test adds to its shard on the real-ZFS"
    print "# VM: the time cost of a test as data (ADR 0005). ci/real-zfs-guest.sh"
    print "# shards the endpoint binary by these weights (longest first, onto the"
    print "# lightest shard); a test missing here weighs 0.1s. Only the balance"
    print "# depends on this file, never the coverage: every listed test runs in"
    print "# exactly one shard (a split test, every subtest: ci/check-split.sh), and"
    print "# ci/check-skips.sh fails a test that neither passed nor skipped."
    print "#"
    print "# Generated by ci/real-zfs-weights.sh from the guest'"'"'s test-times.tsv"
    print "# (sequential test: wall, parallel subtests included; parallel test:"
    print "# test-seconds / nproc). Edit by hand only with a `# note:` line; rerun the script on newer"
    print "# runs when shards drift apart (shards.txt in the job summary)."
    if (src != "") print "# Source: " src "."
    for (i = 1; i <= nn; i++) print notes[i]
    if (nn) printf "real-zfs-weights: kept %d `# note:` line(s) of the old file: drop each whose weight these runs now measure\n", nn >"/dev/stderr"
    print "#"
    print "# <test> <seconds>"
    for (t in sum) printf "%s %.2f\n", t, sum[t] / cnt[t] | "sort -k2,2gr -k1,1"
    close("sort -k2,2gr -k1,1")
    n = 0
    for (t in prev) if (keep(t)) n++
    if (n) {
      print "#"
      print "# not measured in the source runs: weights kept from the previous file"
      for (t in prev) if (keep(t)) printf "%s %s\n", t, prev[t] | "sort -k2,2gr -k1,1"
      close("sort -k2,2gr -k1,1")
    }
  }
  function keep(t) { return !(t in sum) && (!havelist || (t in exists)) }
' old="$out" "$out" "${lst[@]}" "${files[@]}" >"$tmp"
mv "$tmp" "$out"
echo "wrote $out ($(grep -cv '^#' "$out") tests)"
