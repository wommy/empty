#!/bin/bash
# failbefore.sh ROOT REV FILE FUNCS_FILE RUNPATTERN PKG OUT
# Replace FILE with its version at REV, append the Go source in FUNCS_FILE
# (new symbols the tests need), run the tests, restore FILE.
set -u
root=$1 rev=$2 file=$3 funcs=$4 pat=$5 pkg=$6 out=$7
cd "$root" || exit 1
cp "$file" "$file.keep"
git show "$rev:$file" > "$file"
if [ -s "$funcs" ]; then cat "$funcs" >> "$file"; fi
go test -count=1 "$pkg" -run "$pat" > "$out" 2>&1
echo "exit $?" >> "$out"
mv "$file.keep" "$file"
tail -40 "$out"
