"""disable.py <file> <old-substring-file> <new-substring-file>: swap one exact occurrence.

Used to capture fail-before evidence with only the fix disabled; the
caller restores the file from a copy afterwards.
"""
import sys
p, oldf, newf = sys.argv[1:4]
old = open(oldf).read()
new = open(newf).read()
s = open(p).read()
assert s.count(old) == 1, (p, s.count(old))
open(p, 'w').write(s.replace(old, new))
