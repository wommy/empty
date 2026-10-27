import json, os, sys, collections
W7 = "/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave7"
tree = sys.argv[1]
muts = {m["id"]: m for m in json.load(open(os.path.join(W7, "mutants.json")))}
tested = [json.loads(l) for l in open(os.path.join(W7, "results.jsonl"))]
ids = sorted(r["id"] for r in tested if r["status"] != "BUILD_FAIL")
c = collections.Counter()
miss = []
for mid in ids:
    m = muts[mid]
    src = open(os.path.join(W7, "mut-copy", m["file"]), "rb").read()
    head = open(os.path.join(tree, m["file"]), "rb").read()
    s, e = m["start"], m["end"]
    ok = False
    for ctx in (80, 48, 32, 20):
        pat = src[max(0, s - ctx):e + ctx]
        i = head.find(pat)
        if i >= 0 and head.find(pat, i + 1) < 0:
            ok = True
            break
    c[(m["file"], ok)] += 1
    if not ok:
        miss.append((mid, m["file"], m["line"], m["desc"]))
print(len(ids), sorted(c.items()))
for x in miss:
    print(*x)
