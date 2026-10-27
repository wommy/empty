#!/usr/bin/env bash
# replay ci/real-zfs-guest.sh's sharder: shardsim.sh <list> <weights> [n]
# prints per-shard load (sum of weights) and members with weight >= 0.5
set -euo pipefail
nfs='^(TestFileExport|TestNFSReplay$)'
n=${3:-3}
awk -v nfs="$nfs" 'FNR == NR { if ($1 !~ /^#/ && NF == 2) w[$1] = $2; next }
     /^(Test|Fuzz|Example)/ { g = ($1 ~ nfs) ? "nfs" : $1
               wt[g] += ($1 in w) ? w[$1] : 0.1; m[g] = m[g] " " $1 }
     END { for (g in wt) printf "%.3f%s\n", wt[g], m[g] }' "$2" "$1" |
  sort -k1,1gr -k2,2 |
  awk -v n="$n" '{ b = 0; for (i = 1; i < n; i++) if (load[i] + 0 < load[b] + 0) b = i
                   load[b] += $1; if ($1 >= 0.5) big[b] = big[b] " " $2 "(" $1 ")" }
       END { for (i = 0; i < n; i++) printf "shard %d load %.2f:%s\n", i, load[i], big[i] }'
