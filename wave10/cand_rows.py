import json, sys
S = "/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad"
d = json.load(open(S + "/wave8/spec.json"))
cands = sys.argv[1].split(",")
for c in cands:
    rows = [r for r in d["rows"] if c in r["tests"]]
    print(c)
    for r in rows:
        print("   ", r["id"], r["status"], "others:", [t for t in r["tests"] if t != c])
