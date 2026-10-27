#!/usr/bin/env python3
"""Fit a per-call latency model of the real pool from a VM log.

usage: calibrate.py <repo copy> <vm log>
Counts zfs calls (get/list vs mutating) per endpoint test on the simulator
(ZFS_TEST_CALLS=1), then least-squares fits real_seconds = a + b*get + c*mut
over the tests that passed on the real pool. Prints the fit and residuals.
"""
import json, os, re, subprocess, sys, collections

repo, vmlog = sys.argv[1], sys.argv[2]
env = dict(os.environ, GOTOOLCHAIN="go1.27.3", ZFS_TEST_CALLS="1")
out = subprocess.run(["go", "test", "-json", "-count=1", "-run", "^Test", "./internal/endpoint"],
                     cwd=repo, env=env, capture_output=True, text=True).stdout
calls = collections.defaultdict(lambda: [0, 0])
for line in out.splitlines():
    try:
        e = json.loads(line)
    except ValueError:
        continue
    m = re.search(r"CALLS get=(\d+) mut=(\d+)", e.get("Output", ""))
    if m and e.get("Test"):
        top = e["Test"].split("/")[0]
        calls[top][0] += int(m.group(1))
        calls[top][1] += int(m.group(2))
log = open(vmlog, errors="replace").read()
end = log.find("--- PASS: TestRoundTripAndCodes")  # endpoint binary only
real = {m.group(1): float(m.group(2)) for m in re.finditer(r"--- PASS: (Test\w+) \(([\d.]+)s\)", log[:end])}
rows = [(t, calls[t][0], calls[t][1], real[t]) for t in calls if t in real and real[t] > 0]
# normal equations for y = a + b g + c m
import itertools
X = [[1, g, m] for _, g, m, _ in rows]
y = [r for *_, r in rows]
XtX = [[sum(X[k][i] * X[k][j] for k in range(len(X))) for j in range(3)] for i in range(3)]
Xty = [sum(X[k][i] * y[k] for k in range(len(X))) for i in range(3)]
def solve(A, b):
    A = [row[:] + [v] for row, v in zip(A, b)]
    n = len(A)
    for i in range(n):
        p = max(range(i, n), key=lambda r: abs(A[r][i]))
        A[i], A[p] = A[p], A[i]
        for r in range(n):
            if r != i:
                f = A[r][i] / A[i][i]
                A[r] = [x - f * yv for x, yv in zip(A[r], A[i])]
    return [A[i][n] / A[i][i] for i in range(n)]
a, b, c = solve(XtX, Xty)
pred = [a + b * g + c * m for _, g, m, _ in rows]
ss = sum((p - v) ** 2 for p, v in zip(pred, y)); st = sum((v - sum(y) / len(y)) ** 2 for v in y)
print(json.dumps(dict(n=len(rows), intercept_s=round(a, 4), get_ms=round(b * 1000, 2), mut_ms=round(c * 1000, 2),
                      r2=round(1 - ss / st, 3), sum_real_s=round(sum(y), 2))))
for (t, g, m, r), p in sorted(zip(rows, pred), key=lambda x: -x[0][3]):
    print(f"{t:50s} get={g:3d} mut={m:3d} real={r:.2f} model={p:.2f}")
