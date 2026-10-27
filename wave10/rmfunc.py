"""Remove top-level funcs (with their doc comment) from a Go file.
usage: rmfunc.py <file> <name>..."""
import re, sys
p = sys.argv[1]
s = open(p).read()
for name in sys.argv[2:]:
    m = re.search(r"^func %s\(" % re.escape(name), s, re.M)
    assert m, name
    start = m.start()
    # include the contiguous // comment block right above
    lines = s[:start].split("\n")
    # lines[-1] is "" (start is at line start)
    k = len(lines) - 1
    while k - 1 >= 0 and lines[k - 1].startswith("//"):
        k -= 1
    start = len("\n".join(lines[:k])) + (1 if k > 0 else 0)
    end = s.index("\n}\n", m.start()) + 3
    if s[end:end + 1] == "\n":
        end += 1
    s = s[:start] + s[end:]
    print("removed", name)
open(p, "w").write(s)
