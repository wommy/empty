#!/usr/bin/env bash
# The endpoint suite's shard plan: which shard runs each top-level test
# (Test, Fuzz, Example) of the endpoint binary's -test.list. A pure
# function of the list, ci/real-zfs-weights.txt and the part and shard
# counts, so every VM job of a run that splits the suite (ci.yml: endpoint
# parts 0/2 and 1/2, ADR 0005) computes the same plan from the same commit
# and runs its own part of it. ci/check-split.sh recomputes the plan,
# proves it a partition of the list over every part, and proves that its
# own part ran exactly its shards' tests: so the parts together run every
# test once, without a job that waits for both.
#
# usage: ci/real-zfs-plan.sh <test list> <weights> <parts> <shards per part>
#   ci/real-zfs-plan.sh "$work/endpoint.list" ci/real-zfs-weights.txt 2 3
# prints "<shard> <test>" per test, shards numbered 0..parts*shards-1, part
# p running shards p*<shards per part> .. (p+1)*<shards per part>-1. A split
# test (TestPropertyRoundTrip) is printed once per shard, each shard running
# the rows i mod <parts*shards> == shard (PROPRT_SHARD). Two levels, each
# longest first onto the lightest (ties: the lowest number): the tests onto
# the parts, then each part's onto its shards, after the split test's equal
# share of each. The parts get equal test-seconds, which is what a
# CPU-bound guest's phase follows (ADR 0005); a test missing from the
# weights weighs 0.1s. The NFS tests (TestFileExport*, TestNFSReplay; with
# ZFSDIFF_FULL=1, TestZFSDiffReplay too) stay in one shard: one nfsd, one
# export table (ci/real-zfs-guest.sh). Only the balance depends on the
# weights, never the coverage. The loads go to stderr.
set -euo pipefail

list=${1:?test list}
weights=${2:?weights}
parts=${3:?parts}
per=${4:?shards per part}
for v in "$parts" "$per"; do
  case $v in '' | *[!0-9]* | 0*) echo "real-zfs-plan: parts and shards per part must be positive integers, got $parts $per" >&2; exit 2 ;; esac
done

nfs='^(TestFileExport|TestNFSReplay$)'
[ "${ZFSDIFF_FULL:-}" = 1 ] && nfs='^(TestFileExport|TestNFSReplay$|TestZFSDiffReplay$)'
split='^TestPropertyRoundTrip$'
# LC_ALL=C: the same order and float parsing on every runner
export LC_ALL=C
awk -v nfs="$nfs" -v sp="$split" 'FNR == NR { if ($1 !~ /^#/ && NF == 2) w[$1] = $2; next }
     /^(Test|Fuzz|Example)/ { if ($1 in done) next; done[$1] = 1
               x = ($1 in w) ? w[$1] : 0.1
               if ($1 ~ sp) { printf "S %.3f %s\n", x, $1; next }
               g = ($1 ~ nfs) ? "nfs" : $1
               wt[g] += x; m[g] = m[g] " " $1 }
     END { for (g in wt) printf "%.3f%s\n", wt[g], m[g] }' "$weights" "$list" |
  sort -k1,1gr -k2,2 |
  awk -v P="$parts" -v S="$per" '
    function lightest(l, lo, hi,   b, i) { b = lo; for (i = lo + 1; i < hi; i++) if (l[i] + 0 < l[b] + 0) b = i; return b }
    BEGIN { N = P * S }
    $1 == "S" { sp[++ns] = $3; for (p = 0; p < P; p++) pl[p] += $2 / P; for (i = 0; i < N; i++) sl[i] += $2 / N; next }
    { g[++k] = $0 }
    END {
      for (x = 1; x <= ns; x++) for (i = 0; i < N; i++) print i, sp[x]
      for (x = 1; x <= k; x++) {
        c = split(g[x], f, " ")
        p = lightest(pl, 0, P); pl[p] += f[1]
        b = lightest(sl, p * S, p * S + S); sl[b] += f[1]
        for (j = 2; j <= c; j++) print b, f[j]
      }
      for (p = 0; p < P; p++) {
        line = sprintf("part %d/%d: %.2f s:", p, P, pl[p])
        for (i = p * S; i < p * S + S; i++) line = line sprintf(" shard %d %.2f", i, sl[i])
        print line >"/dev/stderr"
      }
    }'
