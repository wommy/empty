#!/usr/bin/env python3
"""Per-test and per-spec-row killer sets from the wave8 mutation rerun (9f1790d).
A test is proposed redundant only when
  (a) every mutant it kills is killed by the model (TestModel/FuzzModel),
  (b) every spec row listing it has another killer: a kept test on that row,
      or the model when the row is in MODEL_ROWS (asserted by the model),
  (c) it is not in a hand-written tier (golden, csi-sanity, real-pool round trip,
      parser/classifier/fuzz units) and not about an area the model omits
      (exports, properties, Expand, List, limit_bytes, MinAge),
  (d) it does not encode contested behaviour (nfs_clients, hostnames,
      name-based clone sources) -- those are listed separately: retire with the
      decision, never port."""
import json, re, os, glob, collections, sys
W = os.path.dirname(os.path.abspath(__file__)) + "/.."
res = {}
for f in glob.glob(W + "/mut/res*.jsonl"):
    for l in open(f):
        r = json.loads(l); res[r["id"]] = r
res = {k: v for k, v in res.items() if not v["build_fail"]}
MODEL = {"TestModel", "FuzzModel"}
K = collections.defaultdict(set)
for mid, r in res.items():
    for t in r["fails"]:
        K[t].add(mid)
km = K["TestModel"] | K["FuzzModel"]
allk = set().union(*K.values()) if K else set()
spec = json.load(open(W + "/spec.json"))["rows"]
MODEL_ROWS = set("""OWN-01 OWN-02 OWN-04 OWN-05 OWN-06 OWN-07 OWN-08 OWN-10 OWN-11 OWN-12 OWN-13 OWN-14 OWN-15
VOL-01 VOL-06 VOL-07 VOL-08 VOL-09 VOL-12 VOL-13 CLN-01 CLN-02 CLN-04 CLN-05 CLN-07 CLN-08
DEL-01 DEL-02 DEL-03 DEL-04 DEL-05 DEL-06 DEL-07 DEL-08 SNP-01 SNP-02 SNP-06 SNP-07 SNP-08 SNP-10 SNP-12 SNP-14
ID-01 ERR-05 ERR-06 ERR-07 ERR-10 ERR-11 SCN-10 REC-01 REC-02 REC-03 REC-04 REC-07 REC-08 REC-09 REC-11 REC-12 CR-01""".split())
# tests and their source (9f1790d copy, where the mutants ran)
src = {}
for f in glob.glob(W + "/model-9f1790d/internal/**/*_test.go", recursive=True):
    txt = open(f).read()
    for m in re.finditer(r"^func ((?:Test|Fuzz)\w+)\(", txt, re.M):
        body = txt[m.end(): txt.find("\nfunc ", m.end()) if txt.find("\nfunc ", m.end()) > 0 else len(txt)]
        src[m.group(1)] = (os.path.relpath(f, W + "/model-9f1790d"), body)
src8 = {}
for f in glob.glob(W + "/model-copy/internal/**/*_test.go", recursive=True):
    txt = open(f).read()
    for m in re.finditer(r"^func ((?:Test|Fuzz)\w+)\(", txt, re.M):
        e = txt.find("\nfunc ", m.end()); src8[m.group(1)] = txt[m.end(): e if e > 0 else len(txt)]
tests = sorted(t for t in src if t not in MODEL and t != "TestModelMasksStillBite")
rows_of = collections.defaultdict(list)
for r in spec:
    for t in r["tests"]:
        rows_of[t].append(r["id"])
HAND = re.compile(r"Sanity|Golden|Example|RoundTrip|Classify|Parse|Decode|Fuzz(?!Model)|Listen|Lock(Parent)|Sim|OpKind|RejectsFlag|Capabilities|Validate|Encoding")
OMIT = re.compile(r"Export|Share|NFS|Etab|Propert|Expand|Pagination|List|LargerThanLimit|LimitOnly|OutOfRange|Abandoned|MinAge|Parameters|Capacit|PropertyPrefix|MissingParent")
def contested(body):
    why = []
    if re.search(r"nfs_clients|NFSClients|[Hh]ostname", body): why.append("nfs_clients/hostname")
    if "volume-source-for-volume" in body and not re.search(r"clone_source_for|CloneSourceFor", body): why.append("name-based clone source")
    return why
kept = set(tests); out = []
cands = sorted(tests, key=lambda t: len(K[t]))
for t in cands:
    f, body = src[t]
    reason = None
    if HAND.search(t): reason = "hand-written tier"
    elif OMIT.search(t): reason = "outside the model (exports/props/expand/list/limits/MinAge)"
    elif not K[t]: reason = "no mutant evidence (sample did not reach it)"
    elif not K[t] <= km: reason = "kills %d mutants the model misses" % len(K[t] - km)
    else:
        bad = [row for row in rows_of[t] if row not in MODEL_ROWS and not (kept - {t}) & set(next(r for r in spec if r["id"] == row)["tests"])]
        if bad: reason = "sole killer of " + ",".join(bad)
    c = contested(src8[t]) if t in src8 else ["gone at 8cc52ef"]
    if reason is None:
        kept.discard(t)
    out.append((t, f, len(K[t]), rows_of[t], reason, c))
red = [o for o in out if o[4] is None]
print(f"mutants analysed: {len(res)}  killed by any: {len(allk)}  by model: {len(km)}  ({100*len(km)/max(1,len(allk)):.0f}% of killed)")
print(f"model-only kills: {len(km - set().union(*(K[t] for t in tests)))}")
print(f"tests: {len(tests)}  proposed redundant: {len(red)}  kept: {len(tests)-len(red)}")
by = collections.Counter(o[1] for o in red)
print("redundant by file:", dict(by))
print("\n# redundant")
for t, f, n, rows, _, c in sorted(red, key=lambda o: o[1]):
    print(f"{t}\t{f}\tkills={n}\trows={','.join(rows) or '-'}\t{'CONTESTED: '+';'.join(c) if c else ''}")
print("\n# kept, by reason")
for why, n in collections.Counter((o[4].split(' ')[0] if o[4].startswith(('kills','sole')) else o[4]) for o in out if o[4]).most_common():
    print(f"{n}\t{why}")
print("\n# kept because the model misses their mutants (top)")
for o in sorted((o for o in out if o[4] and o[4].startswith("kills")), key=lambda o: -len(K[o[0]] - km))[:15]:
    print(o[0], o[4])
print("\n# contested (never port):")
for o in out:
    if o[5]: print(o[0], o[1], o[5], "redundant" if o[4] is None else "kept: " + o[4])
