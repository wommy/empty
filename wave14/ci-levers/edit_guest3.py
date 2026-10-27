import sys
p = sys.argv[1]
s = open(p).read()

old = '''# shard <list> <weights> <n>: "<shard> <test>" per top-level test, fuzz
# target (its seed corpus) and example, as check-skips counts them; longest
# first onto the lightest shard
nfs='^(TestFileExport|TestNFSReplay$)'
[ "${ZFSDIFF_FULL:-}" = 1 ] && nfs='^(TestFileExport|TestNFSReplay$|TestZFSDiffReplay$)'
shard() {
  awk -v nfs="$nfs" 'FNR == NR { if ($1 !~ /^#/ && NF == 2) w[$1] = $2; next }
       /^(Test|Fuzz|Example)/ { g = ($1 ~ nfs) ? "nfs" : $1
                 wt[g] += ($1 in w) ? w[$1] : 0.1; m[g] = m[g] " " $1 }
       END { for (g in wt) printf "%.3f%s\\n", wt[g], m[g] }' "$2" "$1" |
    sort -k1,1gr -k2,2 |
    awk -v n="$3" '{ b = 0; for (i = 1; i < n; i++) if (load[i] + 0 < load[b] + 0) b = i
                     load[b] += $1; for (j = 2; j <= NF; j++) print b, $j }'
}
'''
new = '''# shard <list> <weights> <n>: "<shard> <test>" per top-level test, fuzz
# target (its seed corpus) and example, as check-skips counts them; longest
# first onto the lightest shard. A split test (split=) runs in every shard,
# each on its own part of its subtests (TestPropertyRoundTrip reads
# PROPRT_SHARD=<i>/<n>, set below), so its weight goes 1/n to each shard
# before the rest are placed
nfs='^(TestFileExport|TestNFSReplay$)'
[ "${ZFSDIFF_FULL:-}" = 1 ] && nfs='^(TestFileExport|TestNFSReplay$|TestZFSDiffReplay$)'
split='^TestPropertyRoundTrip$'
shard() {
  awk -v nfs="$nfs" -v sp="$split" 'FNR == NR { if ($1 !~ /^#/ && NF == 2) w[$1] = $2; next }
       /^(Test|Fuzz|Example)/ { x = ($1 in w) ? w[$1] : 0.1
                 if ($1 ~ sp) { printf "S %.3f %s\\n", x, $1; next }
                 g = ($1 ~ nfs) ? "nfs" : $1
                 wt[g] += x; m[g] = m[g] " " $1 }
       END { for (g in wt) printf "%.3f%s\\n", wt[g], m[g] }' "$2" "$1" |
    sort -k1,1gr -k2,2 |
    awk -v n="$3" '$1 == "S" { for (i = 0; i < n; i++) { load[i] += $2 / n; print i, $3 }; next }
                   { g[++k] = $0 }
                   END { for (x = 1; x <= k; x++) {
                           c = split(g[x], f, " "); b = 0
                           for (i = 1; i < n; i++) if (load[i] + 0 < load[b] + 0) b = i
                           load[b] += f[1]; for (j = 2; j <= c; j++) print b, f[j] } }'
}
'''
assert s.count(old) == 1
s = s.replace(old, new)

old = '''for i in $(seq 0 $((ep_shards - 1))); do
  re=$(awk -v s="$i" '$1 == s { printf "%s%s", sep, $2; sep = "|" }' "$out/endpoint.plan")
  [ -n "$re" ] || continue
  run "endpoint.$i" "zep$i" ./endpoint.test "${t[@]}" -test.run "^($re)\\$"
done
'''
new = '''# -test.parallel=8 for the endpoint shards: their parallel tests wait on
# zfs(8) and the kernel more than on CPU, and a shard's solo tail ran 4 at a
# time on 4 vCPUs (run 40494213342); the csi shards are mostly sequential
for i in $(seq 0 $((ep_shards - 1))); do
  re=$(awk -v s="$i" '$1 == s { printf "%s%s", sep, $2; sep = "|" }' "$out/endpoint.plan")
  [ -n "$re" ] || continue
  run "endpoint.$i" "zep$i" env PROPRT_SHARD="$i/$ep_shards" \\
    ./endpoint.test "${t[@]}" -test.parallel=8 -test.run "^($re)\\$"
done
'''
assert s.count(old) == 1
s = s.replace(old, new)

old = '''# Shards: the endpoint binary split three ways by ci/real-zfs-weights.txt
'''
new = '''# Shards: the endpoint binary split three ways by ci/real-zfs-weights.txt,
# with TestPropertyRoundTrip in all three, each running a third of its rows
# (PROPRT_SHARD; ci/check-split.sh checks that every row ran exactly once)
'''
assert s.count(old) == 1
s = s.replace(old, new)
open(p, 'w').write(s)
