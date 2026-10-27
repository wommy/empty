import json
import sys

# usage: rep.py FILE PAIRS.json  -- PAIRS is [[old, new], ...]; each old must occur once
p = sys.argv[1]
s = open(p).read()
for a, b in json.load(open(sys.argv[2])):
    if s.count(a) != 1:
        sys.exit("not exactly once: %r (%d)" % (a[:80], s.count(a)))
    s = s.replace(a, b)
open(p, "w").write(s)
