import difflib, sys, os
W = "/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave11"
A = W + "/work/a"
def patch(name, edits, newfiles=None):
    out = []
    for path, reps in edits.items():
        old = open(os.path.join(A, path)).read()
        new = old
        for o, n in reps:
            assert new.count(o) == 1, (name, path, o[:60], new.count(o))
            new = new.replace(o, n)
        out += difflib.unified_diff(old.splitlines(True), new.splitlines(True), "a/" + path, "b/" + path)
    for path, content in (newfiles or {}).items():
        out += difflib.unified_diff([], content.splitlines(True), "/dev/null", "b/" + path)
    open(f"{W}/build-patches/{name}", "w").write("".join(out))
    print(name, sum(1 for l in out if l[:1] in "+-") , "changed lines")
