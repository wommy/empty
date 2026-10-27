#!/bin/bash
# Interleaved BenchmarkScan runs of the 654b864 and json-scan test binaries.
cd "$(dirname "$0")"
: >bench-interleaved.txt
for i in 1 2 3 4 5 6; do
	for b in base after; do
		echo "# $b run $i" >>bench-interleaved.txt
		./$b-endpoint.test -test.run '^$' -test.bench 'BenchmarkScan' -test.benchmem -test.count=1 | grep Benchmark | sed "s/^/$b /" >>bench-interleaved.txt
	done
done
uptime >>bench-interleaved.txt
