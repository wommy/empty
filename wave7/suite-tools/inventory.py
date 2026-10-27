#!/usr/bin/env python3
"""Mechanical test inventory for the zfs endpoint suite.

usage: inventory.py <repo copy> <out dir> [<real-vm log>]

1. go test -json -count=1 ./...           -> wall time per top-level test (sim)
2. go test -c -cover -coverpkg=<prod>     -> one cover binary per package
3. <bin> -test.run ^T$ -test.coverprofile -> one profile per test
4. unique statements = blocks covered by exactly one test (suite-wide), and
   by exactly one of the real-capable tests (what each brings to the VM leg)
5. runs-on from the source (harness use, h.Sim/h.NFS) cross-checked with
   ci/expected-skips.txt; real wall time from a VM log when given.
Writes inventory.json, inventory.md, merged.cov, uncovered-funcs.txt.
"""
import json, os, re, subprocess, sys, collections, concurrent.futures as cf

repo, out = os.path.abspath(sys.argv[1]), os.path.abspath(sys.argv[2])
vmlog = sys.argv[3] if len(sys.argv) > 3 else None
os.makedirs(out + "/cov", exist_ok=True)
env = dict(os.environ, GOTOOLCHAIN="go1.27.3", GOFLAGS="-mod=mod")
here = os.path.dirname(os.path.abspath(__file__))
inv = json.load(open(here + "/invariants.json"))
mod = "github.com/wommy/empty"

def sh(args, cwd=repo, check=True):
    return subprocess.run(args, cwd=cwd, env=env, capture_output=True, text=True, check=check)

# 1. wall times on the simulator
times, status = {}, {}
for line in sh(["go", "test", "-json", "-count=1", "./..."], check=False).stdout.splitlines():
    try:
        e = json.loads(line)
    except ValueError:
        continue
    t = e.get("Test")
    if t and "/" not in t and e["Action"] in ("pass", "fail", "skip"):
        key = (e["Package"].removeprefix(mod + "/"), t)
        times[key], status[key] = e["Elapsed"], e["Action"]

pkgs = sh(["go", "list", "./..."]).stdout.split()
prod = [p for p in pkgs if "/gen/" not in p and not p.endswith("/zfstest") and not p.endswith("/leakcheck")]
coverpkg = ",".join(prod)

# 2. cover binaries
tests = collections.defaultdict(list)
for (p, t) in times:
    tests[p].append(t)
def build(p):
    b = f"{out}/bin/{p.replace('/', '_')}.test"
    os.makedirs(out + "/bin", exist_ok=True)
    sh(["go", "test", "-c", "-cover", "-covermode=set", "-coverpkg=" + coverpkg, "-o", b, "./" + p])
    return p, b
with cf.ThreadPoolExecutor(4) as ex:
    bins = dict(ex.map(build, tests))

# 3. one profile per test
def run(pt):
    p, t = pt
    prof = f"{out}/cov/{p.replace('/', '_')}.{t}.cov"
    r = subprocess.run([bins[p], "-test.run", f"^{t}$", "-test.count=1", "-test.coverprofile=" + prof],
                       cwd=f"{repo}/{p}", env=env, capture_output=True, text=True)
    return pt, prof, r.returncode
with cf.ThreadPoolExecutor(4) as ex:
    profs = {pt: prof for pt, prof, rc in ex.map(run, list(times))}

# 4. parse
blocks = {}            # block -> statements
covered = {}           # test -> set(blocks)
for pt, prof in profs.items():
    s = set()
    for line in open(prof).read().splitlines()[1:]:
        blk, n, c = line.rsplit(" ", 2)
        blocks[blk] = int(n)
        if int(c) > 0:
            s.add(blk)
    covered[pt] = s
total = sum(blocks.values())
who = collections.Counter(b for s in covered.values() for b in s)

# 5. runs-on, from the source
HARNESS = r"\bsetup(Prefix)?\(t|endpointClient\(t\)"
def bodies(p):
    src, helpers = {}, {}
    d = f"{repo}/{p}"
    for f in os.listdir(d):
        if f.endswith("_test.go"):
            txt = open(f"{d}/{f}").read()
            for m in re.finditer(r"^func (?:\([^)]*\) )?(\w+)\(.*?(?=^func |\Z)", txt, re.S | re.M):
                (src if re.match(r"(Test|Fuzz)", m.group(1)) else helpers)[m.group(1)] = m.group(0)
    # a test that calls a helper which calls the harness uses the harness
    via = [h for h, b in helpers.items() if re.search(HARNESS, b) and h not in ("setup", "setupPrefix", "endpointClient")]
    for t in src:
        for h in via:
            if re.search(r"\b%s\(" % h, src[t]):
                src[t] += "\n// via helper: setup(t)"
    return src
skips = set()
for line in open(f"{repo}/ci/expected-skips.txt"):
    f = line.split("#")[0].split()
    if len(f) == 3:
        skips.add((f"internal/{f[1]}", f[2]))
vm_pkgs = re.search(r"pkgs=\(([^)]*)\)", open(f"{repo}/ci/real-zfs-vm.sh").read()).group(1).split()
src = {p: bodies(p) for p in tests}
def runs_on(p, t):
    b = src[p].get(t, "")
    harness = re.search(HARNESS, b)
    simonly = "Sim(t)" in b or "failOn(h" in b or "requireSim(t)" in b  # failOn calls h.Sim(t)
    if p.removeprefix("internal/") not in vm_pkgs:
        return "unit"                       # never in a real-ZFS binary
    if not harness and "requireSim(t)" not in b:
        return "sim*"                       # inside the real binary, never touches the pool
    if simonly:
        return "sim"                        # skips on a real pool
    if "h.NFS(t)" in b:
        return "both(nfs)"
    return "both"
rows = []
for (p, t) in sorted(times, key=lambda k: (k[0], k[1])):
    ro = runs_on(p, t)
    rows.append(dict(pkg=p.removeprefix("internal/"), test=t, runs=ro, status=status[(p, t)],
                     sim_s=times[(p, t)], cls=inv.get(t, ["?", "?"])[0], inv=inv.get(t, ["?", "?"])[1],
                     stmts=sum(blocks[b] for b in covered[(p, t)]),
                     uniq=sum(blocks[b] for b in covered[(p, t)] if who[b] == 1),
                     skiplisted=(p, t) in skips))
# consistency: source says sim-only <=> expected-skips lists it
mismatch = [(r["pkg"], r["test"]) for r in rows if (r["runs"] == "sim") != r["skiplisted"]]

realset = [(f"internal/{r['pkg']}" if r["pkg"] != "zfs-endpoint" else r["pkg"], r["test"]) for r in rows if r["runs"].startswith("both")]
realset = [k for k in covered if (k[0], k[1]) in set(realset) or (k[0].removeprefix("internal/"), k[1]) in {(r["pkg"], r["test"]) for r in rows if r["runs"].startswith("both")}]
whoreal = collections.Counter(b for k in realset for b in covered[k])
for r in rows:
    k = next(k for k in covered if k[1] == r["test"] and k[0].endswith(r["pkg"]))
    r["uniq_real"] = sum(blocks[b] for b in covered[k] if whoreal[b] == 1) if k in realset else None

# real wall times (first occurrence: endpoint binary, csi mount run)
real = {}
if vmlog:
    for m in re.finditer(r"--- (PASS|SKIP|FAIL): ((?:Test|Fuzz)\w+) \(([\d.]+)s\)", open(vmlog, errors="replace").read()):
        real.setdefault(m.group(2), (m.group(1), float(m.group(3))))
for r in rows:
    r["real_s"] = real.get(r["test"], (None, None))[1]
    r["real_status"] = real.get(r["test"], (None, None))[0]

union = set().union(*covered.values())
union_real = set().union(*(covered[k] for k in realset))
summary = dict(total_stmts=total,
               sim_stmts=sum(blocks[b] for b in union),
               real_reachable_stmts=sum(blocks[b] for b in union_real),
               tests=len(rows), mismatch_runs_on_vs_skiplist=mismatch,
               vm_pkgs=vm_pkgs)
# redundant: zero unique statements and another test of the same class
bycls = collections.defaultdict(list)
for r in rows:
    bycls[r["cls"]].append(r)
for r in rows:
    r["redundant"] = r["uniq"] == 0 and len(bycls[r["cls"]]) > 1
json.dump(dict(summary=summary, rows=rows), open(out + "/inventory.json", "w"), indent=1)

# merged profile for go tool cover -func
with open(out + "/merged.cov", "w") as f:
    f.write("mode: set\n")
    for b, n in blocks.items():
        f.write(f"{b} {n} {1 if b in union else 0}\n")
fn = sh(["go", "tool", "cover", "-func=" + out + "/merged.cov"]).stdout
open(out + "/uncovered-funcs.txt", "w").write(
    "\n".join(l for l in fn.splitlines() if not l.rstrip().endswith("100.0%")))
with open(out + "/merged-real.cov", "w") as f:
    f.write("mode: set\n")
    for b, n in blocks.items():
        f.write(f"{b} {n} {1 if b in union_real else 0}\n")

def fmt(x):
    return "" if x is None else (f"{x:.2f}" if isinstance(x, float) else str(x))
with open(out + "/inventory.md", "w") as f:
    f.write(f"total {total} stmts; sim union {summary['sim_stmts']} ({100*summary['sim_stmts']/total:.1f}%); "
            f"real-reachable {summary['real_reachable_stmts']} ({100*summary['real_reachable_stmts']/total:.1f}%)\n\n")
    f.write("| pkg | test | runs | sim s | real s | stmts | unique | unique on real leg | class | invariant | redundant? |\n|---|---|---|---|---|---|---|---|---|---|---|\n")
    for r in rows:
        f.write(f"| {r['pkg']} | {r['test']} | {r['runs']} | {fmt(r['sim_s'])} | {fmt(r['real_s'])} | {r['stmts']} | {r['uniq']} | "
                f"{fmt(r['uniq_real'])} | {r['cls']} | {r['inv']} | {'yes' if r['redundant'] else ''} |\n")
print(json.dumps(summary, indent=1))
