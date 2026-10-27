#!/bin/bash
# 10 interleaved rounds of BenchmarkScan/10000 (10 iterations each), base vs after; prints min and median.
cd "$(dirname "$0")"
: >bench10.txt
for i in $(seq 10); do
	for b in base after; do
		./$b-endpoint.test -test.run '^$' -test.bench 'BenchmarkScan/10000' -test.benchmem -test.benchtime=10x | grep Benchmark | sed "s/^/$b /" >>bench10.txt
	done
done
python3 -I - <<'EOF'
import statistics
rows = {}
for l in open('bench10.txt'):
    f = l.split()
    rows.setdefault(f[0], []).append((float(f[3]) / 1e6, int(f[7]), int(f[9])))
for k, v in sorted(rows.items()):
    t = sorted(x[0] for x in v)
    print(f"{k}: n={len(t)} min {t[0]:.1f} ms, median {statistics.median(t):.1f} ms, {v[0][1]} B/op, {v[0][2]} allocs/op")
EOF
uptime
