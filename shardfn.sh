nfs='^(TestFileExport|TestNFSReplay$)'
[ "${ZFSDIFF_FULL:-}" = 1 ] && nfs='^(TestFileExport|TestNFSReplay$|TestZFSDiffReplay$)'
shard() {
  awk -v nfs="$nfs" 'FNR == NR { if ($1 !~ /^#/ && NF == 2) w[$1] = $2; next }
       /^(Test|Fuzz|Example)/ { g = ($1 ~ nfs) ? "nfs" : $1
                 wt[g] += ($1 in w) ? w[$1] : 0.1; m[g] = m[g] " " $1 }
       END { for (g in wt) printf "%.3f%s\n", wt[g], m[g] }' "$2" "$1" |
    sort -k1,1gr -k2,2 |
    awk -v n="$3" '{ b = 0; for (i = 1; i < n; i++) if (load[i] + 0 < load[b] + 0) b = i
                     load[b] += $1; for (j = 2; j <= NF; j++) print b, $j }'
}
shard "$1" ci/real-zfs-weights.txt 3 > /tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/plan.txt
awk "FNR==NR{if(\$1!~/^#/&&NF==2)w[\$1]=\$2;next}{l[\$1]+=(\$2 in w)?w[\$2]:0.1}END{for(i in l)print i,l[i]}" ci/real-zfs-weights.txt /tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/plan.txt
