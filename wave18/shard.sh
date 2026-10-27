nfs='^(TestFileExport|TestNFSReplay$)'; split='^TestPropertyRoundTrip$'
awk -v nfs="$nfs" -v sp="$split" 'FNR == NR { if ($1 !~ /^#/ && NF == 2) w[$1] = $2; next }
       /^(Test|Fuzz|Example)/ { x = ($1 in w) ? w[$1] : 0.1
                 if ($1 ~ sp) { printf "S %.3f %s\n", x, $1; next }
                 g = ($1 ~ nfs) ? "nfs" : $1
                 wt[g] += x; m[g] = m[g] " " $1 }
       END { for (g in wt) printf "%.3f%s\n", wt[g], m[g] }' "$2" "$1" |
    sort -k1,1gr -k2,2 |
    awk -v n="$3" '$1 == "S" { for (i = 0; i < n; i++) { load[i] += $2 / n }; next }
                   { g[++k] = $0 }
                   END { for (x = 1; x <= k; x++) {
                           c = split(g[x], f, " "); b = 0
                           for (i = 1; i < n; i++) if (load[i] + 0 < load[b] + 0) b = i
                           load[b] += f[1]; if (f[1] > 1) print "  place", f[2], (c>2?"(+group)":""), f[1], "-> shard", b }
                         for (i = 0; i < n; i++) print "shard", i, load[i] }'
