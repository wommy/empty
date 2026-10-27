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
# - ci.yml and nightly.yml each have exactly one `suite: [...]` matrix, and
#   it lists every suite exactly once;
# - cache-warm.yml's `ns: [...]` matrix lists real-zfs-<suite> for every
#   suite exactly once, and no other real-zfs-* namespace.
# At run time each job then proves its own part: ci/check-skips.sh (every
# listed test of its binaries passed or skipped) and ci/check-split.sh
# (every endpoint test in exactly one shard, every TestPropertyRoundTrip
# row in exactly one shard).
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

# matrix <workflow> <key>: the entries of every `<key>: [a, b]` line, one
# per line, prefixed with the line's number among them
matrix() {
  awk -v k="$2" '
    $1 == k ":" && /\[.*\]/ {
      n++; s = $0; sub(/^[^[]*\[/, "", s); sub(/\].*$/, "", s)
      c = split(s, f, ","); for (i = 1; i <= c; i++) { gsub(/[ "\047]/, "", f[i]); if (f[i] != "") print n, f[i] } }' "$1"
}
for wf in .github/workflows/ci.yml .github/workflows/nightly.yml; do
  m=$(matrix "$wf" suite)
  n=$(awk '{ print $1 }' <<<"$m" | sort -u | sed '/^$/d' | wc -l)
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

[ $rc -ne 0 ] || echo "check-suites: $(wc -l <<<"$suites") suites ($(tr '\n' ' ' <<<"$suites")) cover the real-pool packages ($(tr '\n' ' ' <<<"$pkgs")) once each; ci.yml, nightly.yml and cache-warm.yml run each suite once"
exit $rc
