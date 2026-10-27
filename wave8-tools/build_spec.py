import json, sys, collections, os
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from rows import ROWS

OUT = "/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave8"
KINDS = ["invariant","idempotency","error-code","ownership","crash-convergence","OpenZFS-fact","CSI-contract","perf"]

ids = [r["id"] for r in ROWS]
dup = [i for i, c in collections.Counter(ids).items() if c > 1]
assert not dup, dup
rules = collections.Counter(r["rule"] for r in ROWS)
assert all(c == 1 for c in rules.values())
for r in ROWS:
    assert r["kind"] in KINDS, r
    assert r["type_eliminable"] in ("full","partial","none"), r
    assert r["drive"] in ("type","table","scenario"), r
    assert r["status"] in ("current","current-untested","open-finding","decided-pending"), r
    assert r["oracle"] in ("openzfs","nfs-utils","csi-spec","adr","self"), r

def count(field):
    return dict(collections.Counter(r[field] for r in ROWS).most_common())

summary = dict(
    rows=len(ROWS),
    by_kind={k: sum(1 for r in ROWS if r["kind"] == k) for k in KINDS},
    by_drive=count("drive"), by_type_eliminable=count("type_eliminable"),
    by_status=count("status"), by_oracle=count("oracle"),
    uncovered=sum(1 for r in ROWS if not r["tests"]),
    uncovered_current=[r["id"] for r in ROWS if not r["tests"] and r["status"] == "current"],
    external_oracle_scenarios=[r["id"] for r in ROWS if r["drive"] == "scenario" and r["oracle"] in ("openzfs","nfs-utils")],
)
cross = collections.defaultdict(lambda: collections.Counter())
for r in ROWS:
    cross[r["kind"]][r["drive"]] += 1
summary["kind_x_drive"] = {k: dict(cross[k]) for k in KINDS}

with open(f"{OUT}/spec.json", "w") as f:
    json.dump(dict(head="9f1790d", summary=summary, rows=ROWS), f, indent=1)

L = []
L.append("# zfs-endpoint executable spec (wave8)\n")
L.append(f"Generated from `wave8-tools/rows.py` by `build_spec.py`; {len(ROWS)} deduplicated rows against HEAD 9f1790d. Machine form: `wave8/spec.json`. Go table skeleton: `wave8/spec_rows.go.txt`.\n")
L.append("Fields: **drive** = what the residual test is after the refactor (type: compile-time plus one constructor test; table: a SpecRow; scenario: a multi-step or real-kernel test). **elim** = whether a type can make the violation unrepresentable. **status**: current (done and asserted), current-untested, open-finding (HEAD violates it), decided-pending (ADR/owner decided, not built). **oracle**: where the expected value must come from; `self` means our own code or model is the oracle (common-mode risk).\n")
def table(title, d):
    L.append(f"\n**{title}**\n\n| value | rows |\n|---|---|")
    for k, v in d.items():
        L.append(f"| {k} | {v} |")
table("By kind", summary["by_kind"])
table("By drive", summary["by_drive"])
table("By type_eliminable", summary["by_type_eliminable"])
table("By status", summary["by_status"])
table("By oracle", summary["by_oracle"])
L.append("\n**Kind x drive**\n\n| kind | type | table | scenario |\n|---|---|---|---|")
for k in KINDS:
    c = cross[k]
    L.append(f"| {k} | {c['type']} | {c['table']} | {c['scenario']} |")
L.append(f"\nRows with no covering test: {summary['uncovered']}. Scenario rows whose oracle is upstream (must run against real OpenZFS/nfs-utils, never the simulator alone): {', '.join(summary['external_oracle_scenarios'])}.\n")

dom = collections.OrderedDict()
for r in ROWS:
    dom.setdefault(r["id"].split("-")[0], []).append(r)
for d, rs in dom.items():
    L.append(f"\n## {d}\n")
    for r in rs:
        L.append(f"### {r['id']} ({r['kind']}; {r['drive']}; elim {r['type_eliminable']}; {r['status']}; oracle {r['oracle']})\n")
        L.append(r["rule"] + "\n")
        L.append(f"- source: {r['source']}")
        L.append(f"- tests: {', '.join(r['tests']) if r['tests'] else 'none'}")
        if r["type_idea"]:
            L.append(f"- type: {r['type_idea']}")
        L.append("")
with open(f"{OUT}/spec.md", "w") as f:
    f.write("\n".join(L) + "\n")
print(json.dumps(summary, indent=1))
