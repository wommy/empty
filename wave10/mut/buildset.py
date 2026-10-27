"""Build wave10/mut/mutants-head.json: wave7's 303 tested mutants on HEAD.

Anchored ones keep their wave7 id; the unanchored ones are replaced by a
seed-7 sample of HEAD mutgen mutants from the same file (ids 10000+)."""
import json, os, random, collections
D = os.path.dirname(os.path.abspath(__file__))
W7 = os.path.join(D, "../../wave7")
tree = os.path.join(D, "before0")
muts = {m["id"]: m for m in json.load(open(os.path.join(W7, "mutants.json")))}
tested = [json.loads(l) for l in open(os.path.join(W7, "results.jsonl"))]
ids = sorted(r["id"] for r in tested if r["status"] != "BUILD_FAIL")
gen = json.load(open(os.path.join(D, "headgen.json")))
out, need, taken = [], collections.Counter(), set()
for mid in ids:
    m = muts[mid]
    src = open(os.path.join(W7, "mut-copy", m["file"]), "rb").read()
    head = open(os.path.join(tree, m["file"]), "rb").read()
    s, e = m["start"], m["end"]
    hit = None
    for ctx in (80, 48, 32, 20):
        pre = src[max(0, s - ctx):s]
        pat = pre + src[s:e] + src[e:e + ctx]
        i = head.find(pat)
        if i >= 0 and head.find(pat, i + 1) < 0:
            hit = i + len(pre)
            break
    if hit is None:
        need[m["file"]] += 1
        continue
    n = dict(m, start=hit, end=hit + (e - s), origin="wave7")
    out.append(n)
    taken.add((n["file"], n["start"], n["repl"]))
rng = random.Random(7)
for f, k in sorted(need.items()):
    pool = [g for g in gen if g["file"] == f and (g["file"], g["start"], g["repl"]) not in taken]
    for g in rng.sample(pool, k):
        out.append(dict(g, id=10000 + g["id"], origin="head"))
json.dump(out, open(os.path.join(D, "mutants-head.json"), "w"), indent=0)
print(len(out), collections.Counter(m["origin"] for m in out), dict(need))
