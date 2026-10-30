#!/usr/bin/env bash
# Prove that the real-ZFS VM jobs together run every real-pool test binary,
# each in exactly one job (ci/real-zfs-suites.txt). Static: it reads the
# repository, not a run, so it runs in ci.yml's static job.
#
# usage: ci/check-suites.sh
#
# Fails unless
# - every package in ci/real-zfs-suites.txt is in exactly one suite, and
#   each suite has at least one package;
# - those packages are exactly the internal/ packages whose tests read
#   ZFS_TEST_POOL (a new real-pool package must join a suite);
# - ci.yml and nightly.yml each have exactly one real-zfs matrix: a
#   `suite: [...]` line listing every suite exactly once, or `include:`
#   entries `- { suite: <s>, part: <p>, parts: <P> }` that give every
#   suite parts 0..P-1 exactly once each, one P per suite, and P > 1 only
#   for endpoint (the only suite with a shard plan, ci/real-zfs-plan.sh);
# - cache-warm.yml's `ns: [...]` matrix lists real-zfs-<suite> for every
#   suite exactly once, and no other real-zfs-* namespace;
# - nightly.yml's fuzz matrix (`- { name: Fuzz..., pkg: ./... }`) lists
#   every func Fuzz* of the repository's tests exactly once, in its package;
# - ci/expected-skips.txt has a vm entry for every test (or subtest) of
#   those packages that skips on a real pool, and none for a test that is
#   gone (ci/skiplint).
# At run time each job then proves its own part: ci/check-skips.sh (every
# listed test of its binaries passed or skipped) and ci/check-split.sh
# (the endpoint plan a partition of the list over every part, each part's
# logs exactly its shards' tests, every TestPropertyRoundTrip row in
# exactly one shard of all parts).
set -euo pipefail
cd "$(dirname "$0")/.."

rc=0
err() { echo "::error::check-suites: $*"; rc=1; }
lines() { sed '/^$/d' | sort; }

file=ci/real-zfs-suites.txt
suites=$(awk '!/^#/ && NF { print $1 }' "$file" | lines)
pkgs=$(awk '!/^#/ && NF { for (i = 2; i <= NF; i++) print $i }' "$file" | lines)
[ -n "$suites" ] || err "$file lists no suite"
dup=$(uniq -d <<<"$suites")
[ -z "$dup" ] || err "$file: suite listed twice: $dup"
dup=$(uniq -d <<<"$pkgs")
[ -z "$dup" ] || err "$file: package in more than one suite: $dup"
empty=$(awk '!/^#/ && NF == 1 { print $1 }' "$file")
[ -z "$empty" ] || err "$file: suite with no package: $empty"

# the packages whose tests reach a real pool
real=$(grep -rl --include='*_test.go' 'ZFS_TEST_POOL' internal | awk -F/ '{ print $2 }' | sort -u)
[ "$real" = "$(sort -u <<<"$pkgs")" ] || {
  err "the suites' packages differ from the packages whose tests read ZFS_TEST_POOL (< suites, > real-pool packages):"
  diff <(sort -u <<<"$pkgs") <(echo "$real") || true
}

# the skip budget, statically: every test of the real-pool packages that
# skips on a real pool (calls requireSim, harness.Sim or a helper that
# does) has its ci/expected-skips.txt entry for the vm leg, and every vm
# entry names a test that exists, here rather than a VM run later
# (ci/skiplint; ci/check-skips.sh proves the same on the VM's logs)
# shellcheck disable=SC2086 # one argument per package
go run ./ci/skiplint -list ci/expected-skips.txt -leg vm $pkgs || err "ci/expected-skips.txt does not match the simulator-only tests (above)"

# matrix <workflow> <key>: the entries of every `<key>: [a, b]` line, one
# per line, prefixed with the line's number among them
matrix() {
  awk -v k="$2" '
    $1 == k ":" && /\[.*\]/ {
      n++; s = $0; sub(/^[^[]*\[/, "", s); sub(/\].*$/, "", s)
      c = split(s, f, ","); for (i = 1; i <= c; i++) { gsub(/[ "\047]/, "", f[i]); if (f[i] != "") print n, f[i] } }' "$1"
}
# parts <workflow>: "<suite> <part> <parts>" per `- { suite: s, part: p,
# parts: P }` include entry
parts() {
  awk '/^[[:space:]]*- *\{ *suite:/ {
    s = $0; sub(/^[^{]*\{/, "", s); sub(/\}.*$/, "", s); gsub(/[ "\047]/, "", s)
    c = split(s, f, ","); delete kv
    for (i = 1; i <= c; i++) { split(f[i], e, ":"); kv[e[1]] = e[2] }
    print kv["suite"], ("part" in kv) ? kv["part"] : "?", ("parts" in kv) ? kv["parts"] : "?" }' "$1"
}
for wf in .github/workflows/ci.yml .github/workflows/nightly.yml; do
  m=$(matrix "$wf" suite)
  p=$(parts "$wf")
  n=$(awk '{ print $1 }' <<<"$m" | sort -u | sed '/^$/d' | wc -l)
  if [ -n "$p" ]; then
    [ "$n" = 0 ] || { err "$wf: a suite matrix and include entries, want one of them"; continue; }
    got=$(awk '{ print $1 }' <<<"$p" | lines | sort -u)
    [ "$got" = "$suites" ] || {
      err "$wf: the include entries do not name every suite (< $file, > $wf):"
      diff <(echo "$suites") <(echo "$got") || true
    }
    bad=$(awk '$2 !~ /^[0-9]+$/ || $3 !~ /^[1-9][0-9]*$/ || $2 + 0 >= $3 + 0 { print "bad entry: " $0; next }
      { if (($1 in P) && P[$1] != $3) print $1 ": parts " P[$1] " and " $3; P[$1] = $3
        if (($1, $2) in seen) print $1 ": part " $2 " twice"; seen[$1, $2] = 1; c[$1]++
        if ($3 > 1 && $1 != "endpoint") print $1 ": split into " $3 " parts, only endpoint has a shard plan" }
      END { for (s in P) if (c[s] != P[s]) print s ": " c[s] " of " P[s] " parts" }' <<<"$p")
    [ -z "$bad" ] || err "$wf: the suite parts are not each part of each suite exactly once: $(tr '\n' ';' <<<"$bad")"
    continue
  fi
  [ "$n" = 1 ] || { err "$wf: $n suite matrices, want exactly 1"; continue; }
  got=$(awk '{ print $2 }' <<<"$m" | lines)
  [ "$got" = "$suites" ] || {
    err "$wf: the suite matrix is not every suite exactly once (< $file, > $wf):"
    diff <(echo "$suites") <(echo "$got") || true
  }
done
wf=.github/workflows/cache-warm.yml
m=$(matrix "$wf" ns)
got=$(awk '$2 ~ /^real-zfs-/ { sub(/^real-zfs-/, "", $2); print $2 }' <<<"$m" | lines)
[ "$got" = "$suites" ] || {
  err "$wf: the Go cache namespaces are not real-zfs-<suite> for every suite exactly once (< $file, > $wf):"
  diff <(echo "$suites") <(echo "$got") || true
}
[ -z "$(awk '$2 == "real-zfs"' <<<"$m")" ] || err "$wf: namespace real-zfs is gone, each suite has its own"

# nightly's fuzz matrix: every func Fuzz* of the repository's tests exactly
# once, with its package. Tracked files only (git ls-files), so a local
# worktree under the checkout is not read
wf=.github/workflows/nightly.yml
want=$(git ls-files -- '*_test.go' | xargs -r awk '/^func Fuzz[A-Za-z0-9_]*\(/ {
    n = $2; sub(/\(.*/, "", n); d = FILENAME; sub(/\/[^\/]*$/, "", d); print n, "./" d }' | sort)
got=$(awk '/^[[:space:]]*- *\{ *name: *Fuzz/ {
    s = $0; sub(/^[^{]*\{/, "", s); sub(/\}.*$/, "", s); gsub(/[ "\047]/, "", s)
    c = split(s, f, ","); delete kv
    for (i = 1; i <= c; i++) { split(f[i], e, ":"); kv[e[1]] = e[2] }
    print kv["name"], kv["pkg"] }' "$wf" | sort)
[ -n "$want" ] || err "no func Fuzz* found in the repository's tests"
[ "$want" = "$got" ] || {
  err "$wf: the fuzz matrix is not every func Fuzz* exactly once (< repository, > $wf):"
  diff <(echo "$want") <(echo "$got") || true
}

[ $rc -ne 0 ] || echo "check-suites: nightly fuzzes the $(wc -l <<<"$want") Fuzz targets"
[ $rc -ne 0 ] || echo "check-suites: $(wc -l <<<"$suites") suites ($(tr '\n' ' ' <<<"$suites")) cover the real-pool packages ($(tr '\n' ' ' <<<"$pkgs")) once each; ci.yml, nightly.yml and cache-warm.yml run each suite (each part of it) once"
exit $rc
