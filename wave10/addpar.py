"""Insert t.Parallel() as the first statement of the named tests.
usage: addpar.py <file> <TestName>..."""
import sys
p = sys.argv[1]
s = open(p).read()
for name in sys.argv[2:]:
    hdr = "func %s(t *testing.T) {\n" % name
    assert s.count(hdr) == 1, name
    i = s.index(hdr) + len(hdr)
    if s[i:].startswith("\tt.Parallel()"):
        continue
    s = s[:i] + "\tt.Parallel()\n" + s[i:]
open(p, "w").write(s)
