import sys, subprocess
d = "/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/"
plan = {}
for l in open(d + "plan.txt"):
    f = l.split()
    if len(f) == 2 and f[0].isdigit():
        plan.setdefault(f[1], set()).add(int(f[0]) // 3)
def counts(tsv):
    out = subprocess.run([sys.executable, "-I", d + "agg.py", d + tsv, d + "skips.txt", "500"], capture_output=True, text=True).stdout
    tests, sec = {}, False
    for l in out.splitlines():
        if l.startswith("-- top tests"):
            sec = True
            continue
        if l.startswith("--"):
            sec = False
        if sec:
            n, t = l.split()
            tests[t] = int(n)
    return tests
for tsv, reads in (("before.tsv", None), ("after.tsv", 3)):
    tests = counts(tsv)
    for p in (0, 1):
        tot = 0
        for t, n in tests.items():
            if p not in plan.get(t, set()):
                continue
            if t == "TestPropertyRoundTrip":
                n = n / 2 if reads is None else (n - 1) / 2 + reads
            tot += n
        print(tsv, "part", p, round(tot))
