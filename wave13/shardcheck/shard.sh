shard() {
  awk 'FNR == NR { if ($1 !~ /^#/ && NF == 2) w[$1] = $2; next }
       /^(Test|Fuzz|Example)/ { g = ($1 ~ /^(TestFileExport|TestNFSReplay$)/) ? "nfs" : $1
                 wt[g] += ($1 in w) ? w[$1] : 0.1; m[g] = m[g] " " $1 }
       END { for (g in wt) printf "%.3f%s\n", wt[g], m[g] }' "$2" "$1" |
    sort -k1,1gr -k2,2 |
    awk -v n="$3" '{ b = 0; for (i = 1; i < n; i++) if (load[i] + 0 < load[b] + 0) b = i
                     load[b] += $1; for (j = 2; j <= NF; j++) print b, $j }'
}
