#!/bin/bash
# Runs the nvlist_print_json_string mbrtowc loop under each locale.
cd "$(dirname "$0")"
gcc -O1 -o mbr mbr.c || exit 1
for L in C POSIX C.UTF-8; do
	for v in 'abc' $'caf\xc3\xa9' $'\xff' $'a\xf0\x9f\x98\x80b'; do
		printf '%s %q -> ' "$L" "$v"
		LC_ALL=$L ./mbr "$v"
	done
done
