#!/usr/bin/env bash
# times qemu-img check on $1, 3 times; then truncated copies
img=$1
for i in 1 2 3; do
  s=$EPOCHREALTIME
  qemu-img check -q "$img"; rc=$?
  echo "check rc=$rc $(awk -v a="$s" -v b="$EPOCHREALTIME" 'BEGIN { printf "%.3f", b - a }')s"
done
sync
echo 3 >/proc/sys/vm/drop_caches 2>/dev/null && echo dropped
s=$EPOCHREALTIME
qemu-img check -q "$img"; rc=$?
echo "cold check rc=$rc $(awk -v a="$s" -v b="$EPOCHREALTIME" 'BEGIN { printf "%.3f", b - a }')s"
for frac in 0.5 0.9 0.999; do
  cp "$img" trunc.qcow2
  sz=$(stat -c %s "$img")
  truncate -s "$(awk -v s="$sz" -v f="$frac" 'BEGIN { printf "%d", s * f }')" trunc.qcow2
  qemu-img check -q trunc.qcow2 >trunc.out 2>&1; rc=$?
  echo "truncated to $frac: rc=$rc $(awk 'NR<=3' trunc.out | tr '\n' ' ')"
  rm -f trunc.qcow2
done
