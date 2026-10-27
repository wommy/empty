#!/usr/bin/env python3
"""Revert one verifier fix (D1, D2 or D3) in the endpoint package.

usage: revert.py <internal/endpoint dir> <D1|D2|D3>
Each replacement must match exactly once, so a revert never silently
does nothing.
"""
import sys

d, which = sys.argv[1], sys.argv[2]

D1 = {
    "export.go": [(
        """	if x := live.crossmntAncestors(row.Mount); len(x) > 0 {
		var s []string""",
        """	if x := live.crossmntAncestors(row.Mount); false && len(x) > 0 {
		var s []string""")],
    "endpoint.go": [(
        """	if x := live.exposure(realPath(row.Mount), publishWant(row, c)); len(x) > 0 {""",
        """	if x := live.exposure(realPath(row.Mount), publishWant(row, c)); false && len(x) > 0 {""")],
}

D2 = {
    "export.go": [
        # serves back to HEAD: our exact network's line in our mode
        ("""func (t Etab) serves(path string, c NFSClient) bool {
	want, err := parseClient(c.Host)""",
         """func (t Etab) serves(path string, c NFSClient) bool {
	if got, ok := t.Lookup(path, c.Host); true {
		return ok && !got.unknownMode && got.ReadOnly == c.ReadOnly
	}
	want, err := parseClient(c.Host)"""),
        # no option allow-list: nothing is broad
        ("""	return broad, crossmnt
}""", """	return false, crossmnt
}"""),
    ],
}

D3 = {
    "export.go": [(
        """	isLive = isLive || (!foreign(row) && len(live.covers(row.Mount, c.Host, want)) > 0)""",
        """	isLive = isLive || (false && !foreign(row) && len(live.covers(row.Mount, c.Host, want)) > 0)""")],
    "endpoint.go": [(
        """	if x := live.covers(realPath(row.Mount), h, unpublishWant(row, h)); len(x) > 0 {""",
        """	if x := live.covers(realPath(row.Mount), h, unpublishWant(row, h)); false && len(x) > 0 {""")],
}

# the after-apply re-checks alone (the planner parts kept)
D1e = {"endpoint.go": D1["endpoint.go"]}
D3e = {"endpoint.go": D3["endpoint.go"]}

for f, reps in {"D1": D1, "D2": D2, "D3": D3, "D1e": D1e, "D3e": D3e}[which].items():
    p = f"{d}/{f}"
    s = open(p).read()
    for old, new in reps:
        n = s.count(old)
        if n != 1:
            sys.exit(f"{which}: {f}: pattern matches {n} times: {old[:60]!r}")
        s = s.replace(old, new)
    open(p, "w").write(s)
print(f"reverted {which}")
